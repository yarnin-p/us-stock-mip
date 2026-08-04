# ADR-005: Governed Learning and Realtime Order Flow

## Status

Accepted.

## Context

Candidate ranking must improve as new market outcomes arrive, but a newly
trained model must not replace a working production model on in-sample
accuracy alone. Execution also needs evidence from actual transactions, not
only a periodically sampled last price.

## Decision

- Build the runner target from outcomes after the feature date: a 50% next-day
  intraday high or a 100% fifth-session close. Rows without the complete
  five-session future horizon are excluded.
- Backfill the versioned daily feature set from stored Massive bars before the
  daily learning job. Preserve richer premarket, after-hours, research, and
  risk fields already calculated by the feature service.
- Split training and validation by whole trading dates. The newest 20% of
  dates are never passed to model fitting.
- Store training and validation accuracy, log loss, Brier score, positive
  rate, and top-decile precision for every version.
- Register every new version as a challenger. Promote it atomically to the
  single champion only when the validation sample floor is met, validation
  log loss improves by the configured margin, and top-decile precision does
  not regress beyond its configured tolerance. Record promotions and
  rejections in `model_learning_runs`.
- Sync the completed US session from Massive at 13:20 ICT, run daily learning
  at 13:30 ICT, and run inference for that session at 14:59 ICT. The premarket
  scanner consumes that latest
  point-in-time-safe probability when building its score.
- Subscribe to Webull `QUOTE` and `TICK` topics in one MQTT session. Keep a
  rolling order-flow window per active candidate and calculate quote count,
  transaction count, aggressive-buy volume ratio, uptick ratio, average
  top-of-book pressure, and price velocity.
- Require the configured order-flow thresholds in addition to pullback,
  reclaim, spread, and current buyer-pressure rules before entry.
- Keep the first premarket discovery pass broad and isolate Webull
  `INVALID_SYMBOL` responses recursively, so unsupported warrants or stale
  universe members cannot fail the valid portion of a 100-symbol batch.
- Fingerprint the complete deterministic strategy configuration and persist
  that version plus the decision-time order-flow snapshot on every plan.
- Persist executable top-of-book history and transaction ticks for later
  replay, slippage analysis, and execution-strategy research. Continue to
  persist broker orders, fills, positions, fees, and PnL as realized forward
  outcomes.
- Label Webull stock-book data as Nasdaq-scoped. It is useful execution
  context but is not treated as consolidated whole-market pressure.
- Evaluate spike discovery causally: a scanner or dynamic-list hit counts
  only when it was received no later than the first threshold crossing
  observed by our stored feed. Crossing and signal comparisons use receipt
  time so the evaluator does not credit data before the system received it.
- Mark observations received during the scanner's first minute as
  left-censored. They are counted as unknown, never as early coverage, because
  they do not prove that the system saw the beginning of the move.
- Mark model evidence as `FORWARD` only when both the model promotion and the
  complete ranking existed before the 04:00 ET session boundary. A model or
  ranking reconstructed later is explicitly `RETROSPECTIVE_BACKTEST`.
- A created but unpromoted challenger is never accepted as forward evidence.
- Known split execution dates are excluded from prediction metrics and remain
  visible only as corporate-action patterns.
- Persist each completed-date spike evaluation with its evidence class.
  Daily spike learning, completed-date evaluation, and next-session inference
  are scheduled separately so a ranking must exist before 04:00 ET to earn a
  `FORWARD` label.
- The production API image includes the LightGBM inference/training runtime;
  scheduled jobs use the same serialized artifact and bridge as offline
  validation rather than silently substituting a different model family.
- A spike model may seed the continuous discovery universe only when its
  validation set contains at least 50 rows, recall at 20 is at least 10%, and
  recall at 100 is at least 40%. Failing models remain visible for research
  but are blocked from candidate selection.
- Spike model promotion applies the same recall-at-20 and recall-at-100
  thresholds before the first model can become champion. A first model is not
  exempt from discovery-quality gates.
- Regularize the LightGBM bridge for rare-event ranking with bounded tree
  depth, conservative leaf sizes, row and feature subsampling, and a bounded
  maximum update. Dashboard probabilities are labeled as raw model scores;
  they are not presented as calibrated odds.
- The Spike Watch endpoint and dashboard expose the model gate, evidence
  class, validation recall, current scanner state, and Nasdaq-scoped
  top-of-book confirmation separately. A live market confirmation cannot
  upgrade retrospective model evidence into a forward prediction.
- Treat an empty news/filing result as `NO_STORED_CATALYST`, not proof that a
  move had no catalyst.

## Consequences

- ML ranks candidates; it does not bypass the deterministic strategy or risk
  engine and it never sends a broker order directly.
- `TRADING_MODE=paper` uses the same candidate, order-flow, strategy, and risk
  path and is the forward-test environment before live promotion.
- Model and strategy thresholds are independently configurable. A model
  promotion cannot silently change stop loss, trailing behavior, allocation,
  or execution limits.
- Microstructure replay is available only from the date quote/tick history
  collection began; earlier Massive daily bars support daily candidate
  backtests but cannot reconstruct historical order-book state.
- Backtests and forward tests share the same report format, but forward
  certification may consume only reports whose point-in-time causal flag is
  true.
- Webull may send a transient one-sided book during session transitions. Such
  an event is non-executable and is discarded without reconnecting the healthy
  MQTT session.
- Initial live promotion is certified only from the current strategy version's
  forward shadow outcomes: at least 20 valid closed cycles across at least
  three trading days, positive net PnL after simulated fees, and profit factor
  of at least 1.15. Actual live PnL, actual-fill replay, partial fills,
  protective-stop replacement, restart recovery, and disconnect recovery
  remain visible as post-live monitors; they cannot create a pre-live
  certification deadlock.
- Paper and shadow SELL fills apply the configured conservative fee model
  (`max(exit fee minimum, quantity × per-share fee)`, rounded up to the next
  cent). A fee-model change starts a new strategy evidence version rather than
  mixing incompatible forward samples.
- The daily spike model uses information complete before the target session.
  It cannot by itself anticipate an unscheduled catalyst arriving during the
  target session; that requires the realtime discovery, catalyst, and
  microstructure path.
