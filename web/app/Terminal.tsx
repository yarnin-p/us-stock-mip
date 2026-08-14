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

/* Three shapes of the same ladder, from the handoff. They are a starting point, not a
 * recommendation: what separates them is how much of an open gain the trail is allowed
 * to give back before the floors take over.
 *
 * Every one of them satisfies the ordering the engine enforces -- floors below their
 * own activation, break-even before profit lock, profit lock before the trail, partial
 * above the trail -- so picking one can never produce the 400 that ladderError exists
 * to explain. That was checked by hand against all five rules when these were written;
 * there is no test framework in web/ yet to hold it, so editing a number here means
 * re-checking it against ladderError below. */
const LADDER_PRESETS = {
  conservative: {
    label: "conservative",
    note: "banks capital early, gives the runner little room",
    breakEven: { after: "2", floor: "1" },
    profitLock: { after: "4", floor: "2.5" },
    trail: { stopAfter: "8", targetAfter: "16", stopDistance: "7", targetDistance: "12" },
    partial: { after: "20", fraction: "40", minShares: "10" },
  },
  balanced: {
    label: "balanced",
    note: "the middle setting",
    breakEven: { after: "3", floor: "1.5" },
    profitLock: { after: "6", floor: "3" },
    trail: { stopAfter: "10", targetAfter: "20", stopDistance: "10", targetDistance: "15" },
    partial: { after: "30", fraction: "25", minShares: "10" },
  },
  runner: {
    label: "runner",
    note: "accepts a deeper give-back to hold a long runner",
    breakEven: { after: "4", floor: "1" },
    profitLock: { after: "10", floor: "4" },
    trail: { stopAfter: "14", targetAfter: "28", stopDistance: "16", targetDistance: "22" },
    partial: { after: "45", fraction: "20", minShares: "10" },
  },
} as const;

