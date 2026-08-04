package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/llm"
)

type SECDocumentSource struct {
	FiledAt, FormType, AccessionNo, SourceURL string
}

func (store *Store) LoadSECDocumentSources(
	ctx context.Context,
	ticker string,
	asOf time.Time,
) ([]SECDocumentSource, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT filed_at::text,form_type,accession_no,COALESCE(source_url,'')
		FROM sec_filings
		WHERE ticker=$1 AND available_at<=$2 AND COALESCE(source_url,'')<>''
			AND (
				form_type IN ('S-1','S-3','424B5','8-K','10-Q','10-K','DEF 14A')
				OR form_type LIKE 'SC 13D%'
				OR form_type LIKE 'SC 13G%'
			)
		ORDER BY filed_at DESC LIMIT 5`,
		strings.ToUpper(strings.TrimSpace(ticker)), asOf,
	)
	if err != nil {
		return nil, fmt.Errorf("loading SEC document sources: %w", err)
	}
	defer rows.Close()
	var sources []SECDocumentSource
	for rows.Next() {
		var source SECDocumentSource
		if err := rows.Scan(
			&source.FiledAt, &source.FormType, &source.AccessionNo, &source.SourceURL,
		); err != nil {
			return nil, fmt.Errorf("scanning SEC document source: %w", err)
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating SEC document sources: %w", err)
	}
	return sources, nil
}

func (store *Store) LoadLLMContext(
	ctx context.Context,
	workflow llm.Workflow,
	ticker string,
	asOf time.Time,
) (string, error) {
	ticker = strings.ToUpper(strings.TrimSpace(ticker))
	var query string
	var args []any
	switch workflow {
	case llm.WorkflowNews:
		query = `SELECT published_at,title,COALESCE(content,''),COALESCE(source_url,'')
			FROM news WHERE ticker=$1 AND available_at<=$2
			ORDER BY published_at DESC LIMIT 30`
		args = []any{ticker, asOf}
	case llm.WorkflowSEC:
		query = `SELECT filed_at,form_type,accession_no,COALESCE(source_url,''),
				COALESCE(atm_risk,0),COALESCE(offering_risk,0)
			FROM sec_filings WHERE ticker=$1 AND available_at<=$2
			ORDER BY filed_at DESC LIMIT 30`
		args = []any{ticker, asOf}
	case llm.WorkflowRanking:
		query = `SELECT concat_ws(' | ',
				'as_of='||r.as_of,
				'rank='||r.rank,
				'runner_probability='||r.runner_probability,
				'trading_phase='||r.trading_phase,
				'algorithm='||m.algorithm,
				'gap_percent='||COALESCE(f.gap_percent,0),
				'premarket_change='||COALESCE(f.premarket_change,0),
				'after_hour_change='||COALESCE(f.after_hour_change,0),
				'return_1d='||COALESCE(f.return_1d,0),
				'relative_volume='||COALESCE(f.relative_volume,0),
				'volume_spike='||COALESCE(f.volume_spike,0),
				'float_rotation='||COALESCE(f.float_rotation,0),
				'vwap_distance='||COALESCE(f.vwap_distance,0),
				'breakout_strength='||COALESCE(f.breakout_strength,0),
				'news_score='||COALESCE(f.news_score,0),
				'atm_risk='||COALESCE(f.atm_risk,0),
				'offering_risk='||COALESCE(f.offering_risk,0)
			)
			FROM candidate_rankings r
			JOIN stocks s ON s.id=r.stock_id
			JOIN model_versions m ON m.id=r.model_version_id
			LEFT JOIN feature_snapshots f
				ON f.stock_id=r.stock_id AND f.as_of=r.as_of
				AND f.calculator_version=m.calculator_version
				AND f.relative_volume_period=m.relative_volume_period
				AND f.ema_period=m.ema_period
				AND f.breakout_period=m.breakout_period
			WHERE s.ticker=$1 AND r.as_of<=$2::date AND m.trained_to<r.as_of
			ORDER BY r.as_of DESC,m.created_at DESC LIMIT 10`
		args = []any{ticker, asOf}
	case llm.WorkflowJournal:
		query = `SELECT entered_at,exited_at,ticker,entry_price,exit_price,
				strategy,pnl,COALESCE(notes,'')
			FROM trades WHERE entered_at<=$1
				AND ($2='' OR ticker=$2)
			ORDER BY entered_at DESC LIMIT 100`
		args = []any{asOf, ticker}
	default:
		return "", fmt.Errorf("unsupported LLM workflow %q", workflow)
	}
	rows, err := store.pool.Query(ctx, query, args...)
	if err != nil {
		return "", fmt.Errorf("loading %s LLM context: %w", workflow, err)
	}
	defer rows.Close()
	var builder strings.Builder
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return "", fmt.Errorf("reading %s LLM context: %w", workflow, err)
		}
		for index, value := range values {
			if index > 0 {
				builder.WriteString(" | ")
			}
			_, _ = fmt.Fprint(&builder, value)
		}
		builder.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterating %s LLM context: %w", workflow, err)
	}
	if builder.Len() == 0 {
		return "", errors.New("no point-in-time context found for LLM workflow")
	}
	return builder.String(), nil
}

func (store *Store) SaveLLMAnalysis(
	ctx context.Context,
	ticker string,
	asOf time.Time,
	source string,
	result llm.Result,
) (int64, error) {
	digest := sha256.Sum256([]byte(source))
	var id int64
	if err := store.pool.QueryRow(ctx, `
		INSERT INTO llm_analyses (
			workflow,ticker,as_of,provider,provider_endpoint,model,
			prompt_version,source_sha256,source_text,analysis
		) VALUES ($1,NULLIF($2,''),$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id`,
		result.Workflow, strings.ToUpper(strings.TrimSpace(ticker)), asOf,
		result.Provider, result.ProviderEndpoint, result.Model,
		result.PromptVersion, hex.EncodeToString(digest[:]), source, result.Text,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("saving LLM analysis: %w", err)
	}
	return id, nil
}
