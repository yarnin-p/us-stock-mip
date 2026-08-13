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
  config: {
    StopLossPercent: number;
    TakeProfitPercent: number;
    TrailStopAfter: number;
    TrailStopDistance: number;
    TrailTargetAfter: number;
    TrailTargetDistance: number;
  };
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
          }
        : {}),
    };
  }, [
    ticker, entry, basis, amount, equity, exits, advanced,
    trailStopAfter, trailStopDistance, trailTargetAfter, trailTargetDistance,
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
            {advanced ? "▾" : "▸"} Trailing (กรอบบน + กรอบล่าง)
          </button>

          {advanced && (
            <div className="tm-advanced">
              <p className="tm-hint">
                SL จะขยับขึ้นตาม high เท่านั้น ไม่ถอยลง · TP จะขยายขึ้นเพื่อไม่ขายตัววิ่งเร็วเกินไป
              </p>
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
                disabled={opening || !request}
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

function BracketList({ reload }: { reload: number }) {
  const [rows, setRows] = useState<BracketRecord[]>([]);
  const [error, setError] = useState("");
  const [openId, setOpenId] = useState<number | null>(null);

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
                <th>ธง</th><th />
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.id}>
                  <td className="tm-cell-ticker">{row.ticker}</td>
                  <td><span className={`tm-state s-${row.state}`}>{row.state}</span></td>
                  <td className="num">{row.quantity.toLocaleString()}</td>
                  <td className="num">
                    ${money(row.entry_price ?? row.requested_entry)}
                    {!row.entry_price && <em className="tm-pending"> ขอไว้</em>}
                  </td>
                  <td className="num tone-risk">
                    {row.stop_price ? `$${money(row.stop_price)}` : "—"}
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
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {openId !== null && <History id={openId} />}
    </section>
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