type PresetName = keyof typeof LADDER_PRESETS;

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
  const [advanced, setAdvanced] = useState(false);
  const [trailStopAfter, setTrailStopAfter] = useState("10");
  const [trailStopDistance, setTrailStopDistance] = useState("10");
  const [trailTargetAfter, setTrailTargetAfter] = useState("20");
  const [trailTargetDistance, setTrailTargetDistance] = useState("15");
  // The rungs under the trail. Each is opt-in, because a floor that is on by
  // default is a rule you did not choose being applied to your money.
  const [breakEvenOn, setBreakEvenOn] = useState(true);
  const [breakEvenAfter, setBreakEvenAfter] = useState("3");
  const [breakEvenFloor, setBreakEvenFloor] = useState("1.5");
  const [profitLockOn, setProfitLockOn] = useState(true);
  const [profitLockAfter, setProfitLockAfter] = useState("6");
  const [profitLockFloor, setProfitLockFloor] = useState("3");
  // Empty means "work it out from the entry price", which is right for Dime and right
  // for almost every edit. It is an override rather than a fixed default because
  // another broker, or a promotion, is a number this screen cannot know -- but a blank
  // field that quietly uses 0.35% at $0.40 is how a break-even rung ends up losing
  // 4.7% of the position.
  const [feeOverride, setFeeOverride] = useState("");
  /* The rate the baht figure is converted at. Typed rather than fetched: the preview
   * does not carry one, and a hard-coded 33.6 dressed up as live data would be a lie
   * told in the largest type on the screen. */
  const [usdThb, setUsdThb] = useState("33.60");
  // The design opens on the balanced preset with every rung armed.
  const [partialOn, setPartialOn] = useState(true);
  const [partialAfter, setPartialAfter] = useState("30");
  const [partialFraction, setPartialFraction] = useState("25");
  const [partialMinShares, setPartialMinShares] = useState("10");

  const [plan, setPlan] = useState<EntryPlan | null>(null);
  const [planError, setPlanError] = useState("");
  const [opening, setOpening] = useState(false);
  const [openError, setOpenError] = useState("");
  const [reload, setReload] = useState(0);

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

  const applyPreset = useCallback((name: PresetName) => {
    const shape = LADDER_PRESETS[name];
    setBreakEvenOn(true);
    setBreakEvenAfter(shape.breakEven.after);
    setBreakEvenFloor(shape.breakEven.floor);
    setProfitLockOn(true);
    setProfitLockAfter(shape.profitLock.after);
    setProfitLockFloor(shape.profitLock.floor);
    setTrailStopAfter(shape.trail.stopAfter);
    setTrailTargetAfter(shape.trail.targetAfter);
    setTrailStopDistance(shape.trail.stopDistance);
    setTrailTargetDistance(shape.trail.targetDistance);
    setPartialOn(true);
    setPartialAfter(shape.partial.after);
    setPartialFraction(shape.partial.fraction);
    setPartialMinShares(shape.partial.minShares);
  }, []);

  /* Which pill is lit is read back from the fields rather than remembered from the
   * click. Remembering it means a highlighted "balanced" can sit over numbers that
   * were edited afterwards -- a label making a claim about the ladder that the ladder
   * no longer supports. Derived, it cannot drift: change one number and no pill is
   * lit, change it back and the pill returns. */
  const activePreset = useMemo(() => {
    const current = {
      breakEven: { after: breakEvenAfter, floor: breakEvenFloor },
      profitLock: { after: profitLockAfter, floor: profitLockFloor },
      trail: {
        stopAfter: trailStopAfter, targetAfter: trailTargetAfter,
        stopDistance: trailStopDistance, targetDistance: trailTargetDistance,
      },
      partial: {
        after: partialAfter, fraction: partialFraction, minShares: partialMinShares,
      },
    };
    if (!breakEvenOn || !profitLockOn || !partialOn) return null;
    const same = (a: string, b: string) => Number(a) === Number(b);
    for (const [name, shape] of Object.entries(LADDER_PRESETS)) {
      if (
        same(current.breakEven.after, shape.breakEven.after) &&
        same(current.breakEven.floor, shape.breakEven.floor) &&
        same(current.profitLock.after, shape.profitLock.after) &&
        same(current.profitLock.floor, shape.profitLock.floor) &&
        same(current.trail.stopAfter, shape.trail.stopAfter) &&
        same(current.trail.targetAfter, shape.trail.targetAfter) &&
        same(current.trail.stopDistance, shape.trail.stopDistance) &&
        same(current.trail.targetDistance, shape.trail.targetDistance) &&
        same(current.partial.after, shape.partial.after) &&
        same(current.partial.fraction, shape.partial.fraction) &&
        same(current.partial.minShares, shape.partial.minShares)
      ) {
        return name as PresetName;
      }
    }
    return null;
  }, [
    breakEvenOn, breakEvenAfter, breakEvenFloor,
    profitLockOn, profitLockAfter, profitLockFloor,
    trailStopAfter, trailTargetAfter, trailStopDistance, trailTargetDistance,
    partialOn, partialAfter, partialFraction, partialMinShares,
  ]);

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
      ...(advanced
        ? {
            trail_stop_after: Number(trailStopAfter) / 100,
            trail_stop_distance: Number(trailStopDistance) / 100,
            trail_target_after: Number(trailTargetAfter) / 100,
            trail_target_distance: Number(trailTargetDistance) / 100,
            fee_round_trip_percent: fee.fraction,
            // Omitted rather than zeroed when off: the server refuses a floor with
            // no activation, and sending halves of a disabled rung would trip that.
            ...(breakEvenOn
              ? {
                  break_even_after: Number(breakEvenAfter) / 100,
                  break_even_floor: Number(breakEvenFloor) / 100,
                }
              : {}),
            ...(profitLockOn
              ? {
                  profit_lock_after: Number(profitLockAfter) / 100,
                  profit_lock_floor: Number(profitLockFloor) / 100,
                }
              : {}),
            ...(partialOn
              ? {
                  partial_tp_after: Number(partialAfter) / 100,
                  partial_tp_fraction: Number(partialFraction) / 100,
                  partial_tp_min_shares: Number(partialMinShares),
                }
              : {}),
          }
        : {}),
    };
  }, [
    ticker, entry, basis, amount, equity, exits, advanced,
    trailStopAfter, trailStopDistance, trailTargetAfter, trailTargetDistance,
    breakEvenOn, breakEvenAfter, breakEvenFloor,
    profitLockOn, profitLockAfter, profitLockFloor, fee,
    partialOn, partialAfter, partialFraction, partialMinShares,
  ]);

  /* The order of the rungs is the whole design, and getting it wrong is a 400 from
   * the server with no clue attached. Checking it here turns that into a sentence
   * that says which two numbers are in the wrong order. The server is still the
   * authority -- this only saves a round trip to be told so. */
  const ladderError = useMemo(() => {
    if (!advanced) return "";
    const trail = Number(trailStopAfter);
    const be = Number(breakEvenAfter);
    const lock = Number(profitLockAfter);
    if (breakEvenOn && !(Number(breakEvenFloor) < be)) {
      return "Break-even: the floor must sit below the gain that arms it, or it is a target rather than a floor.";
    }
    if (profitLockOn && !(Number(profitLockFloor) < lock)) {
      return "Profit lock: the floor must sit below the gain that arms it.";
    }
    if (breakEvenOn && profitLockOn && !(be < lock)) {
      return "Break-even must arm before profit lock — the ladder only climbs.";
    }
    if (profitLockOn && !(lock < trail)) {
      return "Profit lock must arm before the trail.";
    }
    if (partialOn && !(Number(partialAfter) > trail)) {
      return "Partial take-profit must arm above the trail — selling before the trail engages cuts into the runner the trail exists to hold.";
    }
    return "";
  }, [
    advanced, trailStopAfter, breakEvenOn, breakEvenAfter, breakEvenFloor,
    profitLockOn, profitLockAfter, profitLockFloor, partialOn, partialAfter,
  ]);

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
  const riskThb = riskUsd * (Number(usdThb) || 0);
  const budgetUse = plan?.risk_percent_of_account
    ? Math.min(1, plan.risk_percent_of_account / 0.01)
    : 0;
  const armed = [breakEvenOn, profitLockOn, true, partialOn].filter(Boolean).length;
  const canSend = Boolean(request) && ladderError === "" && Boolean(plan);

  const field = (label: string, value: string, set: (next: string) => void) => (
    <label className="tg-rungfield">
      <span>{label}</span>
      <input value={value} inputMode="decimal"
        onChange={(event) => set(event.target.value)} />
    </label>
  );

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
        <ModeBadge />
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
            <label className="tg-card">
              <span>Ticker</span>
              <input
                className="tg-in tg-in-ticker" value={ticker} placeholder="RCEL"
                spellCheck={false} autoComplete="off"
                onChange={(event) => setTicker(event.target.value.toUpperCase())}
              />
            </label>
            <label className="tg-card">
              <span>Entry price</span>
              <span className="tg-inwrap">
                <i className="tg-prefix">$</i>
                <input
                  className="tg-in" value={entry} inputMode="decimal" placeholder="7.77"
                  onChange={(event) => setEntry(event.target.value)}
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
                onChange={(event) => setAmount(event.target.value)}
              />
              <span className="tg-shares">
                {plan ? `= ${plan.shares.toLocaleString()} shares` : ""}
              </span>
            </div>
            <div className="tg-quick">
              {[100, 200, 500, 1000].map((value) => (
                <button key={value} type="button" onClick={() => setAmount(String(value))}>
                  ${value.toLocaleString()}
                </button>
              ))}
            </div>
          </div>

          <div className="tg-exits">
            <div className="tg-exit sl">
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
                  onChange={(event) => setStopPct(event.target.value)}
                />
                <i>{exitUnit === "pct" ? "%" : "$"}</i>
              </div>
              <div className="tg-exitfoot">
                {exits
                  ? `$${exits.stopPrice.toFixed(2)}${
                      plan ? ` · −฿${Math.round(plan.risk * (Number(usdThb) || 0)).toLocaleString()}` : ""
                    }`
                  : "below entry"}
              </div>
            </div>
            <div className="tg-exit tp">
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
                  onChange={(event) => setTargetPct(event.target.value)}
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
                onChange={(event) => setEquity(event.target.value)} />
            </label>
            <label className="tg-pillfield narrow">
              <span>Fee %</span>
              {/* Shows the effective rate, which is the Dime one worked out from the
                  entry price unless something has been typed over it. */}
              <input
                value={feeOverride || (fee.fraction * 100).toFixed(2)}
                inputMode="decimal"
                onChange={(event) => setFeeOverride(event.target.value)}
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
                <div className="tg-riskbaht">
                  ฿{riskThb ? Math.round(riskThb).toLocaleString() : "0"}
                </div>
                <div className="tg-risksub">
                  ${money(riskUsd)} · USD/THB {(Number(usdThb) || 0).toFixed(2)}
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
            <div className="tg-stackrow tp">
              <b>TAKE PROFIT</b>
              <span className="tg-stackright">
                <span>{plan ? `+${((plan.target_price / plan.entry_price - 1) * 100).toFixed(1)}%` : ""}</span>
                <i>{plan ? `$${money(plan.target_price)}` : "—"}</i>
              </span>
            </div>
            <div className="tg-stackrow entry">
              <b>ENTRY</b>
              <span className="tg-stackright">
                <span>{plan ? `${plan.shares.toLocaleString()} sh · $${money(plan.cost)}` : ""}</span>
                <i>{plan ? `$${money(plan.entry_price)}` : "—"}</i>
              </span>
            </div>
            <div className="tg-stackrow sl">
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

      <section className="tg-plane tg-ladderplane">
        <div className="tg-planehead">
          <div>
            <h2>The exit ladder <span className="tg-step">step 2 of 2</span></h2>
            <p className="tg-laddersub">
              Every rung only raises the floor — none of them lowers it.
            </p>
          </div>
          <div className="tg-presetgroup">
            {(Object.keys(LADDER_PRESETS) as PresetName[]).map((name) => (
              <button
                key={name} type="button"
                className={`tg-presetpill${activePreset === name ? " on" : ""}`}
                aria-pressed={activePreset === name}
                onClick={() => applyPreset(name)}
              >
                {name}
              </button>
            ))}
          </div>
        </div>

        <div className="tg-rungs">
          <div className={`tg-rung${breakEvenOn ? "" : " off"}`}>
            <div className="tg-runghead">
              <button type="button" className="tg-rungno" aria-pressed={breakEvenOn}
                aria-label="toggle rung 1"
                onClick={() => setBreakEvenOn((value) => !value)}>1</button>
              <span className="tg-rungname">Break-even</span>
            </div>
            <p className="tg-rungwhat">Stops a winner from turning into a loss.</p>
            <div className="tg-rungfields">
              {field("Arm at gain %", breakEvenAfter, setBreakEvenAfter)}
              {field("Lift SL to gain %", breakEvenFloor, setBreakEvenFloor)}
            </div>
            <p className="tg-rungnote">
              If it stalls here it exits at this floor — a small gain, never red.
            </p>
          </div>

          <div className={`tg-rung${profitLockOn ? "" : " off"}`}>
            <div className="tg-runghead">
              <button type="button" className="tg-rungno" aria-pressed={profitLockOn}
                aria-label="toggle rung 2"
                onClick={() => setProfitLockOn((value) => !value)}>2</button>
              <span className="tg-rungname">Profit lock</span>
            </div>
            <p className="tg-rungwhat">Banks a real slice instead of giving it all back.</p>
            <div className="tg-rungfields">
              {field("Arm at gain %", profitLockAfter, setProfitLockAfter)}
              {field("Lift SL to gain %", profitLockFloor, setProfitLockFloor)}
            </div>
            <p className="tg-rungnote">
              This floor sits above rung one; however far price retraces, it holds.
            </p>
          </div>

          <div className="tg-rung">
            <div className="tg-runghead">
              <span className="tg-rungno static">3</span>
              <span className="tg-rungname">Trail</span>
            </div>
            <p className="tg-rungwhat">Lets a runner run, following at a fixed distance.</p>
            <div className="tg-rungfields">
              {field("SL trails from gain %", trailStopAfter, setTrailStopAfter)}
              {field("TP widens from gain %", trailTargetAfter, setTrailTargetAfter)}
              {field("SL below high %", trailStopDistance, setTrailStopDistance)}
              {field("TP above high %", trailTargetDistance, setTrailTargetDistance)}
            </div>
            <p className="tg-rungnote">
              Rungs one and two measure from entry; the trail measures from the high.
            </p>
          </div>

          <div className={`tg-rung${partialOn ? "" : " off"}`}>
            <div className="tg-runghead">
              <button type="button" className="tg-rungno" aria-pressed={partialOn}
                aria-label="toggle rung 4"
                onClick={() => setPartialOn((value) => !value)}>4</button>
              <span className="tg-rungname">Partial take-profit</span>
            </div>
            <p className="tg-rungwhat">Takes cash off the table without closing the runner.</p>
            <div className="tg-rungfields">
              {field("Arm at gain %", partialAfter, setPartialAfter)}
              {field("Sell % of position", partialFraction, setPartialFraction)}
              {field("Skip under N shares", partialMinShares, setPartialMinShares)}
            </div>
            <p className="tg-rungnote">
              Sent as a limit at the arm price, never market — it will not walk down its
              own book.
            </p>
          </div>
        </div>
        {ladderError && <p className="tg-err">{ladderError}</p>}
      </section>

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
              SL ${money(plan.stop_price)} · TP ${money(plan.target_price)} · risk ฿
              {Math.round(riskThb).toLocaleString()} (${money(plan.risk)}) · {armed} of 4
              rungs armed
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
          <b className="risk">{plan ? `$${money(plan.risk)}` : "—"}</b>
        </div>
        <div className="tg-barstat">
          <span>Reward : risk</span>
          <b>{plan ? `${plan.reward_risk.toFixed(2)}:1` : "—"}</b>
        </div>
        <div className="tg-barstat">
          <span>Exit ladder</span>
          <b>{armed} of 4 rungs</b>
        </div>
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

