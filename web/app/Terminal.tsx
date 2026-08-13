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

/* Structural warnings are written out in full rather than shown as badges. A
 * three-letter tag is easy to scroll past; a sentence explaining that a stop may
 * not fill is not. */
const FLAG_COPY: Record<string, { title: string; body: string }> = {
  MICRO_FLOAT: {
    title: "float เล็ก — stop อาจไม่ทำงาน",
    body: "หุ้น float ต่ำ halt บ่อย และระหว่าง halt คุณส่งคำสั่งไม่ได้ พอเปิดกลับมาราคาอาจกระโดดข้าม stop ไปแล้ว ขนาดไม้คือการควบคุมเดียวที่ยังทำงาน",
  },
  EXTREME_RVOL: {
    title: "volume พุ่งผิดปกติ",
    body: "ราคาถูกกำหนดโดยสมุดคำสั่งที่แทบว่าง เคลื่อนไหวได้รุนแรงทั้งสองทาง",
  },
  OVEREXTENDED: {
    title: "วิ่งมาไกลแล้ว",
    body: "จากที่วัด 1,930 ตัว-วัน ตัวที่วิ่งเกิน +100% ปิดบวกเพียง 29% และย่อลึกกว่าที่ขึ้นต่อ",
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
  // Dime charges 0.15% each way plus 7% VAT, so a round trip is about 0.32% before
  // the SEC and TAF cents on the sell. Both floors are stated net of this, which is
  // the difference between a break-even rung that breaks even and one that loses.
  const [feeRoundTrip, setFeeRoundTrip] = useState("0.35");
  const [partialOn, setPartialOn] = useState(false);
  const [partialAfter, setPartialAfter] = useState("30");
  const [partialFraction, setPartialFraction] = useState("25");
  const [partialMinShares, setPartialMinShares] = useState("10");

  const [plan, setPlan] = useState<EntryPlan | null>(null);
  const [planError, setPlanError] = useState("");
  const [opening, setOpening] = useState(false);
  const [openError, setOpenError] = useState("");
  const [reload, setReload] = useState(0);

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
            fee_round_trip_percent: Number(feeRoundTrip) / 100,
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
    profitLockOn, profitLockAfter, profitLockFloor, feeRoundTrip,
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
      return "break-even: floor ต้องต่ำกว่าจุดที่มัน arm ไม่งั้นมันคือ TP ไม่ใช่ floor";
    }
    if (profitLockOn && !(Number(profitLockFloor) < lock)) {
      return "profit lock: floor ต้องต่ำกว่าจุดที่มัน arm";
    }
    if (breakEvenOn && profitLockOn && !(be < lock)) {
      return "break-even ต้อง arm ก่อน profit lock — บันไดขึ้นทางเดียว";
    }
    if (profitLockOn && !(lock < trail)) {
      return "profit lock ต้อง arm ก่อน trail";
    }
    if (partialOn && !(Number(partialAfter) > trail)) {
      return "partial TP ต้อง arm สูงกว่า trail — ขายก่อน trail ทำงานคือหั่นตัววิ่งที่ trail มีไว้จับ";
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
      setTicker("");
      setEntry("");
      setAmount("");
    } catch (cause) {
      setOpenError(cause instanceof Error ? cause.message : "could not open");
    } finally {
      setOpening(false);
    }
  }, [request]);

  return (
    <div className="tm">
      <header className="tm-head">
        <div>
          <h1 className="tm-title">Order Terminal</h1>
          <p className="tm-sub">
            ตั้ง entry · SL · TP ในทีเดียว — trailing ทั้งกรอบบนและกรอบล่าง
          </p>
        </div>
        <ModeBadge />
      </header>

      <div className="tm-grid">
        <section className="tm-card tm-ticket">
          <h2 className="tm-card-title">ตั้งคำสั่ง</h2>

          <label className="tm-field">
            <span>Ticker</span>
            <input
              className="tm-input tm-input-ticker" value={ticker}
              onChange={(event) => setTicker(event.target.value.toUpperCase())}
              placeholder="RCEL" spellCheck={false} autoComplete="off"
            />
          </label>

          <label className="tm-field">
            <span>Entry price</span>
            <input
              className="tm-input" value={entry} inputMode="decimal"
              onChange={(event) => setEntry(event.target.value)} placeholder="7.77"
            />
          </label>

          <div className="tm-field">
            <span>ขนาดไม้</span>
            <div className="tm-toggle">
              <button
                type="button" className={basis === "budget" ? "on" : ""}
                onClick={() => setBasis("budget")}
              >
                ใส่เงิน
              </button>
              <button
                type="button" className={basis === "risk" ? "on" : ""}
                onClick={() => setBasis("risk")}
              >
                ใส่ความเสี่ยง
              </button>
            </div>
            <input
              className="tm-input" value={amount} inputMode="decimal"
              onChange={(event) => setAmount(event.target.value)}
              placeholder={basis === "budget" ? "2000" : "400"}
            />
            <small className="tm-hint">
              {basis === "budget"
                ? "จำนวนเงินที่จะลง — ระบบจะบอกว่าถ้า SL ทำงานจะเสียเท่าไหร่"
                : "จำนวนเงินที่ยอมเสียถ้า SL ทำงาน — ขนาดไม้จะคำนวณย้อนกลับให้"}
            </small>
          </div>

          <div className="tm-field">
            <span>SL / TP</span>
            <div className="tm-toggle">
              <button
                type="button" className={exitUnit === "pct" ? "on" : ""}
                onClick={() => switchUnit("pct")}
              >
                %
              </button>
              <button
                type="button" className={exitUnit === "price" ? "on" : ""}
                onClick={() => switchUnit("price")}
              >
                ราคา $
              </button>
            </div>
            <div className="tm-row">
              <label className="tm-subfield">
                <span>SL {exitUnit === "pct" ? "%" : "$"}</span>
                <input
                  className="tm-input" value={stopPct} inputMode="decimal"
                  onChange={(event) => setStopPct(event.target.value)}
                  placeholder={exitUnit === "pct" ? "3" : "7.76"}
                />
              </label>
              <label className="tm-subfield">
                <span>TP {exitUnit === "pct" ? "%" : "$"}</span>
                <input
                  className="tm-input" value={targetPct} inputMode="decimal"
                  onChange={(event) => setTargetPct(event.target.value)}
                  placeholder={exitUnit === "pct" ? "10" : "8.80"}
                />
              </label>
            </div>
            {/* The unit not being typed is echoed back, so the level and the rule
                are both visible without switching back and forth. */}
            {exits && (
              <small className="tm-hint tm-mirror">
                {exitUnit === "pct" ? (
                  <>
                    SL <strong>${exits.stopPrice.toFixed(2)}</strong>
                    {" · "}TP <strong>${exits.targetPrice.toFixed(2)}</strong>
                  </>
                ) : (
                  <>
                    SL <strong>−{(exits.stop * 100).toFixed(2)}%</strong>
                    {" · "}TP <strong>+{(exits.target * 100).toFixed(2)}%</strong>
                  </>
                )}
              </small>
            )}
            {!exits && Number(entry) > 0 && (
              <small className="tm-hint tm-error">
                {exitUnit === "price"
                  ? "SL ต้องต่ำกว่าราคาเข้า และ TP ต้องสูงกว่า"
                  : "SL และ TP ต้องมากกว่า 0"}
              </small>
            )}
          </div>

          <label className="tm-field">
            <span>เงินในพอร์ตทั้งหมด</span>
            <input
              className="tm-input" value={equity} inputMode="decimal"
              onChange={(event) => setEquity(event.target.value)}
            />
            <small className="tm-hint">
              ใช้คำนวณว่าไม้นี้เสี่ยงกี่ % ของพอร์ต — ตัวเลขที่ตัดสินว่าไม้เดียวจะทำพอร์ตพังไหม
            </small>
          </label>

          <button
            type="button" className="tm-disclose"
            onClick={() => setAdvanced((value) => !value)}
            aria-expanded={advanced}
          >
            {advanced ? "▾" : "▸"} แผนขาออก — บันไดทั้งชุด
          </button>

          {advanced && (
            <div className="tm-advanced">
              {/* The rungs are listed in the order the price meets them, because that
                * is the only order in which the rules make sense to read. */}
              <p className="tm-hint">
                ราคาไต่ขึ้นไปเจอทีละขั้น · แต่ละขั้น<strong>ยกพื้น</strong>ขึ้นเท่านั้น
                ไม่มีขั้นไหนถอยลง — ขั้นที่ยกสูงสุดคือขั้นที่ใช้จริง
              </p>

              <LadderRung
                on={breakEvenOn} onToggle={setBreakEvenOn}
                name="1 · break-even" tone="flat"
                what="ไม่ให้ไม้ที่กำไรแล้วกลับมาขาดทุน"
              >
                <div className="tm-row">
                  <label className="tm-field">
                    <span>arm เมื่อกำไร %</span>
                    <input className="tm-input" value={breakEvenAfter} inputMode="decimal"
                      onChange={(event) => setBreakEvenAfter(event.target.value)} />
                  </label>
                  <label className="tm-field">
                    <span>ยก SL ไปที่กำไร %</span>
                    <input className="tm-input" value={breakEvenFloor} inputMode="decimal"
                      onChange={(event) => setBreakEvenFloor(event.target.value)} />
                  </label>
                </div>
                <small className="tm-hint">
                  ถ้าแตะแล้วไม่ไปต่อ มันจะออกที่พื้นนี้ — กำไรน้อยแต่ไม่ติดลบ
                  นี่คือสิ่งที่ควรจะเกิดขึ้น ไม่ใช่ความผิดพลาด
                </small>
              </LadderRung>

              <LadderRung
                on={profitLockOn} onToggle={setProfitLockOn}
                name="2 · profit lock" tone="reward"
                what="เก็บกำไรก้อนจริงไว้ ไม่คืนหมด"
              >
                <div className="tm-row">
                  <label className="tm-field">
                    <span>arm เมื่อกำไร %</span>
                    <input className="tm-input" value={profitLockAfter} inputMode="decimal"
                      onChange={(event) => setProfitLockAfter(event.target.value)} />
                  </label>
                  <label className="tm-field">
                    <span>ยก SL ไปที่กำไร %</span>
                    <input className="tm-input" value={profitLockFloor} inputMode="decimal"
                      onChange={(event) => setProfitLockFloor(event.target.value)} />
                  </label>
                </div>
              </LadderRung>

              <div className="tm-rung on">
                <div className="tm-rung-head">
                  <strong>3 · trail</strong>
                  <span className="tm-rung-what">ปล่อยให้ตัววิ่งวิ่ง แล้วตามด้วยระยะห่างคงที่</span>
                </div>
                <div className="tm-row">
                  <label className="tm-field">
                    <span>SL เริ่ม trail เมื่อกำไร %</span>
                    <input className="tm-input" value={trailStopAfter} inputMode="decimal"
                      onChange={(event) => setTrailStopAfter(event.target.value)} />
                  </label>
                  <label className="tm-field">
                    <span>SL ห่างจาก high %</span>
                    <input className="tm-input" value={trailStopDistance} inputMode="decimal"
                      onChange={(event) => setTrailStopDistance(event.target.value)} />
                  </label>
                </div>
                <div className="tm-row">
                  <label className="tm-field">
                    <span>TP เริ่มขยายเมื่อกำไร %</span>
                    <input className="tm-input" value={trailTargetAfter} inputMode="decimal"
                      onChange={(event) => setTrailTargetAfter(event.target.value)} />
                  </label>
                  <label className="tm-field">
                    <span>TP ห่างจาก high %</span>
                    <input className="tm-input" value={trailTargetDistance} inputMode="decimal"
                      onChange={(event) => setTrailTargetDistance(event.target.value)} />
                  </label>
                </div>
                <small className="tm-hint">
                  สองอันบนวัดจาก <strong>entry</strong> (สัญญาว่าผลลัพธ์สุทธิจะไม่แย่กว่านี้) ·
                  trail วัดจาก <strong>high</strong> (สัญญาว่าจะคืนกำไรไม่เกินนี้)
                </small>
              </div>

              <LadderRung
                on={partialOn} onToggle={setPartialOn}
                name="4 · partial TP" tone="reward"
                what="ขายบางส่วนตอนวิ่ง เก็บเงินสดโดยไม่ปิดตัววิ่ง"
              >
                <div className="tm-row">
                  <label className="tm-field">
                    <span>arm เมื่อกำไร %</span>
                    <input className="tm-input" value={partialAfter} inputMode="decimal"
                      onChange={(event) => setPartialAfter(event.target.value)} />
                  </label>
                  <label className="tm-field">
                    <span>ขายกี่ % ของไม้</span>
                    <input className="tm-input" value={partialFraction} inputMode="decimal"
                      onChange={(event) => setPartialFraction(event.target.value)} />
                  </label>
                  <label className="tm-field">
                    <span>ขายน้อยกว่ากี่หุ้นให้ข้าม</span>
                    <input className="tm-input" value={partialMinShares} inputMode="decimal"
                      onChange={(event) => setPartialMinShares(event.target.value)} />
                  </label>
                </div>
                <small className="tm-hint">
                  ส่งเป็น <strong>limit</strong> ที่ราคาที่ arm ไม่ใช่ market —
                  market order ขายหุ้นบางบางไปหนึ่งในสี่คือการเดินลง book ตัวเอง ·
                  ไม้จะยังนับเต็มจนกว่าโบรกจะยืนยันว่าขายได้จริง
                </small>
              </LadderRung>

              <label className="tm-field">
                <span>ค่าธรรมเนียมไป-กลับ %</span>
                <input className="tm-input" value={feeRoundTrip} inputMode="decimal"
                  onChange={(event) => setFeeRoundTrip(event.target.value)} />
                <small className="tm-hint">
                  Dime คิด 0.15% ต่อขา + VAT 7% ≈ 0.32% ไป-กลับ · พื้นทั้งสองขั้นบวกตัวนี้เข้าไป
                  ไม่งั้น &quot;break-even&quot; จะออกมาขาดทุนเท่าค่าคอม
                </small>
              </label>

              {ladderError && <p className="tm-error">{ladderError}</p>}
            </div>
          )}
        </section>

        <section className="tm-card tm-preview">
          <h2 className="tm-card-title">ก่อนกด — นี่คือสิ่งที่คุณกำลังเสี่ยง</h2>
          {!plan && !planError && (
            <p className="tm-empty">ใส่ ticker · ราคา · จำนวนเงิน แล้วตัวเลขจะขึ้นที่นี่</p>
          )}
          {planError && <p className="tm-error">{planError}</p>}
          {plan && (
            <>
              <div className="tm-numbers">
                <Figure label="จำนวนหุ้น" value={plan.shares.toLocaleString()} />
                <Figure label="ใช้เงิน" value={`$${money(plan.cost)}`} />
                <Figure
                  label="เสี่ยงจริง" value={`$${money(plan.risk)}`} tone="risk"
                  note={
                    plan.risk_percent_of_account
                      ? `${pct(plan.risk_percent_of_account)} ของพอร์ต`
                      : undefined
                  }
                />
                <Figure label="ได้ถ้าถึง TP" value={`$${money(plan.reward)}`} tone="reward" />
              </div>

              <div className="tm-ladder">
                <Rung label="TP" price={plan.target_price} tone="reward" />
                <Rung label="Entry" price={plan.entry_price} tone="flat" />
                <Rung label="SL" price={plan.stop_price} tone="risk" />
              </div>

              <div className="tm-ratio">
                <div>
                  <span className="tm-ratio-value">{plan.reward_risk.toFixed(2)}:1</span>
                  <span className="tm-ratio-label">reward : risk</span>
                </div>
                <div>
                  <span className="tm-ratio-value">{pct(plan.breakeven_win_rate)}</span>
                  <span className="tm-ratio-label">
                    win rate ที่ต้องได้เพื่อ<strong>เสมอทุน</strong>
                  </span>
                </div>
              </div>

              <AccountRiskWarning share={plan.risk_percent_of_account} />

              {plan.risk_flags.map((flag) => {
                const key = flag.split(":")[0];
                const copy = FLAG_COPY[key];
                return (
                  <div className="tm-flag" key={flag}>
                    <strong>{copy?.title ?? key}</strong>
                    <p>{copy?.body ?? flag}</p>
                  </div>
                );
              })}

              <button
                type="button" className="tm-submit" onClick={open}
                disabled={opening || !request || ladderError !== ""}
              >
                {opening ? "กำลังบันทึก…" : "บันทึกแผนไม้นี้"}
              </button>
              {openError && <p className="tm-error">{openError}</p>}
              <p className="tm-hint tm-hint-strong">
                ปุ่มนี้บันทึกแผนเท่านั้น — ไม่ส่งคำสั่งซื้อไปที่โบรกเกอร์
                การส่งคำสั่งจริงยังต้องผ่าน execution path ที่มี risk gate และ kill switch
              </p>
            </>
          )}
        </section>
      </div>

      <BracketList reload={reload} />
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

/* The asymmetry of a drawdown is the lesson that cost the most to learn: losing
 * 60% needs 150% to undo, not 60%. Stating the recovery figure beside the risk is
 * the only way that arithmetic gets seen before the money is committed. */
function AccountRiskWarning({ share }: { share?: number }) {
  if (!share || share <= 0.02) return null;
  return (
    <p className="tm-warn">
      ไม้นี้เสี่ยง {pct(share)} ของพอร์ต — ถ้าเสียเต็มจำนวน
      ต้องทำกำไร {pct(share / (1 - share))} เพื่อกลับมาเท่าเดิม
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
      <h2 className="tm-card-title">ไม้ที่บันทึกไว้</h2>
      {error && <p className="tm-error">{error}</p>}
      {!error && rows.length === 0 && <p className="tm-empty">ยังไม่มีไม้</p>}
      {rows.length > 0 && (
        <div className="tm-table-wrap">
          <table className="tm-table">
            <thead>
              <tr>
                <th>Ticker</th><th>สถานะ</th><th className="num">หุ้น</th>
                <th className="num">Entry</th><th className="num">SL</th>
                <th className="num">TP</th><th className="num">High</th>
                <th>ธง</th><th /><th />
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
                    {row.manual_hold && <span className="tm-held">มือ</span>}
                  </td>
                  <td className="num">
                    {row.quantity.toLocaleString()}
                    {(row.partial_taken_quantity ?? 0) > 0 && (
                      <em className="tm-pending">
                        {" "}ขายแล้ว {row.partial_taken_quantity?.toLocaleString()}
                      </em>
                    )}
                  </td>
                  <td className="num">
                    ${money(row.entry_price ?? row.requested_entry)}
                    {!row.entry_price && <em className="tm-pending"> ขอไว้</em>}
                  </td>
                  <td className="num tone-risk">
                    {row.stop_price ? `$${money(row.stop_price)}` : "—"}
                    {row.state === "ACTIVE" && row.stop_price ? (
                      row.stop_fired ? (
                        <em className="tm-pending"> ขายแล้ว</em>
                      ) : row.stop_order_id ? (
                        <span className="tm-holder broker" title="stop order วางอยู่ที่โบรก — รอดแม้ MIP ดับ">
                          โบรก
                        </span>
                      ) : (
                        <span
                          className="tm-holder engine"
                          title="Webull ไม่รับ stop order นอก regular session — engine เฝ้าราคาและจะยิง limit sell เอง ถ้า MIP ดับจะไม่มีอะไรคุ้ม"
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
                      {openId === row.id ? "ปิด" : "ประวัติ"}
                    </button>
                  </td>
                  <td>
                    {(row.state === "ACTIVE" || row.state === "PENDING") && (
                      <button
                        type="button" className="tm-link"
                        onClick={() => setManageId(manageId === row.id ? null : row.id)}
                      >
                        {manageId === row.id ? "ปิด" : "จัดการ"}
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
        setError(cause instanceof Error ? cause.message : "ไม่สำเร็จ");
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
        setError(cause instanceof Error ? cause.message : "ไม่สำเร็จ");
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
        {held && <span className="tm-held">คุณกำลังขับ</span>}
      </h3>

      {pending && <Entry bracket={bracket} onChanged={onChanged} />}

      {bracket.state === "ACTIVE" && !bracket.stop_order_id && !bracket.stop_fired && (
        <div className="tm-flag">
          <strong>stop นี้ engine ถืออยู่ ไม่ได้อยู่ที่โบรก</strong>
          <p>
            Webull ไม่รับ stop order นอกเวลา 21:30–04:00 น. — engine เฝ้าทุก print
            และจะยิง limit sell เองเมื่อราคาแตะ ตลาดเปิดแล้วมันจะส่ง stop ไปวางที่โบรกให้
            <strong> ระหว่างนี้ถ้า MIP ดับหรือเน็ตหลุด จะไม่มีอะไรคุ้มไม้นี้</strong>
          </p>
        </div>
      )}

      <div className="tm-manage-block">
        <p className="tm-hint">
          ย้ายเส้นตรงๆ · ปล่อยว่างไว้ = ไม่แตะเส้นนั้น ·
          engine ยังขับอยู่ถ้าไม่ได้กด hold
          {pending && " · ไม้นี้ยัง PENDING — engine ยังไม่ได้ตามอะไร จนกด arm"}
        </p>
        <div className="tm-row">
          <label className="tm-field">
            <span>SL ราคา</span>
            <input className="tm-input" value={stop} inputMode="decimal"
              onChange={(event) => setStop(event.target.value)} />
          </label>
          <label className="tm-field">
            <span>TP ราคา</span>
            <input className="tm-input" value={target} inputMode="decimal"
              onChange={(event) => setTarget(event.target.value)} />
          </label>
        </div>
        <label className="tm-field">
          <span>เหตุผล (ลงในประวัติ)</span>
          <input className="tm-input" value={note}
            onChange={(event) => setNote(event.target.value)}
            placeholder="เช่น อ่านว่ามันจะ pump กลับ" />
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
              "ย้ายเส้นแล้ว",
            )
          }
        >
          ย้ายเส้น
        </button>
      </div>

      <div className="tm-manage-block">
        <p className="tm-hint">
          {held
            ? "engine กำลังบันทึกว่ามันอยากทำอะไร แต่ไม่ส่งอะไรเลย — ดูได้ในประวัติ"
            : "กดแล้ว engine จะหยุดส่งคำสั่งทันที แต่ยังบันทึกว่ามันอยากทำอะไร ปล่อยกลับได้ทุกเมื่อโดยไม่เสียประวัติช่วงนั้น"}
        </p>
        <button
          type="button" className={held ? "tm-btn" : "tm-btn warn"} disabled={busy}
          onClick={() =>
            send({ hold: !held, note: note.trim() }, held ? "คืนพวงมาลัยแล้ว" : "คุณขับแล้ว")
          }
        >
          {held ? "ให้ engine ขับต่อ" : "ผมขับเอง (hold)"}
        </button>
      </div>

      <div className="tm-manage-block danger">
        <p className="tm-hint">
          ปิดไม้นี้ใน MIP — engine เลิกตาม และปล่อย subscription ราคาทิ้ง
          <strong> การขายจริงยังต้องกดที่โบรก</strong> เพราะคำสั่งขายต้องผ่าน execution path
          ที่เห็น kill switch
        </p>
        <div className="tm-row">
          <button type="button" className="tm-btn warn" disabled={busy}
            onClick={() => close("CANCELLED")}>
            ยกเลิกไม้ (ยังไม่ได้ขาย)
          </button>
          <button type="button" className="tm-btn danger" disabled={busy}
            onClick={() => close("STOPPED")}>
            ปิดว่าโดน SL
          </button>
          <button type="button" className="tm-btn" disabled={busy}
            onClick={() => close("TARGETED")}>
            ปิดว่าได้ TP
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
          `คำสั่งถูกสร้างแล้ว (#${created.id}) แต่ preview ไม่ผ่าน: ` +
            (cause instanceof Error ? cause.message : "unknown"),
        );
      }
      setOrder(costed);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "สร้างคำสั่งไม่ได้");
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
      setSaid("ส่งแล้ว");
      // Prefill from what actually happened, not from what was asked for.
      if (sent.average_fill_price > 0) setFill(String(sent.average_fill_price));
      if (sent.filled_quantity > 0) setShares(String(sent.filled_quantity));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "ส่งไม่สำเร็จ");
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
      setError(cause instanceof Error ? cause.message : "arm ไม่สำเร็จ");
    } finally {
      setBusy(false);
    }
  }, [bracket.id, fill, shares, call, onChanged]);

  const blocked = order?.risk?.allowed === false;

  return (
    <>
      <div className="tm-manage-block">
        <p className="tm-hint">
          ส่งคำสั่งซื้อผ่าน execution path — ผ่าน risk gate และ kill switch
          {bracket.quantity.toLocaleString()} หุ้น @ ${money(bracket.requested_entry)}
        </p>
        {!order && (
          <button type="button" className="tm-btn" disabled={busy} onClick={draft}>
            เตรียมคำสั่ง + คิดค่าใช้จ่าย
          </button>
        )}
        {order && (
          <>
            <div className="tm-numbers">
              <Figure label="สถานะ" value={order.state} />
              <Figure label="ต้นทุนประมาณ" value={`$${money(order.estimated_cost)}`} />
              <Figure label="ค่าธรรมเนียม" value={`$${money(order.estimated_fee)}`} />
              {order.filled_quantity > 0 && (
                <Figure
                  label="ได้จริง"
                  value={`${order.filled_quantity.toLocaleString()} @ $${money(order.average_fill_price)}`}
                  tone="reward"
                />
              )}
            </div>
            {blocked && (
              <div className="tm-flag">
                <strong>risk gate ไม่ให้ผ่าน</strong>
                <p>{order.risk?.reasons?.join(" · ") || "ไม่ระบุเหตุผล"}</p>
              </div>
            )}
            {order.filled_quantity <= 0 && (
              <button
                type="button" className="tm-btn danger" disabled={busy || blocked}
                onClick={send}
              >
                ยืนยัน — อนุมัติและส่งจริง
              </button>
            )}
          </>
        )}
      </div>

      <div className="tm-manage-block">
        <p className="tm-hint">
          <strong>arm</strong> = วาง SL/TP จริงที่โบรก แล้วเปิดให้ engine ตาม ·
          ใส่ราคาที่<strong>ได้จริง</strong> ไม่ใช่ราคาที่ขอ เพราะทุกเส้นคิดจากราคานี้ ·
          ซื้อมือที่โบรกเองก็กรอกตรงนี้ได้
        </p>
        <div className="tm-row">
          <label className="tm-field">
            <span>ราคาที่ได้จริง</span>
            <input className="tm-input" value={fill} inputMode="decimal"
              onChange={(event) => setFill(event.target.value)}
              placeholder={String(bracket.requested_entry)} />
          </label>
          <label className="tm-field">
            <span>จำนวนจริง (ว่าง = ตามแผน)</span>
            <input className="tm-input" value={shares} inputMode="decimal"
              onChange={(event) => setShares(event.target.value)}
              placeholder={String(bracket.quantity)} />
          </label>
        </div>
        <button
          type="button" className="tm-btn" disabled={busy || !(Number(fill) > 0)}
          onClick={arm}
        >
          arm — วาง SL/TP แล้วให้ engine ตาม
        </button>
        {!(Number(fill) > 0) && (
          <p className="tm-error">ใส่ราคาที่ได้จริงก่อน — ทุกเส้นคิดจากราคานี้</p>
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
  if (rows.length === 0) return <p className="tm-empty">ยังไม่มีการขยับ</p>;
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
              <>ตั้ง SL ${money(row.new_stop)} · TP ${money(row.new_target ?? 0)}</>
            )}
          </span>
          <span className="tm-history-price">@ ${money(row.last_price)}</span>
          {!row.applied && (
            <span className="tm-history-error">
              ไม่สำเร็จ — {row.broker_error || "โบรกเกอร์ปฏิเสธ"}
            </span>
          )}
        </div>
      ))}
    </div>
  );
}
