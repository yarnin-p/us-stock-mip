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

func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
