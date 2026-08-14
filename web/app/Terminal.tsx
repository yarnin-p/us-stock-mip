"use client";

/* The order terminal.
 *
 * This screen is dark on purpose. The rest of the console is a light, creamy
 * surface for reading; this is the one place that sends instructions about money,
 * and it should not look like the page you were browsing a second ago. The change
 * in colour is the signal that the consequences changed.
 *
 * The layout follows the order the decision is actually made: ticker, then how
 * much, then where the exits go, then trailing. Nothing is submitted from here --
 * the entry order goes through the execution path so it cannot route around the
 * risk gate or the kill switch. What this screen does is make the risk visible
 * before the money moves, which is the part that was missing.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  LadderPlane, LadderValues, countRungs, ladderErrorOf, presetLadder,
} from "./Ladder";
import { useCurrency } from "./currency";
import { sanitizeDecimal, sanitizeInteger, sanitizeTicker } from "./inputs";

const API = process.env.NEXT_PUBLIC_API_BASE ?? "http://localhost:8080";

type EntryPlan = {
  ticker: string;
  shares: number;
  entry_price: number;
  stop_price: number;
  target_price: number;
  cost: number;
  risk: number;
  reward: number;
  reward_risk: number;
  breakeven_win_rate: number;
  risk_percent_of_account?: number;
  sizing_rule: string;
  // What the money asked for, and what the book allowed. They differ exactly when the
  // position was cut to what can actually be sold.
  requested_shares?: number;
  depth_limited_shares?: number;
  bid_shares?: number;
  bid_value?: number;
  risk_flags: string[];
};

type BracketConfig = {
  StopLossPercent: number;
  TakeProfitPercent: number;
  TrailStopAfter: number;
  TrailStopDistance: number;
  TrailTargetAfter: number;
  TrailTargetDistance: number;
  BreakEvenAfter: number;
  BreakEvenFloor: number;
  ProfitLockAfter: number;
  ProfitLockFloor: number;
  FeeRoundTripPercent: number;
  PartialTPAfter: number;
  PartialTPFraction: number;
  PartialTPMinShares: number;
};

type BracketRecord = {
  id: number;
  mode: string;
  ticker: string;
  state: string;
  quantity: number;
  requested_entry: number;
  entry_price?: number;
  stop_price?: number;
  target_price?: number;
  high_water?: number;
  risk_flags: string[];
  note?: string;
  opened_at: string;
  /* manual_hold says the engine is recording what it would do and sending
   * nothing. It is shown on the row rather than buried in a panel, because a
   * position nobody is trailing must not look like one that is. */
  manual_hold?: boolean;
  partial_taken_quantity?: number;
  partial_order_id?: string;
  /* Empty means no stop order rests at the broker: outside the regular session
   * Webull accepts none, so the engine is holding the level itself. That is the one
   * thing on this screen that changes what happens if MIP goes down, so it is shown
   * on the row rather than left to be inferred. */
  stop_order_id?: string;
  stop_fired?: boolean;
  config: BracketConfig;
};

/* An order as the execution service sees it. The states matter here: create,
 * preview, approve and submit are four separate calls on purpose, and the approval
 * is the gate. This screen walks them in order rather than collapsing them, so the
 * click that spends money is its own click. */
type ExecutionOrder = {
  id: number;
  ticker: string;
  side: string;
  quantity: number;
  limit_price: number;
  state: string;
  estimated_cost: number;
  estimated_fee: number;
  filled_quantity: number;
  average_fill_price: number;
  risk?: { allowed?: boolean; reasons?: string[] };
};

type Adjustment = {
  id: number;
  trigger: string;
  previous_stop?: number;
  new_stop?: number;
  previous_target?: number;
  new_target?: number;
  last_price: number;
  high_water: number;
  applied: boolean;
  broker_error?: string;
  reason?: string;
  created_at: string;
};

const money = (value: number) =>
  value.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const formatBaht = (usd: number, rate: number) =>
  "฿" + Math.round(usd * rate).toLocaleString("en-US");
const pct = (value: number, digits = 1) => `${(value * 100).toFixed(digits)}%`;

/* Dime charges max($0.01 per share, 0.15% of value) each way, then 7% VAT on the
 * commission. Expressed as a fraction of notional the share count cancels out of both
 * terms, so the rate depends on price alone.
 *
 * That matters more than it sounds. Above $6.67 the percentage term wins and the round
 * trip is a flat 0.32%. Below it the per-share floor binds and the rate climbs as the
 * price falls: 1.3% at $1.62, 5.1% at $0.42. A single typed percentage is right for one
 * price and wrong by an order of magnitude for the rest of this book -- and it is wrong
 * in the direction that makes a break-even rung lose money, because the floor is stated
 * net of this number. */