function Figure({
  label, value, note, tone,
}: { label: string; value: string; note?: string; tone?: string }) {
  return (
    <div className={`tm-figure ${tone ? `tone-${tone}` : ""}`}>
      <span className="tm-figure-label">{label}</span>
      <span className="tm-figure-value">{value}</span>
      {note && <span className="tm-figure-note">{note}</span>}
    </div>
  );
}

/* Plans in play. Only what the engine is still watching -- the full, filterable
 * history belongs to the Orders app, which is why there is no pagination here. */
function PlansInPlay({ reload }: { reload: number }) {
  const [rows, setRows] = useState<BracketRecord[]>([]);
  const [error, setError] = useState("");
  const [tab, setTab] = useState<"live" | "closed">("live");

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
        <a className="tg-back" href="/orders">Open Orders app ›</a>
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
          return (
            <a className="tg-plansrow" key={row.id} href={`/orders#${row.id}`}>
              <span className="t">{row.ticker}</span>
              <span
                className={`st ${open ? (trailing ? "trailing" : "armed") : "done"}`}
              >
                {open
                  ? trailing ? "Trailing" : "Armed"
                  : row.state.charAt(0) + row.state.slice(1).toLowerCase()}
              </span>
              <span className="n">{row.quantity.toLocaleString()}</span>
              <span className="n px">${money(row.entry_price ?? 0)}</span>
              <span className="n sl">${money(row.stop_price ?? 0)}</span>
              <span className="n tp">${money(row.target_price ?? 0)}</span>
              <span className="n">${money(row.high_water ?? 0)}</span>
              <span className="n log">History</span>
            </a>
          );
        })}
        {shown.length === 0 && !error && (
          <p className="tg-empty">Nothing here yet.</p>
        )}
      </div>
      <div className="tg-plansfoot">
        <span>{live.length} live now · {rows.length} plans in total history</span>
        <a href="/orders">See the full history ›</a>
      </div>
    </section>
  );
}

