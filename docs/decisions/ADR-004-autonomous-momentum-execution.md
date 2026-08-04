# ADR-004: Deterministic Autonomous Momentum Execution

## Status

Accepted for paper forward testing. Live mode is opt-in.

## Context

The daily scanner, research, and ranking pipeline already produces an opening
list. Requiring a person to copy positions or approve every order prevents the
platform from reacting to short-lived small-cap momentum setups.

The execution path must remain deterministic, auditable, bounded by portfolio
risk, and independent of LLM output.

## Decision

- Select at most the top two candidates from the current US Eastern trading
  date. Candidates that leave the current list are removed unless they have an
  entry, position, or exit still being reconciled.
- Enter only after a controlled pullback and reclaim with acceptable spread and
  top-of-book buyer pressure.
- Size entries from configured dollar risk and stop distance. Revalidate
  buying power, account-wide day PnL, position limits, active-order
  reservations, loss cooldown, and the kill switch immediately before submit.
- Submit entry limits automatically when `AUTO_TRADING_ENABLED=true`.
  Unfilled live entries are cancelled after a short configurable timeout;
  partial entries cancel their remainder and protect the filled quantity.
- Arm an in-process fixed stop at entry and raise a trailing stop after the
  configured gain threshold. Exit orders are marketable limits derived from the
  latest Webull bid. Partial exits retain their actual remaining quantity under
  stop control.
- Use Webull account positions, order details, commissions, fees, and account
  day PnL as the source of truth in live mode. Paper mode never calls the broker.
- Persist every strategy state, order transition, fill, position update, and
  trade-journal row. Recover pending strategy intents from persisted execution
  orders after restart.
- If more than one Webull account is synchronized, require
  `WEBULL_ACCOUNT_ID`; never choose an account nondeterministically.
- Keep `TRADING_MODE=paper` as the default. Changing to live requires both
  `TRADING_MODE=live` and valid Webull trading credentials.

## Consequences

- The dashboard needs no manual position-entry form and can derive Daily PnL
  and entry/exit history from broker/execution data.
- A running market-data and execution service is currently required for stop
  enforcement. Broker-native protective orders and HA leader leasing are
  follow-up hardening before unattended production live trading.
- Historical Webull order rows are cumulative order-level records; multiple
  fills observed between polls may be represented as one incremental fill.