const DIME_PER_SHARE = 0.01;
const DIME_RATE = 0.0015;
const DIME_VAT = 1.07;
const dimeRoundTrip = (price: number) =>
  2 * DIME_VAT * (price > 0 ? Math.max(DIME_PER_SHARE / price, DIME_RATE) : DIME_RATE);


/* Structural warnings are written out in full rather than shown as badges. A
 * three-letter tag is easy to scroll past; a sentence explaining that a stop may
 * not fill is not. */
const FLAG_COPY: Record<string, { title: string; body: string }> = {
  MICRO_FLOAT: {
    title: "small float — the stop may not work",
    body: "Low-float names halt often, and no order can be sent during a halt. Price can reopen straight through the stop. Position size is the only control still working.",
  },
  EXTREME_RVOL: {
    title: "volume far above normal",
    body: "The price is being set by an almost empty book, and can move violently in either direction.",
  },
  OVEREXTENDED: {
    title: "already extended",
    body: "Measured over 1,930 name-days: of those up more than 100%, only 29% closed green, and the give-back ran deeper than the continuation.",
  },
  DEPTH_CAPPED: {
    title: "size cut to what the book will take",
    body: "The money asked for more than the market will absorb, so the size was cut to what can actually be sold. A position larger than the book is not one with more risk; it is one whose exit does not exist at the price the plan assumed.",
  },
  DEPTH_THIN: {
    title: "thin book against this size",
    body: "The best bid holds far less than the position. Selling means walking down into the levels below, which is moving the price yourself — the stop will not fill where it was calculated.",
  },
  DEPTH_UNKNOWN: {
    title: "book depth unknown",
    body: "No bid has been observed for this name, so the size has not been checked against the book at all. Read the depth on the broker screen before sending.",
  },
};