/* Everything else on this screen answers how much is at risk. This answers whether the
 * position can be got out of, which is a different question and the one that has gone
 * unasked. Two positions on 2026-08-13 made the case: one name showed five shares on
 * the best bid, and the size that looked reasonable against the money was three hundred
 * times what the market would take.
 *
 * The cost line is deliberately labelled as incomplete. The preview carries the bid but
 * not the ask, so the spread -- which on an illiquid name is the larger half of the
 * bill -- is not in this number yet. A total that silently omits its biggest term is
 * worse than no total, so it says what it is missing. */
function ExitLiquidity({
  plan, feeFraction,
}: {
  plan: EntryPlan;
  feeFraction: number;
}) {
  const bidShares = plan.bid_shares ?? 0;
  const bidValue = plan.bid_value ?? 0;
  const requested = plan.requested_shares ?? plan.shares;
  const capped = requested > plan.shares;
  const unknown = bidShares <= 0;
  // How many times over the best bid the position is. One means the whole thing could
  // leave into the bid showing; ten means nine tenths of it is looking for a buyer that
  // has not appeared yet.
  const cover = bidShares > 0 ? plan.shares / bidShares : 0;
  const thin = !unknown && cover > 3;

  return (
    <div className={`tm-liquidity${unknown || thin ? " warn" : ""}`}>
      <span className="tm-liquidity-head">CAN YOU GET OUT</span>
      <div className="tm-liquidity-grid">
        <Figure
          label="best bid holds"
          value={unknown ? "unknown" : `${bidShares.toLocaleString()} sh`}
          note={unknown ? "no bid observed" : `$${money(bidValue)}`}
        />
        <Figure
          label="position vs bid"
          value={unknown ? "—" : `${cover.toFixed(1)}×`}
          note={unknown ? "cannot be judged" : cover <= 1 ? "leaves on the bid showing" : "must eat below the first bid"}
          tone={thin ? "risk" : undefined}
        />
        <Figure
          label="round-trip fee"
          value={pct(feeFraction, 2)}
          note="spread not included"
        />
      </div>
      {capped && (
        <p className="tm-liquidity-note">
          The money asked for <strong>{requested.toLocaleString()}</strong> shares; the book takes{" "}
          <strong>{plan.shares.toLocaleString()}</strong> — cut to the depth actually there.
        </p>
      )}
      {unknown && (
        <p className="tm-liquidity-note">
          No bid observed for this name — the size has not been checked against the book.
          Read the depth on the broker screen before sending.
        </p>
      )}
    </div>
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

function Rung({ label, price, tone }: { label: string; price: number; tone: string }) {
  return (
    <div className={`tm-rung tone-${tone}`}>
      <span className="tm-rung-label">{label}</span>
      <span className="tm-rung-price">${money(price)}</span>
    </div>
  );
}

/* A rung with its own on/off. The switch is part of the rung rather than a list of
 * checkboxes at the top, so turning one off cannot leave its numbers looking live. */
function LadderRung({
  on, onToggle, name, what, tone, children,
}: {
  on: boolean;
  onToggle: (next: boolean) => void;
  name: string;
  what: string;
  tone: "flat" | "reward";
  children: React.ReactNode;
}) {
  return (
    <div className={`tm-rung t-${tone} ${on ? "on" : "off"}`}>
      <div className="tm-rung-head">
        <label className="tm-switch">
          <input
            type="checkbox" checked={on}
            onChange={(event) => onToggle(event.target.checked)}
          />
          <strong>{name}</strong>
        </label>
        <span className="tm-rung-what">{what}</span>
      </div>
      {on && children}
    </div>
  );
}

function BracketList({ reload }: { reload: number }) {
  const [rows, setRows] = useState<BracketRecord[]>([]);
  const [error, setError] = useState("");
  const [openId, setOpenId] = useState<number | null>(null);
  const [manageId, setManageId] = useState<number | null>(null);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    fetch(`${API}/brackets?limit=50`)
      .then(async (response) => {
        const answer = await response.json();
        if (!response.ok) throw new Error(answer?.error ?? `HTTP ${response.status}`);
        setRows(answer?.brackets ?? []);
        setError("");
      })
      .catch((cause) => setError(cause instanceof Error ? cause.message : "load failed"));
  }, [reload, tick]);

  return (
    <section className="tm-card">
      <h2 className="tm-card-title">Saved plans</h2>
      {error && <p className="tm-error">{error}</p>}
      {!error && rows.length === 0 && <p className="tm-empty">No plans yet.</p>}
      {rows.length > 0 && (
        <div className="tm-table-wrap">
          <table className="tm-table">
            <thead>
              <tr>
                <th>Ticker</th><th>Status</th><th className="num">Sh</th>
                <th className="num">Entry</th><th className="num">SL</th>
                <th className="num">TP</th><th className="num">High</th>
                <th>Flags</th><th /><th />
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.id}>
                  <td className="tm-cell-ticker">{row.ticker}</td>
                  <td>
                    <span className={`tm-state s-${row.state}`}>{row.state}</span>
                    {/* A held bracket must not read as a trailed one: nothing is
                      * moving its stop while this is on. */}
                    {row.manual_hold && <span className="tm-held">held</span>}
                  </td>
                  <td className="num">
                    {row.quantity.toLocaleString()}
                    {(row.partial_taken_quantity ?? 0) > 0 && (
                      <em className="tm-pending">
                        {" "}sold {row.partial_taken_quantity?.toLocaleString()}
                      </em>
                    )}
                  </td>
                  <td className="num">
                    ${money(row.entry_price ?? row.requested_entry)}
                    {!row.entry_price && <em className="tm-pending"> requested</em>}
                  </td>
                  <td className="num tone-risk">
                    {row.stop_price ? `$${money(row.stop_price)}` : "—"}
                    {row.state === "ACTIVE" && row.stop_price ? (
                      row.stop_fired ? (
                        <em className="tm-pending"> sold</em>
                      ) : row.stop_order_id ? (
                        <span className="tm-holder broker" title="the stop order rests at the broker — it survives MIP going down">
                          broker
                        </span>
                      ) : (
                        <span
                          className="tm-holder engine"
                          title="Webull will not hold a stop outside the regular session — the engine watches every print and fires the limit sell itself. If MIP goes down nothing is protecting this."
                        >
                          engine
                        </span>
                      )
                    ) : null}
                  </td>
                  <td className="num tone-reward">
                    {row.target_price ? `$${money(row.target_price)}` : "—"}
                  </td>
                  <td className="num">
                    {row.high_water ? `$${money(row.high_water)}` : "—"}
                  </td>
                  <td>
                    {row.risk_flags.length > 0
                      ? <span className="tm-flag-count">{row.risk_flags.length}</span>
                      : "—"}
                  </td>
                  <td>
                    <button
                      type="button" className="tm-link"
                      onClick={() => setOpenId(openId === row.id ? null : row.id)}
                    >
                      {openId === row.id ? "Close" : "History"}
                    </button>
                  </td>
                  <td>
                    {(row.state === "ACTIVE" || row.state === "PENDING") && (
                      <button
                        type="button" className="tm-link"
                        onClick={() => setManageId(manageId === row.id ? null : row.id)}
                      >
                        {manageId === row.id ? "Close" : "Manage"}
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {manageId !== null && (
        <Manage
          bracket={rows.find((row) => row.id === manageId)}
          onChanged={() => setTick((value) => value + 1)}
        />
      )}
      {openId !== null && <History id={openId} />}
    </section>
  );
}

/* In-flight control.
 *
 * Three different things live here and they are deliberately not merged. Moving a
 * level is one edit; changing the rules the engine follows is another; taking the
 * wheel entirely is a third. A single form that did all three would make it
 * impossible to say afterwards which one you meant.
 *
 * Nothing here sends an entry. Force exit closes the bracket's record so the engine
 * stops trailing it -- the sell itself still goes through the execution path, which
 * is the only thing that can see the kill switch. */
function Manage({
  bracket, onChanged,
}: {
  bracket?: BracketRecord;
  onChanged: () => void;
}) {
  const [stop, setStop] = useState("");
  const [target, setTarget] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [said, setSaid] = useState("");

  const id = bracket?.id;
  useEffect(() => {
    setStop(bracket?.stop_price ? String(bracket.stop_price) : "");
    setTarget(bracket?.target_price ? String(bracket.target_price) : "");
    setNote("");
    setError("");
    setSaid("");
  }, [id, bracket?.stop_price, bracket?.target_price]);

  const send = useCallback(
    async (body: Record<string, unknown>, what: string) => {
      if (!id) return;
      setBusy(true);
      setError("");
      setSaid("");
      try {
        const response = await fetch(`${API}/brackets/${id}`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        });
        const answer = await response.json();
        if (!response.ok) throw new Error(answer?.error ?? `HTTP ${response.status}`);
        setSaid(what);
        onChanged();
      } catch (cause) {
        setError(cause instanceof Error ? cause.message : "failed");
      } finally {
        setBusy(false);
      }
    },
    [id, onChanged],
  );

  const close = useCallback(
    async (state: string) => {
      if (!id) return;
      setBusy(true);
      setError("");
      try {
        const response = await fetch(`${API}/brackets/${id}/close`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ state, note: note.trim() }),
        });
        const answer = await response.json();
        if (!response.ok) throw new Error(answer?.error ?? `HTTP ${response.status}`);
        onChanged();
      } catch (cause) {
        setError(cause instanceof Error ? cause.message : "failed");
      } finally {
        setBusy(false);
      }
    },
    [id, note, onChanged],
  );

  if (!bracket) return null;
  const held = bracket.manual_hold === true;
  const pending = bracket.state === "PENDING";

  return (
    <div className="tm-manage">
      <h3 className="tm-manage-title">
        {bracket.ticker} #{bracket.id}
        {held && <span className="tm-held">you are driving</span>}
      </h3>

      {pending && <Entry bracket={bracket} onChanged={onChanged} />}

      {bracket.state === "ACTIVE" && !bracket.stop_order_id && !bracket.stop_fired && (
        <div className="tm-flag">
          <strong>this stop is held by the engine, not by the broker</strong>
          <p>
            Webull will not hold a stop outside the regular session — the engine watches every print
            and fires the limit sell itself when price touches it. At the open it hands the stop back to the broker.
            <strong> Until then, if MIP stops or the connection drops, nothing is protecting this position.</strong>
          </p>
        </div>
      )}

      <div className="tm-manage-block">
        <p className="tm-hint">
          Move a level directly · leave a field empty to not touch it ·
          the engine keeps driving unless hold is on
          {pending && " · this plan is still PENDING — the engine follows nothing until it is armed"}
        </p>
        <div className="tm-row">
          <label className="tm-field">
            <span>SL price</span>
            <input className="tm-input" value={stop} inputMode="decimal"
              onChange={(event) => setStop(event.target.value)} />
          </label>
          <label className="tm-field">
            <span>TP price</span>
            <input className="tm-input" value={target} inputMode="decimal"
              onChange={(event) => setTarget(event.target.value)} />
          </label>
        </div>
        <label className="tm-field">
          <span>Reason (goes in the log)</span>
          <input className="tm-input" value={note}
            onChange={(event) => setNote(event.target.value)}
            placeholder="e.g. read it as pumping back" />
        </label>
        <button
          type="button" className="tm-btn" disabled={busy}
          onClick={() =>
            send(
              {
                ...(Number(stop) > 0 ? { stop_price: Number(stop) } : {}),
                ...(Number(target) > 0 ? { target_price: Number(target) } : {}),
                note: note.trim(),
              },
              "levels moved",
            )
          }
        >
          Move the levels
        </button>
      </div>

      <div className="tm-manage-block">
        <p className="tm-hint">
          {held
            ? "The engine is recording what it wants to do and sending nothing — visible in the log."
            : "Press this and the engine stops sending immediately while still recording what it wanted. Hand it back at any time without losing that record."}
        </p>
        <button
          type="button" className={held ? "tm-btn" : "tm-btn warn"} disabled={busy}
          onClick={() =>
            send({ hold: !held, note: note.trim() }, held ? "handed back to the engine" : "you are driving")
          }
        >
          {held ? "Let the engine drive" : "I will drive (hold)"}
        </button>
      </div>

      <div className="tm-manage-block danger">
        <p className="tm-hint">
          Close this plan in MIP — the engine stops following it and releases the price subscription.
          <strong> Selling for real still happens at the broker</strong>, because a sell has to go through the execution path
          that can see the kill switch.
        </p>
        <div className="tm-row">
          <button type="button" className="tm-btn warn" disabled={busy}
            onClick={() => close("CANCELLED")}>
            Cancel plan (nothing sold)
          </button>
          <button type="button" className="tm-btn danger" disabled={busy}
            onClick={() => close("STOPPED")}>
            Close as stopped out
          </button>
          <button type="button" className="tm-btn" disabled={busy}
            onClick={() => close("TARGETED")}>
            Close as target hit
          </button>
        </div>
      </div>

      {said && <p className="tm-said">{said}</p>}
      {error && <p className="tm-error">{error}</p>}
    </div>
  );
}

