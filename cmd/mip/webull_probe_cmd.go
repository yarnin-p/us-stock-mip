package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/momentum-intelligence-platform/mip/internal/config"
	"github.com/momentum-intelligence-platform/mip/internal/webull"
)

// runWebullProbe answers the one question the test suite cannot: does amending a
// working order actually reach Webull.
//
// It places nothing, cancels nothing and moves nothing. It walks up from the
// cheapest check to the most informative, stopping at the first that fails, so a
// broken token is never reported as a broken endpoint:
//
//	token    -- the credentials are usable at all
//	accounts -- the signed request surface works and yields a real account
//	history  -- the order family is reachable, read-only
//	amend    -- an order that was never placed, on each candidate endpoint
//
// The last step is the point. See webull.ProbeModifyEndpoints for why an
// amendment aimed at nothing is safe and still conclusive.
func runWebullProbe(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("webull-probe", flag.ContinueOnError)
	flags.SetOutput(stderr)
	accountID := flags.String(
		"account", "", "broker account to probe (default WEBULL_ACCOUNT_ID)",
	)
	symbol := flags.String(
		"symbol", "AAPL", "symbol used for the non-binding session previews",
	)
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing Webull probe flags: %w", err)
	}
	appConfig, err := config.LoadWebullAuth()
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}
	if strings.TrimSpace(appConfig.WebullAccessToken) == "" {
		return errors.New(
			"no Webull access token; run `mip webull-token ensure` first",
		)
	}
	client, err := webull.NewClient(
		appConfig.WebullAppKey, appConfig.WebullSecret,
		webull.WithBaseURL(appConfig.WebullBaseURL),
		webull.WithAlgorithm(appConfig.WebullAlgorithm),
		webull.WithAccessToken(appConfig.WebullAccessToken),
		webull.WithHTTPClient(&http.Client{Timeout: appConfig.HTTPTimeout}),
	)
	if err != nil {
		return err
	}
	ctx, stop := commandContext()
	defer stop()

	target := strings.TrimSpace(*accountID)
	if target == "" {
		target = strings.TrimSpace(appConfig.WebullAccountID)
	}
	accounts, err := client.Accounts(ctx)
	if err != nil {
		return fmt.Errorf(
			"listing accounts: %w\nthe signed request surface is not working, so "+
				"nothing further would mean anything", err,
		)
	}
	fmt.Fprintf(stdout, "accounts   ok      %d visible\n", len(accounts))
	if target == "" && len(accounts) > 0 {
		target = accounts[0].ID
	}
	if target == "" {
		return errors.New(
			"no account to probe; set WEBULL_ACCOUNT_ID or pass -account",
		)
	}

	if orders, err := client.OrderHistory(ctx, target); err != nil {
		fmt.Fprintf(stdout, "history    FAILED  %v\n", err)
		fmt.Fprintln(stdout,
			"           the read side of the order family is unreachable, which "+
				"makes a working amend endpoint unlikely")
	} else {
		fmt.Fprintf(stdout, "history    ok      %d orders\n", len(orders))
	}

	probes, err := client.ProbeModifyEndpoints(ctx, target)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "\namend candidates (each aimed at an order that was never placed)")
	verified := false
	for _, probe := range probes {
		marker := " "
		if probe.InUse() {
			marker = "*"
		}
		fmt.Fprintf(
			stdout, "%s %-34s account_id in %-5s  %s\n",
			marker, probe.Path, probe.AccountIDIn, probe.Verdict,
		)
		if probe.Detail != "" {
			fmt.Fprintf(stdout, "    %s\n", truncate(probe.Detail, 300))
		}
		if probe.InUse() && probe.Verdict == webull.VerdictRecognised {
			verified = true
		}
		if probe.Verdict == webull.VerdictAcceptedNothing {
			fmt.Fprintln(stdout,
				"    this endpoint said yes to amending an order that does not "+
					"exist, so a success from it proves nothing")
		}
	}
	fmt.Fprintln(stdout, "\n* is the path the execution adapter sends today")

	// The second question, and the one that decides how the premarket is traded.
	sessions, err := client.ProbeOrderSessions(ctx, target, *symbol)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout,
		"\nwhich sessions each order type is allowed in (previews only -- nothing is placed)")
	stopWorks := make([]string, 0, 4)
	limitWorks := make([]string, 0, 4)
	for _, probe := range sessions {
		fmt.Fprintf(
			stdout, "  %-10s support_trading_session=%-10s %s\n",
			probe.OrderType, probe.Session, probe.Verdict,
		)
		if probe.Verdict != webull.SessionAccepted {
			if probe.Verdict == webull.SessionRefused && probe.Detail != "" {
				fmt.Fprintf(stdout, "      %s\n", truncate(probe.Detail, 200))
			}
			continue
		}
		if probe.OrderType == "STOP_LOSS" {
			stopWorks = append(stopWorks, probe.Session)
		} else {
			limitWorks = append(limitWorks, probe.Session)
		}
	}
	fmt.Fprintf(stdout, "\nLIMIT accepted with: %s\n", orNone(limitWorks))
	fmt.Fprintf(stdout, "STOP_LOSS accepted with: %s\n", orNone(stopWorks))
	if len(stopWorks) == 0 {
		fmt.Fprintln(stdout,
			"no session value was accepted for a native stop. Either stops really are "+
				"regular-hours only, or every value tried was wrong -- read the refusals "+
				"above before concluding either.")
	} else {
		fmt.Fprintln(stdout,
			"a native stop is accepted, so the value above is the one the adapter should "+
				"send. If it covers the extended sessions, premarket trailing is possible "+
				"and the engine's session gate should be widened to match.")
	}
	switch {
	case verified:
		fmt.Fprintln(stdout,
			"verdict: the amend path in use is recognised by the venue. Trailing "+
				"can be trusted to reach the broker.")
	default:
		fmt.Fprintln(stdout,
			"verdict: the amend path in use is NOT confirmed. If another candidate "+
				"above came back recognised, that is the one the adapter should send; "+
				"until then treat every trailing amendment as unproven.")
	}
	return nil
}

func orNone(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, ", ")
}

func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