export function TerminalView() {
  const [ticker, setTicker] = useState("");
  const [entry, setEntry] = useState("");
  const [basis, setBasis] = useState<"budget" | "risk">("budget");
  const [amount, setAmount] = useState("");
  const [equity, setEquity] = useState("20000");
  const [stopPct, setStopPct] = useState("10");
  const [targetPct, setTargetPct] = useState("25");
  // Percent and price are two ways of saying the same thing, and which one is
  // natural depends on the trade: a fixed risk rule is a percentage, a level off
  // the chart is a price. The wire contract stays in percent either way.
  const [exitUnit, setExitUnit] = useState<"pct" | "price">("pct");
  // Empty means "work it out from the entry price", which is right for Dime and right
  // for almost every edit. It is an override rather than a fixed default because
  // another broker, or a promotion, is a number this screen cannot know -- but a blank
  // field that quietly uses 0.35% at $0.40 is how a break-even rung ends up losing
  // 4.7% of the position.
  const [feeOverride, setFeeOverride] = useState("");
  /* Currency and rate are shared with every other screen, so switching here or on the
   * hub switches both. Prices stay in dollars either way -- see currency.ts. */
  const { currency, setCurrency, rate, setRate, format } = useCurrency();
  /* The ladder, as one value rather than fourteen. The bracket screen renders the same
   * component from the same shape, which is what stops the screen you adjust a live
   * position on from drifting away from the screen you planned it on.
   *
   * The design opens on the balanced preset with every rung armed. */
  const [ladder, setLadder] = useState<LadderValues>(() => presetLadder("balanced"));

  const [plan, setPlan] = useState<EntryPlan | null>(null);
  const [planError, setPlanError] = useState("");
  const [opening, setOpening] = useState(false);
  const [openError, setOpenError] = useState("");
  const [reload, setReload] = useState(0);

  /* What the server says about the symbol in the field. Debounced, because it asks on
   * a pause in typing rather than on every keystroke, and aborted on the next edit so
   * a slow answer for "KW" cannot land after "KWM". */
  const [symbol, setSymbol] = useState<{
    ticker: string; known: boolean; tradable: boolean; reason?: string; name?: string;
  } | null>(null);
  const [checking, setChecking] = useState(false);

  useEffect(() => {
    const clean = ticker.trim();
    if (clean.length < 1) { setSymbol(null); setChecking(false); return; }
    const controller = new AbortController();
    setChecking(true);
    const timer = setTimeout(() => {
      fetch(`${API}/symbols/${encodeURIComponent(clean)}`, { signal: controller.signal })
        .then((response) => (response.ok ? response.json() : null))
        .then((answer) => { if (answer) setSymbol(answer); })
        .catch(() => {})
        .finally(() => setChecking(false));
    }, 250);
    return () => { controller.abort(); clearTimeout(timer); setChecking(false); };
  }, [ticker]);

  const symbolBad = Boolean(symbol && symbol.ticker === ticker.trim() && !symbol.tradable);

  /* The hub hands the ticker over in the query string. Read after mount rather than
   * during render: the server has no location to read, so initialising state from it
   * would make the first client pass disagree with the server HTML.
   *
   * Only on load, and only into an empty field -- arriving with ?ticker=X and then
   * typing something else should not have the URL win back on the next render. */
  useEffect(() => {
    const passed = new URLSearchParams(window.location.search).get("ticker");
    if (!passed) return;
    const clean = passed.trim().toUpperCase().slice(0, 8);
    if (clean) setTicker((current) => (current === "" ? clean : current));
  }, []);
  /* The review step. Nothing is written until it has been seen once -- the design's
   * two-step, and the reason the primary button opens a sheet instead of acting. */
  const [confirming, setConfirming] = useState(false);
  const [saved, setSaved] = useState<{ id: number; ticker: string } | null>(null);

  // ±0.5 in percent, ±0.01 in price. Clamped at zero: a negative stop is not a stop.
  const nudge = useCallback(
    (set: (value: string) => void, current: string, direction: 1 | -1) => {
      const step = exitUnit === "pct" ? 0.5 : 0.01;
      const next = Math.max(0, (Number(current) || 0) + direction * step);
      set(exitUnit === "pct" ? String(Number(next.toFixed(2))) : next.toFixed(2));
    },
    [exitUnit],
  );

  /* The ratio pills set the target from the stop and force percent, because a ratio
   * is a statement about distances and only percent expresses that without knowing
   * the price. */
  const setRatio = useCallback(
    (ratio: number) => {
      const stop = Number(exitUnit === "pct" ? stopPct : stopPct);
      if (exitUnit === "price") {
        const entryPrice = Number(entry);
        if (!(entryPrice > 0) || !(stop > 0)) return;
        const stopFraction = 1 - stop / entryPrice;
        setStopPct(String(Number((stopFraction * 100).toFixed(2))));
        setTargetPct(String(Number((stopFraction * 100 * ratio).toFixed(2))));
        setExitUnit("pct");
        return;
      }
      if (!(stop > 0)) return;
      setTargetPct(String(Number((stop * ratio).toFixed(2))));
    },
    [entry, exitUnit, stopPct],
  );

  /* The fee the rest of the screen spends. An override wins when one is typed;
   * otherwise it follows the entry price down, which is the whole point. `auto` is
   * kept so the field can say which of the two is in force -- a computed number that
   * cannot be told apart from a typed one invites the question of whether it updated. */
  const fee = useMemo(() => {
    const typed = Number(feeOverride);
    if (feeOverride.trim() !== "" && typed >= 0) {
      return { fraction: typed / 100, auto: false };
    }
    return { fraction: dimeRoundTrip(Number(entry)), auto: true };
  }, [feeOverride, entry]);

  // Whichever unit is typed, the request carries percentages -- the server sizes
  // and stores in fractions of entry, so a bracket read back later shows the rule
  // it ran under rather than a price that has since moved.
  const exits = useMemo(() => {
    const entryPrice = Number(entry);
    if (!(entryPrice > 0)) return null;
    if (exitUnit === "pct") {
      const stop = Number(stopPct) / 100;
      const target = Number(targetPct) / 100;
      if (!(stop > 0) || !(target > 0)) return null;
      return {
        stop, target,
        stopPrice: entryPrice * (1 - stop),
        targetPrice: entryPrice * (1 + target),
      };
    }
    const stopPrice = Number(stopPct);
    const targetPrice = Number(targetPct);
    if (!(stopPrice > 0) || !(targetPrice > 0)) return null;
    if (stopPrice >= entryPrice || targetPrice <= entryPrice) return null;
    return {
      stop: 1 - stopPrice / entryPrice,
      target: targetPrice / entryPrice - 1,
      stopPrice, targetPrice,
    };
  }, [entry, exitUnit, stopPct, targetPct]);

  const request = useMemo(() => {
    const entryPrice = Number(entry);
    const size = Number(amount);
    if (!ticker.trim() || !(entryPrice > 0) || !(size > 0) || !exits) return null;
    return {
      ticker: ticker.trim().toUpperCase(),
      entry_price: entryPrice,
      ...(basis === "budget" ? { budget: size } : { risk_amount: size }),
      stop_loss_percent: exits.stop,
      take_profit_percent: exits.target,
      account_equity: Number(equity) || 0,
      /* The ladder goes with every plan.
       *
       * This used to be gated on an `advanced` flag from the design before the
       * handoff, where the rungs lived behind a toggle. The handoff made the ladder
       * step two of two -- always shown, always editable -- and the toggle went, but
       * the gate stayed. Nothing ever set the flag again, so every plan saved from
       * this screen carried a bare stop and target while the screen showed four rungs
       * and the bar agreed with it. The numbers were read, validated, priced and
       * displayed, and then dropped on the way out.
       *
       * There is no flag now. What is on the screen is what is sent. */
      trail_stop_after: Number(ladder.trailStopAfter) / 100,
      trail_stop_distance: Number(ladder.trailStopDistance) / 100,
      trail_target_after: Number(ladder.trailTargetAfter) / 100,
      trail_target_distance: Number(ladder.trailTargetDistance) / 100,
      fee_round_trip_percent: fee.fraction,
      // Omitted rather than zeroed when off: the server refuses a floor with
      // no activation, and sending halves of a disabled rung would trip that.
      ...(ladder.breakEvenOn
        ? {
            break_even_after: Number(ladder.breakEvenAfter) / 100,
            break_even_floor: Number(ladder.breakEvenFloor) / 100,
          }
        : {}),
      ...(ladder.profitLockOn
        ? {
            profit_lock_after: Number(ladder.profitLockAfter) / 100,
            profit_lock_floor: Number(ladder.profitLockFloor) / 100,
          }
        : {}),
      ...(ladder.partialOn
        ? {
            partial_tp_after: Number(ladder.partialAfter) / 100,
            partial_tp_fraction: Number(ladder.partialFraction) / 100,
            partial_tp_min_shares: Number(ladder.partialMinShares),
          }
        : {}),
    };
  }, [
    ticker, entry, basis, amount, equity, exits,
    ladder.trailStopAfter, ladder.trailStopDistance, ladder.trailTargetAfter, ladder.trailTargetDistance,
    ladder.breakEvenOn, ladder.breakEvenAfter, ladder.breakEvenFloor,
    ladder.profitLockOn, ladder.profitLockAfter, ladder.profitLockFloor, fee,
    ladder.partialOn, ladder.partialAfter, ladder.partialFraction, ladder.partialMinShares,
  ]);

  // The ordering rules live with the ladder itself, so both screens refuse the same
  // shapes for the same reasons.
  // Always, for the same reason the rungs are always sent: this was gated too, so a
  // ladder the server would have refused reached SAVE PLAN looking fine.
  const ladderError = useMemo(() => ladderErrorOf(ladder), [ladder]);

  // Switching units converts what is already typed, so the levels do not jump.
  const switchUnit = useCallback((next: "pct" | "price") => {
    if (next === exitUnit) return;
    const entryPrice = Number(entry);
    if (entryPrice > 0 && exits) {
      if (next === "price") {
        setStopPct(exits.stopPrice.toFixed(2));
        setTargetPct(exits.targetPrice.toFixed(2));
      } else {
        setStopPct((exits.stop * 100).toFixed(2).replace(/\.?0+$/, ""));
        setTargetPct((exits.target * 100).toFixed(2).replace(/\.?0+$/, ""));
      }
    }
    setExitUnit(next);
  }, [entry, exitUnit, exits]);

  // The preview is a pure calculation server-side, so it can run on every edit.
  // Seeing the risk change as you type is the whole point of the screen.
  useEffect(() => {
    if (!request) {
      setPlan(null);
      setPlanError("");
      return;
    }
    const controller = new AbortController();
    const timer = setTimeout(() => {
      fetch(`${API}/brackets/preview`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(request),
        signal: controller.signal,
      })
        .then(async (response) => {
          const answer = await response.json();
          if (!response.ok) throw new Error(answer?.error ?? `HTTP ${response.status}`);
          setPlan(answer as EntryPlan);
          setPlanError("");
        })
        .catch((cause) => {
          if (controller.signal.aborted) return;
          setPlan(null);
          setPlanError(cause instanceof Error ? cause.message : "preview failed");
        });
    }, 180);
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [request]);

  const open = useCallback(async () => {
    if (!request) return;
    setOpening(true);
    setOpenError("");
    try {
      const response = await fetch(`${API}/brackets`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(request),
      });
      const answer = await response.json();
      if (!response.ok) throw new Error(answer?.error ?? `HTTP ${response.status}`);
      setReload((value) => value + 1);
      setConfirming(false);
      setSaved({ id: answer?.id ?? 0, ticker: request.ticker });
      setTicker("");
      setEntry("");
      setAmount("");
    } catch (cause) {
      setOpenError(cause instanceof Error ? cause.message : "could not open");
    } finally {
      setOpening(false);
    }
  }, [request]);


  const riskUsd = plan?.risk ?? 0;
  const riskMoney = format(riskUsd);
  const budgetUse = plan?.risk_percent_of_account
    ? Math.min(1, plan.risk_percent_of_account / 0.01)
    : 0;
  // Counted against the size actually being sent, not against a hard-coded four:
  // a rung that is switched on can still be unable to fire on a position this small,
  // and the bar used to report it as armed right up to the moment it did nothing.
  const rungs = countRungs(ladder, plan?.shares ?? 0);
  const canSend =
    Boolean(request) && ladderError === "" && Boolean(plan) && !symbolBad;

  return (
    <div className="tg tg-page">
      {/* portal bar — the way back is a pill, not a rail */}
      <div className="tg-portalbar">
        <a className="tg-back" href="/hub">‹ All apps</a>
        <span className="tg-crumb">
          TradeEdge portal <b>Order Terminal</b>
        </span>
        <span className="tg-barspacer" />
        {ticker && (
          <span className="tg-pill">
            <b>{ticker}</b>
            {Number(entry) > 0 && <em>${money(Number(entry))}</em>}
          </span>
        )}
        <CurrencySwitch
          currency={currency} onCurrency={setCurrency}
          rate={rate} onRate={setRate}
        />
        <ModeBadge />
        <span className="tg-avatar">Y</span>
      </div>

      <div className="tg-titlerow">
        <div>
          <div className="tg-status">
            <i className="tg-dot-live" /> Market data live · engine maintains the ladder
          </div>
          <h1 className="tg-h1">Order Terminal</h1>
        </div>
        <div className="tg-rr">
          {([1.5, 2, 3] as const).map((ratio) => (
            <button key={ratio} type="button" onClick={() => setRatio(ratio)}>
              {ratio} : 1
            </button>
          ))}
        </div>
      </div>

      <div className="tg-main">
        <section className="tg-plane tg-enter">
          <div className="tg-planehead">
            <h2>The ticket</h2>
            <span className="tg-step">step 1 of 2</span>
          </div>

          <div className="tg-fields">
            <label className={`tg-card${symbolBad ? " bad" : ""}`}>
              <span>
                Ticker
                {symbol?.name && symbol.tradable && (
                  <em className="tg-symname">{symbol.name}</em>
                )}
              </span>
              <input
                className="tg-in tg-in-ticker" value={ticker} placeholder="RCEL"
                spellCheck={false} autoComplete="off"
                aria-invalid={symbolBad}
                onChange={(event) => setTicker(sanitizeTicker(event.target.value))}
              />
              {symbolBad && <em className="tg-symbad">{symbol?.reason}</em>}
              {checking && !symbolBad && <em className="tg-symcheck">checking…</em>}
            </label>
            <label className="tg-card">
              <span>Entry price</span>
              <span className="tg-inwrap">
                <i className="tg-prefix">$</i>
                <input
                  className="tg-in" value={entry} inputMode="decimal" placeholder="7.77"
                  onChange={(event) => setEntry(sanitizeDecimal(event.target.value))}
                />
              </span>
            </label>
          </div>

          <div className="tg-sizecard">
            <div className="tg-sizehead">
              <span className="tg-cardlabel" style={{ margin: 0 }}>Position size</span>
              <div className="tg-seg">
                <button
                  type="button" className={basis === "budget" ? "on" : ""}
                  onClick={() => setBasis("budget")}
                >
                  By cash
                </button>
                <button
                  type="button" className={basis === "risk" ? "on" : ""}
                  onClick={() => setBasis("risk")}
                >
                  By risk
                </button>
              </div>
            </div>
            <div className="tg-amountrow">
              <i className="tg-prefix">$</i>
              <input
                className="tg-in tg-in-amount" value={amount} inputMode="decimal"
                placeholder={basis === "budget" ? "200" : "40"}
                onChange={(event) => setAmount(sanitizeDecimal(event.target.value))}
              />
              <span className="tg-shares">
                {plan ? `= ${plan.shares.toLocaleString()} shares` : ""}
              </span>
            </div>
            <div className="tg-quick">
              {/* Labels exactly as the design writes them: "1000", not "1,000". */}
              {["100", "200", "500", "1000"].map((value) => (
                <button key={value} type="button" onClick={() => setAmount(value)}>
                  ${value}
                </button>
              ))}
            </div>
          </div>

          <div className="tg-exits">
            <div className="tg-exit tg-sl">
              <div className="tg-exithead">
                <span>STOP LOSS</span>
                <div className="tg-steppers">
                  <button type="button" className="tg-stepper" aria-label="lower stop"
                    onClick={() => nudge(setStopPct, stopPct, -1)}>−</button>
                  <button type="button" className="tg-stepper" aria-label="raise stop"
                    onClick={() => nudge(setStopPct, stopPct, 1)}>+</button>
                </div>
              </div>
              <div className="tg-exitvalue">
                <input
                  className="tg-in tg-in-exit" value={stopPct} inputMode="decimal"
                  placeholder={exitUnit === "pct" ? "10" : "2.07"}
                  onChange={(event) => setStopPct(sanitizeDecimal(event.target.value))}
                />
                <i>{exitUnit === "pct" ? "%" : "$"}</i>
              </div>
              <div className="tg-exitfoot">
                {exits
                  ? `$${exits.stopPrice.toFixed(2)}${
                      plan ? ` · −${format(plan.risk)}` : ""
                    }`
                  : "below entry"}
              </div>
            </div>
            <div className="tg-exit tg-tp">
              <div className="tg-exithead">
                <span>TAKE PROFIT</span>
                <div className="tg-steppers">
                  <button type="button" className="tg-stepper" aria-label="lower target"
                    onClick={() => nudge(setTargetPct, targetPct, -1)}>−</button>
                  <button type="button" className="tg-stepper" aria-label="raise target"
                    onClick={() => nudge(setTargetPct, targetPct, 1)}>+</button>
                </div>
              </div>
              <div className="tg-exitvalue">
                <input
                  className="tg-in tg-in-exit" value={targetPct} inputMode="decimal"
                  placeholder={exitUnit === "pct" ? "25" : "2.88"}
                  onChange={(event) => setTargetPct(sanitizeDecimal(event.target.value))}
                />
                <i>{exitUnit === "pct" ? "%" : "$"}</i>
              </div>
              <div className="tg-exitfoot">
                {exits
                  ? `$${exits.targetPrice.toFixed(2)}${plan ? ` · +$${money(plan.reward)}` : ""}`
                  : "above entry"}
              </div>
            </div>
          </div>

          <div className="tg-measure">
            <span className="tg-measurelabel">measure in</span>
            <div className="tg-seg">
              <button type="button" className={exitUnit === "pct" ? "on" : ""}
                onClick={() => switchUnit("pct")}>percent</button>
              <button type="button" className={exitUnit === "price" ? "on" : ""}
                onClick={() => switchUnit("price")}>price</button>
            </div>
            <label className="tg-pillfield pushed">
              <span>Equity</span>
              <input value={equity} inputMode="decimal"
                onChange={(event) => setEquity(sanitizeDecimal(event.target.value))} />
            </label>
            <label className="tg-pillfield narrow">
              <span>Fee %</span>
              {/* Shows the effective rate, which is the Dime one worked out from the
                  entry price unless something has been typed over it. */}
              <input
                value={feeOverride || (fee.fraction * 100).toFixed(2)}
                inputMode="decimal"
                onChange={(event) => setFeeOverride(sanitizeDecimal(event.target.value))}
              />
            </label>
          </div>
          {!exits && Number(entry) > 0 && (
            <p className="tg-err">
              {exitUnit === "price"
                ? "Stop must sit below entry and target above it."
                : "Stop and target must both be above zero."}
            </p>
          )}
          {planError && <p className="tg-err">{planError}</p>}
        </section>

        <div className="tg-col">
          <section className="tg-risk tg-enter tg-enter-1" key={riskUsd.toFixed(2)}>
            <h2>WHAT YOU ARE RISKING</h2>
            <div className="tg-risktop">
              <div>
                <div className="tg-riskbaht">{riskMoney}</div>
                <div className="tg-risksub">
                  {currency === "THB" ? `$${money(riskUsd)}` : formatBaht(riskUsd, rate)}
                  {" · "}USD/THB {rate.toFixed(2)}
                </div>
              </div>
              <div className="tg-riskpct">
                <b>{plan?.risk_percent_of_account ? pct(plan.risk_percent_of_account, 2) : "0.00%"}</b>
                <span>of equity</span>
              </div>
            </div>
            <div className="tg-meter"><i style={{ width: `${budgetUse * 100}%` }} /></div>
            <div className="tg-metercap">
              <span>risk budget used · 1% of equity</span>
              <b>{Math.round(budgetUse * 100)}%</b>
            </div>
          </section>

          <div className="tg-stack tg-enter tg-enter-2">
            <div className="tg-stackrow tg-tp">
              <b>TAKE PROFIT</b>
              <span className="tg-stackright">
                <span>{plan ? `+${((plan.target_price / plan.entry_price - 1) * 100).toFixed(1)}%` : ""}</span>
                <i>{plan ? `$${money(plan.target_price)}` : "—"}</i>
              </span>
            </div>
            <div className="tg-stackrow tg-entry">
              <b>ENTRY</b>
              <span className="tg-stackright">
                <span>{plan ? `${plan.shares.toLocaleString()} sh · $${money(plan.cost)}` : ""}</span>
                <i>{plan ? `$${money(plan.entry_price)}` : "—"}</i>
              </span>
            </div>
            <div className="tg-stackrow tg-sl">
              <b>STOP LOSS</b>
              <span className="tg-stackright">
                <span>{plan ? `−${((1 - plan.stop_price / plan.entry_price) * 100).toFixed(1)}%` : ""}</span>
                <i>{plan ? `$${money(plan.stop_price)}` : "—"}</i>
              </span>
            </div>
          </div>

          <div className="tg-stats tg-enter tg-enter-3">
            <div className="tg-stat">
              <b>{plan ? `${plan.reward_risk.toFixed(2)}:1` : "—"}</b>
              <span>reward for every unit of risk</span>
            </div>
            <div className="tg-stat">
              <b>{plan ? pct(plan.breakeven_win_rate) : "—"}</b>
              <span>win rate needed to break even</span>
            </div>
            <div className="tg-stat">
              <b className="good">{plan ? `$${money(plan.reward)}` : "—"}</b>
              <span>gain at target, net of fees</span>
            </div>
          </div>

          {plan && <AccountRiskWarning share={plan.risk_percent_of_account} />}
          {plan?.risk_flags.map((flag) => {
            const key = flag.split(":")[0];
            const copy = FLAG_COPY[key];
            return (
              <div className="tm-flag" key={flag}>
                <strong>{copy?.title ?? key}</strong>
                <p>{copy?.body ?? flag}</p>
              </div>
            );
          })}
        </div>
      </div>

      <LadderPlane
        values={ladder}
        onChange={setLadder}
        title={<>The exit ladder <span className="tg-step">step 2 of 2</span></>}
        subtitle="Every rung only raises the floor — none of them lowers it."
        error={ladderError}
      />

      <PlansInPlay reload={reload} />

      {saved && (
        <div className="tg-sheet done">
          <div>
            <div className="headline">
              Bracket saved · plan #{saved.id || "—"}
            </div>
            <div className="detail">
              {saved.ticker} — nothing has reached the broker yet; arm it once you have
              the fill.
            </div>
          </div>
          <div className="tg-sheetactions">
            <button type="button" className="tg-confirm" onClick={() => setSaved(null)}>
              Write another ›
            </button>
          </div>
        </div>
      )}

      {confirming && plan && (
        <div className="tg-sheet review">
          <div>
            <h3>REVIEW — PAPER, simulated</h3>
            <div className="headline">
              {plan.shares.toLocaleString()} {plan.ticker} @ ${money(plan.entry_price)}
            </div>
            <div className="detail">
              SL ${money(plan.stop_price)} · TP ${money(plan.target_price)} · risk{" "}
              {riskMoney} · {rungs.armed} of {rungs.total} rungs armed
              {rungs.note ? ` · ${rungs.note}` : ""}
            </div>
          </div>
          <div className="tg-sheetactions">
            <button type="button" className="tg-cancel" onClick={() => setConfirming(false)}>
              Cancel
            </button>
            <button type="button" className="tg-confirm" disabled={opening} onClick={open}>
              {opening ? "Saving…" : "SAVE PLAN"}
            </button>
          </div>
        </div>
      )}

      <div className="tg-bar">
        <div className="tg-barstat">
          <span>Position</span>
          <b>{plan ? `${plan.shares.toLocaleString()} sh · $${money(plan.cost)}` : "—"}</b>
        </div>
        <div className="tg-barstat">
          <span>Risk</span>
          <b className="risk">{plan ? riskMoney : "—"}</b>
        </div>
        <div className="tg-barstat">
          <span>Reward : risk</span>
          <b>{plan ? `${plan.reward_risk.toFixed(2)}:1` : "—"}</b>
        </div>
        <div className="tg-barstat">
          <span>Exit ladder</span>
          <b>{rungs.armed} of {rungs.total} rungs</b>
        </div>
        {rungs.note && (
          <div className="tg-barstat tg-barwarn">
            <span>Heads up</span>
            <b title={rungs.note}>{rungs.note}</b>
          </div>
        )}
        <div className="tg-barright">
          <span className="tg-barhint">Enter review · Enter send · Esc cancel</span>
          <button
            type="button" className="tg-go" disabled={!canSend || opening}
            onClick={() => setConfirming(true)}
          >
            {plan ? `SAVE ${plan.shares.toLocaleString()} ${plan.ticker}` : "SAVE PLAN"}
          </button>
        </div>
      </div>
      {openError && <p className="tg-err">{openError}</p>}
    </div>
  );
}