/* Entry, then arm. These are the two steps that turn a saved plan into a position
 * the engine is actually protecting, and they are separate because the second one
 * needs a number that only exists after the first: the price that really filled.
 *
 * The send is not one button. Create and preview show what the broker says it will
 * cost and whether the risk gate allows it; approve and submit are the click that
 * spends the money. Collapsing those into one would remove the only moment at which
 * a bad size is still free to cancel.
 *
 * Arming is what was missing before: a bracket that is never armed stays PENDING,
 * and the engine skips anything that is not ACTIVE -- so the ladder above it would
 * have been configuration that never ran. */
function Entry({
  bracket, onChanged,
}: {
  bracket: BracketRecord;
  onChanged: () => void;
}) {
  const [order, setOrder] = useState<ExecutionOrder | null>(null);
  // Prefilled from the plan, because an empty field disabled the arm button with
  // nothing on screen saying why -- and the price asked for is the right starting
  // guess when the fill has not been reported yet. Correcting it is one edit; working
  // out why a button does nothing is not.
  const [fill, setFill] = useState(() => String(bracket.requested_entry));
  const [shares, setShares] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [said, setSaid] = useState("");

  const call = useCallback(async (path: string, body?: unknown) => {
    const response = await fetch(`${API}${path}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
    const answer = await response.json();
    if (!response.ok) throw new Error(answer?.error ?? `HTTP ${response.status}`);
    return answer;
  }, []);

  // Create and preview together: a created order with no costing is not something
  // anyone can decide on, so the two arrive as one step.
  const draft = useCallback(async () => {
    setBusy(true);
    setError("");
    setSaid("");
    try {
      const created = (await call("/execution/orders", {
        ticker: bracket.ticker,
        side: "BUY",
        order_type: "LIMIT",
        quantity: bracket.quantity,
        limit_price: bracket.requested_entry,
        time_in_force: "DAY",
        reason: `bracket ${bracket.id}`,
      })) as ExecutionOrder;
      let costed = created;
      try {
        costed = (await call(`/execution/orders/${created.id}/preview`)) as ExecutionOrder;
      } catch (cause) {
        // The order exists either way, and saying so matters: a failed costing that
        // looked like a failed create would have someone create a second one.
        setError(
          `The order was created (#${created.id}) but the costing failed: ` +
            (cause instanceof Error ? cause.message : "unknown"),
        );
      }
      setOrder(costed);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "could not create the order");
    } finally {
      setBusy(false);
    }
  }, [bracket, call]);

  const send = useCallback(async () => {
    if (!order) return;
    setBusy(true);
    setError("");
    try {
      await call(`/execution/orders/${order.id}/approve`);
      const sent = (await call(`/execution/orders/${order.id}/submit`)) as ExecutionOrder;
      setOrder(sent);
      setSaid("sent");
      // Prefill from what actually happened, not from what was asked for.
      if (sent.average_fill_price > 0) setFill(String(sent.average_fill_price));
      if (sent.filled_quantity > 0) setShares(String(sent.filled_quantity));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "send failed");
    } finally {
      setBusy(false);
    }
  }, [order, call]);

  const arm = useCallback(async () => {
    setBusy(true);
    setError("");
    try {
      await call(`/brackets/${bracket.id}/arm`, {
        fill_price: Number(fill),
        ...(Number(shares) > 0 ? { quantity: Number(shares) } : {}),
      });
      onChanged();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "arm failed");
    } finally {
      setBusy(false);
    }
  }, [bracket.id, fill, shares, call, onChanged]);

  const blocked = order?.risk?.allowed === false;

  return (
    <>
      <div className="tm-manage-block">
        <p className="tm-hint">
          Sends the buy through the execution path — past the risk gate and the kill switch.
          {bracket.quantity.toLocaleString()} sh @ ${money(bracket.requested_entry)}
        </p>
        {!order && (
          <button type="button" className="tm-btn" disabled={busy} onClick={draft}>
            Draft the order and cost it
          </button>
        )}
        {order && (
          <>
            <div className="tm-numbers">
              <Figure label="State" value={order.state} />
              <Figure label="Estimated cost" value={`$${money(order.estimated_cost)}`} />
              <Figure label="Fee" value={`$${money(order.estimated_fee)}`} />
              {order.filled_quantity > 0 && (
                <Figure
                  label="Filled"
                  value={`${order.filled_quantity.toLocaleString()} @ $${money(order.average_fill_price)}`}
                  tone="reward"
                />
              )}
            </div>
            {blocked && (
              <div className="tm-flag">
                <strong>the risk gate refused this</strong>
                <p>{order.risk?.reasons?.join(" · ") || "no reason given"}</p>
              </div>
            )}
            {order.filled_quantity <= 0 && (
              <button
                type="button" className="tm-btn danger" disabled={busy || blocked}
                onClick={send}
              >
                Confirm — approve and send for real
              </button>
            )}
          </>
        )}
      </div>

      <div className="tm-manage-block">
        <p className="tm-hint">
          <strong>Arm</strong> places the real SL and TP at the broker and lets the engine follow them ·
          enter the price you <strong>actually got</strong>, not the one you asked for, because every level is measured from it ·
          a fill you made by hand at the broker goes here too
        </p>
        <div className="tm-row">
          <label className="tm-field">
            <span>Fill price</span>
            <input className="tm-input" value={fill} inputMode="decimal"
              onChange={(event) => setFill(event.target.value)}
              placeholder={String(bracket.requested_entry)} />
          </label>
          <label className="tm-field">
            <span>Shares filled (empty = as planned)</span>
            <input className="tm-input" value={shares} inputMode="decimal"
              onChange={(event) => setShares(event.target.value)}
              placeholder={String(bracket.quantity)} />
          </label>
        </div>
        <button
          type="button" className="tm-btn" disabled={busy || !(Number(fill) > 0)}
          onClick={arm}
        >
          Arm — place SL/TP and hand it to the engine
        </button>
        {!(Number(fill) > 0) && (
          <p className="tm-error">Enter the fill price first — every level is measured from it.</p>
        )}
      </div>

      {said && <p className="tm-said">{said}</p>}
      {error && <p className="tm-error">{error}</p>}
    </>
  );
}

