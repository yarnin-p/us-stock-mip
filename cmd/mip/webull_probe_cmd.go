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

	// Every path the adapter sends, before the amend candidates. A path can be wrong
	// for as long as nobody sends it, and the moment it matters is the moment a stop
	// needs to move -- so the report leads with which of them the venue actually has.
	if reach, reachErr := client.ProbeReach(ctx, target); reachErr != nil {
		fmt.Fprintf(stdout, "\nreach      FAILED  %v\n", reachErr)
	} else {
		fmt.Fprintln(stdout,
			"\nevery path this adapter sends (reads, and orders that were never placed)")
		for _, probe := range reach {
			mark := "  "
			if probe.Verdict != webull.VerdictRecognised &&
				probe.Verdict != webull.VerdictAcceptedNothing {
				mark = "! "
			}
			fmt.Fprintf(
				stdout, "%s%-30s %-34s %s\n",
				mark, probe.Purpose, probe.Path, probe.Verdict,
			)
			if probe.Detail != "" && mark == "! " {
				fmt.Fprintf(stdout, "      %s\n", probe.Detail)
			}
		}
		fmt.Fprintln(stdout,
			"  place                          /openapi/trade/order/place         "+
				"NOT TESTED -- proving it means placing an order")
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
	works := map[string][]string{}
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
		works[probe.OrderType] = append(works[probe.OrderType], probe.Session)
	}
	fmt.Fprintln(stdout)
	for _, orderType := range []string{"LIMIT", "STOP_LOSS", "STOP_LOSS_LIMIT"} {
		fmt.Fprintf(
			stdout, "%-16s accepted with: %s\n", orderType, orNone(works[orderType]),
		)
	}
	switch {
	case len(works["STOP_LOSS"]) > 0:
		fmt.Fprintln(stdout,
			"\na plain stop is accepted. Send the value above and widen the engine's "+
				"session gate to match it.")
	case len(works["STOP_LOSS_LIMIT"]) > 0:
		fmt.Fprintln(stdout,
			"\na plain stop is refused and a stop-limit is not. That is the restriction "+
				"doing what it is for: a stop releases a market order and extended hours "+
				"does not take those, while a stop-limit releases a limit. Set "+
				"BRACKET_STOP_ORDER_TYPE=STOP_LOSS_LIMIT to protect a position outside the "+
				"regular session -- and read what it costs first: through a gap a limit can "+
				"fail to fill at all, and the position keeps falling.")
	default:
		fmt.Fprintln(stdout,
			"\nno protective order was accepted under any value. Read the refusals above: "+
				"if none of them names the session or the order type, the account is being "+
				"refused for something else entirely and this says nothing about sessions.")
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