/* The currency control. A segment for the choice and the rate beside it, because a
 * converted figure without its rate is a number you cannot check. Hidden entirely
 * while USD is selected -- there is nothing to convert, so the rate would be noise. */
function CurrencySwitch({
  currency, onCurrency, rate, onRate,
}: {
  currency: "THB" | "USD";
  onCurrency: (next: "THB" | "USD") => void;
  rate: number;
  onRate: (next: number) => void;
}) {
  const [draft, setDraft] = useState("");
  return (
    <span className="tg-fx">
      <span className="tg-seg">
        {(["THB", "USD"] as const).map((code) => (
          <button
            key={code} type="button" aria-pressed={currency === code}
            className={currency === code ? "on" : ""}
            onClick={() => onCurrency(code)}
          >
            {code}
          </button>
        ))}
      </span>
      {currency === "THB" && (
        <label className="tg-fxrate">
          <span>USD/THB</span>
          <input
            value={draft || rate.toFixed(2)} inputMode="decimal"
            onChange={(event) => setDraft(sanitizeDecimal(event.target.value))}
            onBlur={() => { const next = Number(draft); if (next > 0) onRate(next); setDraft(""); }}
          />
        </label>
      )}
    </span>
  );
}

function ModeBadge() {
  const [mode, setMode] = useState("");
  useEffect(() => {
    fetch(`${API}/brackets?limit=1`)
      .then((response) => (response.ok ? response.json() : null))
      .then((answer) => setMode(answer?.mode ?? ""))
      .catch(() => setMode(""));
  }, []);
  if (!mode) return null;
  return (
    <span className={`tm-mode ${mode === "live" ? "live" : ""}`}>
      {mode === "live" ? "LIVE" : mode.toUpperCase()}
    </span>
  );
}