/* The adjustment trail. When a stop turns out to have been in the wrong place,
 * the only useful question is what was known when it moved -- so a refused
 * amendment is shown as loudly as a successful one. */
function History({ id }: { id: number }) {
  const [rows, setRows] = useState<Adjustment[]>([]);
  const [error, setError] = useState("");
  useEffect(() => {
    fetch(`${API}/brackets/${id}`)
      .then(async (response) => {
        const answer = await response.json();
        if (!response.ok) throw new Error(answer?.error ?? `HTTP ${response.status}`);
        setRows(answer?.adjustments ?? []);
        setError("");
      })
      .catch((cause) => setError(cause instanceof Error ? cause.message : "load failed"));
  }, [id]);

  if (error) return <p className="tm-error">{error}</p>;
  if (rows.length === 0) return <p className="tm-empty">Nothing has moved yet.</p>;
  return (
    <div className="tm-history">
      {rows.map((row) => (
        <div className={`tm-history-row ${row.applied ? "" : "failed"}`} key={row.id}>
          <span className="tm-history-when">
            {new Date(row.created_at).toLocaleString("en-GB", {
              day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit",
            })}
          </span>
          <span className={`tm-trigger t-${row.trigger}`}>{row.trigger}</span>
          <span className="tm-history-move">
            {row.previous_stop && row.new_stop && row.previous_stop !== row.new_stop && (
              <>SL ${money(row.previous_stop)} → ${money(row.new_stop)}</>
            )}
            {row.previous_target && row.new_target && row.previous_target !== row.new_target && (
              <> · TP ${money(row.previous_target)} → ${money(row.new_target)}</>
            )}
            {!row.previous_stop && row.new_stop && (
              <>set SL ${money(row.new_stop)} · TP ${money(row.new_target ?? 0)}</>
            )}
          </span>
          <span className="tm-history-price">@ ${money(row.last_price)}</span>
          {!row.applied && (
            <span className="tm-history-error">
              failed — {row.broker_error || "the broker refused it"}
            </span>
          )}
        </div>
      ))}
    </div>
  );
}