/* Plans in play. Only what the engine is still watching -- the full, filterable
 * history belongs to the Orders app, which is why there is no pagination here. */
function PlansInPlay({ reload }: { reload: number }) {
  const [rows, setRows] = useState<BracketRecord[]>([]);
  const [error, setError] = useState("");
  const [tab, setTab] = useState<"live" | "closed">("live");
  /* Which row is open for work. Arming, moving a level and closing all live here --
   * the design shows this plane as a list, and the list is the only place those
   * actions can be reached from, so a row has to be able to open. */

  useEffect(() => {
    fetch(`${API}/brackets?limit=50`)
      .then(async (response) => {
        const answer = await response.json();
        if (!response.ok) throw new Error(answer?.error ?? `HTTP ${response.status}`);
        setRows(answer?.brackets ?? []);
        setError("");
      })
      .catch((cause) => setError(cause instanceof Error ? cause.message : "load failed"));
  }, [reload]);

  const live = rows.filter((row) => row.state === "PENDING" || row.state === "ACTIVE");
  const closed = rows.filter((row) => row.state !== "PENDING" && row.state !== "ACTIVE");
  const shown = tab === "live" ? live : closed.slice(0, 5);

  return (
    <section className="tg-plane tg-plansplane">
      <div className="tg-planehead">
        <div className="tg-planstitle">
          <h2>Plans in play</h2>
          <div className="tg-seg">
            <button type="button" className={tab === "live" ? "on" : ""}
              onClick={() => setTab("live")}>In play {live.length}</button>
            <button type="button" className={tab === "closed" ? "on" : ""}
              onClick={() => setTab("closed")}>Closed</button>
          </div>
          <span className="tg-step">only plans the engine is still watching live</span>
        </div>
        <a className="tg-back" href="/positions">Open Orders app ›</a>
      </div>

      {error && <p className="tg-err">{error}</p>}
      <div className="tg-plansgrid">
        <span>Ticker</span>
        <span>Status</span>
        <span className="n">Sh</span>
        <span className="n">Entry</span>
        <span className="n">SL</span>
        <span className="n">TP</span>
        <span className="n">High</span>
        <span className="n">Log</span>
      </div>
      <div className="tg-plansbody">
        {shown.map((row) => {
          const trailing = (row.high_water ?? 0) > (row.entry_price ?? 0);
          const open = row.state === "PENDING" || row.state === "ACTIVE";
          /* A plan that has not filled is not armed, and must not be dressed as one.
           *
           * Both of these read the wrong field. Status called PENDING and ACTIVE the
           * same thing, so a plan with nothing at the broker said "Armed" beside a
           * banner saying nothing had reached the broker. Entry showed entry_price,
           * which only exists once a fill has been recorded, so a saved plan showed
           * $0.00 and the price the operator actually typed was nowhere on screen. */
          const pending = row.state === "PENDING";
          const entryShown = pending
            ? row.requested_entry ?? 0
            : row.entry_price ?? 0;
          return (
            <a className="tg-plansrow" key={row.id} href={`/bracket/${row.id}`}>
              <span className="t">{row.ticker}</span>
              <span
                className={`st ${
                  open ? (pending ? "planned" : trailing ? "trailing" : "armed") : "done"
                }`}
              >
                {open
                  ? pending ? "Planned" : trailing ? "Trailing" : "Armed"
                  : row.state.charAt(0) + row.state.slice(1).toLowerCase()}
              </span>
              <span className="n">{row.quantity.toLocaleString()}</span>
              <span className={`n px${pending ? " planned" : ""}`}>
                ${money(entryShown)}
                {pending && <i className="tg-plannedmark">planned</i>}
              </span>
              <span className="n sl">${money(row.stop_price ?? 0)}</span>
              <span className="n tp">${money(row.target_price ?? 0)}</span>
              <span className="n">${money(row.high_water ?? 0)}</span>
              <span className="n log">Open</span>
            </a>
          );
        })}
        {shown.length === 0 && !error && (
          <p className="tg-empty">Nothing here yet.</p>
        )}
      </div>
      <div className="tg-plansfoot">
        <span>{live.length} live now · {rows.length} plans in total history</span>
        <a href="/positions">See the full history ›</a>
      </div>
    </section>
  );
}

/* The asymmetry of a drawdown is the lesson that cost the most to learn: losing
 * 60% needs 150% to undo, not 60%. Stating the recovery figure beside the risk is
 * the only way that arithmetic gets seen before the money is committed. */
function AccountRiskWarning({ share }: { share?: number }) {
  if (!share || share <= 0.02) return null;
  return (
    <p className="tm-warn">
      This position risks {pct(share)} of the account — losing all of it
      needs {pct(share / (1 - share))} to get back to even.
    </p>
  );
}
