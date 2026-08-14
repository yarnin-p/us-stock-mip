"use client";

/* One live bracket, in full.
 *
 * The panel this replaces showed the levels as bare numbers with no sense that
 * anything had moved. The design's insight is that once the ladder has lifted the
 * stop above the fill, the stop stops being a loss and becomes a floor -- so the
 * screen leads with what the engine is holding and what that is worth, and only then
 * offers the controls.
 *
 * "Worst case now" is the number that changes how the position reads. Before rung one
 * it is negative and it is the risk; after rung two it is positive and it is money
 * already won. Same arithmetic, opposite feeling, and the old panel showed neither.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { sanitizeDecimal } from "./inputs";
import { useCurrency } from "./currency";

const API = process.env.NEXT_PUBLIC_API_BASE ?? "http://localhost:8080";

type Record_ = {
  id: number;
  ticker: string;
  state: string;
  quantity: number;
  requested_entry: number;
  entry_price?: number;
  stop_price?: number;
  target_price?: number;
  high_water?: number;
  partial_taken_quantity?: number;
  partial_fill_price?: number;
  manual_hold?: boolean;
  opened_at?: string;
  note?: string;
};

type Adjustment = {
  id: number;
  trigger: string;
  new_stop?: number;
  new_target?: number;
  previous_stop?: number;
  reason?: string;
  applied?: boolean;
  broker_error?: string;
  created_at: string;
};

/* The engine's trigger names, said the way the screen talks. The mapping lives here
 * rather than on the server because it is presentation: the trigger is the fact, this
 * is the sentence about it. */
const TRIGGER_COPY: Record<string, string> = {
  INITIAL: "Bracket accepted by the engine",
  ENTRY_FILLED: "Entry filled",
  FILLED: "Position closed by a protective order",
  BREAK_EVEN: "Rung 1 armed — stop lifted to break-even",
  PROFIT_LOCK: "Rung 2 armed — stop lifted to the profit-lock floor",
  PARTIAL_TP: "Partial take-profit filled",
  TRAIL_STOP: "Rung 3 — trailing stop raised",
  TRAIL_TARGET: "Rung 3 — target widened",
  STOP_FIRED: "Stop fired by the engine",
  STOP_HANDOVER: "Stop handed between the engine and the broker",
  MANUAL: "Levels moved by hand",
};

const RUNG_TRIGGERS = ["BREAK_EVEN", "PROFIT_LOCK", "TRAIL_STOP", "TRAIL_TARGET"];

const money = (value: number) =>
  value.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 });

function since(from?: string): string {
  if (!from) return "—";
  const ms = Date.now() - new Date(from).getTime();
  if (!Number.isFinite(ms) || ms < 0) return "—";
  const hours = Math.floor(ms / 3_600_000);
  const days = Math.floor(hours / 24);
  if (days > 0) return `${days}d ${hours % 24}h`;
  const minutes = Math.floor(ms / 60_000);
  if (hours > 0) return `${hours}h ${minutes % 60}m`;
  return `${minutes}m`;
}

export function BracketView({ id }: { id: number }) {
  const [record, setRecord] = useState<Record_ | null>(null);
  const [log, setLog] = useState<Adjustment[]>([]);
  const [depth, setDepth] = useState<{ shares: number; price: number } | null>(null);
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const { format } = useCurrency();

  useEffect(() => {
    fetch(`${API}/brackets/${id}`)
      .then(async (response) => {
        const answer = await response.json();
        if (!response.ok) throw new Error(answer?.error ?? `HTTP ${response.status}`);
        setRecord(answer.bracket);
        setLog(answer.adjustments ?? []);
        if (answer.bid_shares > 0) {
          setDepth({ shares: answer.bid_shares, price: answer.bid_price });
        }
        setError("");
      })
      .catch((cause) =>
        setError(cause instanceof Error ? cause.message : "could not load this bracket"),
      );
  }, [id, reload]);

  const numbers = useMemo(() => {
    if (!record) return null;
    const fill = record.entry_price ?? record.requested_entry;
    const held = record.quantity;
    const soldQty = record.partial_taken_quantity ?? 0;
    const soldAt = record.partial_fill_price ?? 0;
    const stop = record.stop_price ?? 0;
    const last = record.high_water ?? fill;
    // Banked is only real once the slice has a price against it. Guessing one from
    // the target would report money that was never made.
    const banked = soldQty > 0 && soldAt > 0 ? (soldAt - fill) * soldQty : 0;
    return {
      fill,
      held,
      soldQty,
      banked,
      unrealised: (last - fill) * held,
      // The whole point of the screen: after the ladder lifts, this is positive.
      worstCase: stop > 0 ? (stop - fill) * held + banked : 0,
      fromFill: fill > 0 ? (last / fill - 1) * 100 : 0,
      last,
      stop,
      target: record.target_price ?? 0,
      original: log.find((entry) => entry.trigger === "INITIAL")?.new_stop ?? 0,
    };
  }, [record, log]);

  const lastRung = useMemo(
    () => log.find((entry) => RUNG_TRIGGERS.includes(entry.trigger)),
    [log],
  );

  const refresh = useCallback(() => setReload((value) => value + 1), []);

  if (error) {
    return (
      <div className="tg tg-page">
        <p className="tg-err">{error}</p>
      </div>
    );
  }
  if (!record || !numbers) {
    return (
      <div className="tg tg-page">
        <p className="tg-empty">Loading…</p>
      </div>
    );
  }

  const open = record.state === "ACTIVE" || record.state === "PENDING";
  const lifted = numbers.stop > numbers.fill;

  return (
    <div className="tg tg-page">
      <div className="tg-portalbar">
        <a className="tg-back" href="/positions">‹ Positions &amp; Orders</a>
        <span className="tg-crumb">
          Positions &amp; Orders <b>Bracket #{record.id}</b>
        </span>
        <span className="tg-barspacer" />
        <span className="tg-pill">
          <em>last price</em> <b>${money(numbers.last)}</b>
        </span>
        <span className={`tg-pill tg-enginepill${open && !record.manual_hold ? " on" : ""}`}>
          {record.manual_hold
            ? "YOU ARE DRIVING"
            : open
              ? "ENGINE MAINTAINING"
              : record.state}
        </span>
        <span className="tg-avatar">Y</span>
      </div>

      <div className="tg-brmain">
        <section className="tg-plane">
          <div className="tg-brstatus">
            <span className={`tg-statepill s-${record.state}`}>
              {lifted ? "TRAILING" : record.state}
            </span>
            <span className="tg-step">
              opened {since(record.opened_at)} ago
              {lastRung ? ` · last rung ${since(lastRung.created_at)} ago` : ""}
            </span>
          </div>
          {/* The headline is the order that was sent. What is left of it belongs in
              the line underneath, because the two are different facts and the big
              type should carry the one that does not change. */}
          <h1 className="tg-h1">
            BUY {(numbers.held + numbers.soldQty).toLocaleString()} {record.ticker}
          </h1>
          <p className="tg-brsub">
            avg fill ${money(numbers.fill)}
            {numbers.soldQty > 0
              ? ` · ${numbers.soldQty.toLocaleString()} sold at partial TP`
              : ""}
            {" · "}
            {numbers.held.toLocaleString()} still running
          </p>

          <div className="tg-brstats">
            <div className="tg-brstat">
              <span>Unrealised P/L</span>
              <b className={numbers.unrealised >= 0 ? "good" : "bad"}>
                {numbers.unrealised >= 0 ? "+" : "−"}
                {format(Math.abs(numbers.unrealised))}
              </b>
              <em>on {numbers.held.toLocaleString()} shares</em>
            </div>
            {numbers.soldQty > 0 && (
              <div className="tg-brstat">
                <span>Banked already</span>
                <b className={numbers.banked >= 0 ? "good" : "bad"}>
                  {numbers.banked >= 0 ? "+" : "−"}
                  {format(Math.abs(numbers.banked))}
                </b>
                <em>{numbers.soldQty.toLocaleString()} sh at partial TP</em>
              </div>
            )}
            <div className="tg-brstat">
              <span>Last price</span>
              <b>${money(numbers.last)}</b>
              <em>
                {numbers.fromFill >= 0 ? "+" : ""}
                {numbers.fromFill.toFixed(1)}% from fill
              </em>
            </div>
            <div className="tg-brstat">
              <span>Worst case now</span>
              <b className={numbers.worstCase >= 0 ? "warn" : "bad"}>
                {numbers.worstCase >= 0 ? "+" : "−"}
                {format(Math.abs(numbers.worstCase))}
              </b>
              <em>if the floor is hit</em>
            </div>
          </div>
        </section>

        <div className="tg-col">
          <section className={`tg-floor${lifted ? "" : " atrisk"}`}>
            <div className="tg-floorhead">
              <h2>{lifted ? "FLOOR THE ENGINE IS HOLDING" : "STOP THE ENGINE IS HOLDING"}</h2>
              {lastRung && (
                <span className="tg-floorpill">
                  {TRIGGER_COPY[lastRung.trigger]?.split(" — ")[0] ?? lastRung.trigger}
                </span>
              )}
            </div>
            <div className="tg-floorrow">
              <div>
                <div className="tg-floorprice">${money(numbers.stop)}</div>
                <div className="tg-floorsub">
                  {numbers.original > 0 ? `raised from $${money(numbers.original)} at entry · ` : ""}
                  ${money(numbers.fill)} avg fill
                </div>
              </div>
              <div className="tg-floorpl">
                <b>
                  {numbers.worstCase >= 0 ? "+" : "−"}
                  {format(Math.abs(numbers.worstCase))}
                </b>
                <span>{numbers.worstCase >= 0 ? "locked in if it hits" : "lost if it hits"}</span>
              </div>
            </div>
          </section>

          <AdjustPanel record={record} onChanged={refresh} />

          {open && (
            <ForceExitPanel
              record={record}
              depth={depth}
              lastRung={lastRung?.created_at}
              onChanged={refresh}
            />
          )}
        </div>
      </div>

      <section className="tg-plane">
        <div className="tg-planehead">
          <h2>What the engine has done</h2>
          <span className="tg-step">newest first · nothing here can be undone</span>
        </div>
        <div className="tg-timeline">
          {log.length === 0 && <p className="tg-empty">Nothing yet.</p>}
          {log.map((entry) => (
            <div className={`tg-tlrow${entry.applied === false ? " failed" : ""}`} key={entry.id}>
              <span className="tg-tltime">
                {new Date(entry.created_at).toLocaleTimeString("en-GB", {
                  hour: "2-digit",
                  minute: "2-digit",
                })}
              </span>
              <span className={`tg-tldot t-${entry.trigger}`} />
              <span className="tg-tlwhat">
                {TRIGGER_COPY[entry.trigger] ?? entry.trigger}
                {entry.applied === false && (
                  <em> — refused by the broker: {entry.broker_error || "no reason given"}</em>
                )}
              </span>
              <span className="tg-tlvalue">
                {entry.new_stop ? `$${money(entry.new_stop)}` : ""}
              </span>
            </div>
          ))}
        </div>
      </section>
    </div>
  );
}

/* Moving the levels by hand.
 *
 * Closed rather than open by default. The engine is managing this position and the
 * common case is watching it do so; putting the editor behind a press means an
 * accidental keystroke cannot move a live stop.
 */
function AdjustPanel({
  record, onChanged,
}: {
  record: Record_;
  onChanged: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [stop, setStop] = useState("");
  const [target, setTarget] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [said, setSaid] = useState("");
  const [error, setError] = useState("");

  const apply = async () => {
    setBusy(true);
    setError("");
    setSaid("");
    try {
      const response = await fetch(`${API}/brackets/${record.id}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          ...(Number(stop) > 0 ? { stop_price: Number(stop) } : {}),
          ...(Number(target) > 0 ? { target_price: Number(target) } : {}),
          ...(note.trim() ? { note: note.trim() } : {}),
        }),
      });
      const answer = await response.json();
      if (!response.ok) throw new Error(answer?.error ?? `HTTP ${response.status}`);
      setSaid("Moved. The engine keeps managing from here.");
      setStop("");
      setTarget("");
      setNote("");
      setOpen(false);
      onChanged();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "the move was refused");
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="tg-plane tg-adjust">
      <div className="tg-planehead">
        <h2>Adjust the live bracket</h2>
        <button
          type="button"
          className={`tg-presetpill${open ? " on" : ""}`}
          aria-expanded={open}
          onClick={() => setOpen((value) => !value)}
        >
          {open ? "Close" : "Adjust"}
        </button>
      </div>

      <div className="tg-stack">
        <div className="tg-stackrow tg-tp">
          <b>TAKE PROFIT</b>
          <span className="tg-stackright">
            <i>${money(record.target_price ?? 0)}</i>
          </span>
        </div>
        <div className="tg-stackrow tg-entry">
          <b>AVG FILL</b>
          <span className="tg-stackright">
            <span>{record.quantity.toLocaleString()} sh left</span>
            <i>${money(record.entry_price ?? record.requested_entry)}</i>
          </span>
        </div>
        <div className="tg-stackrow tg-sl">
          <b>STOP LOSS</b>
          <span className="tg-stackright">
            <i>${money(record.stop_price ?? 0)}</i>
          </span>
        </div>
      </div>

      {open && (
        <div className="tg-adjustform">
          <p className="tg-setnote">
            Leave a field empty to leave that level alone. The engine keeps managing
            the bracket afterwards — moving a level by hand is not the same as taking
            it over, and the rungs above where you put the stop will still fire.
          </p>
          <div className="tg-setgrid">
            <label className="tg-card">
              <span>Stop loss</span>
              <span className="tg-inwrap">
                <i className="tg-prefix">$</i>
                <input
                  className="tg-in" inputMode="decimal" value={stop}
                  placeholder={String(record.stop_price ?? "")}
                  onChange={(event) => setStop(sanitizeDecimal(event.target.value))} />
              </span>
            </label>
            <label className="tg-card">
              <span>Take profit</span>
              <span className="tg-inwrap">
                <i className="tg-prefix">$</i>
                <input
                  className="tg-in" inputMode="decimal" value={target}
                  placeholder={String(record.target_price ?? "")}
                  onChange={(event) => setTarget(sanitizeDecimal(event.target.value))} />
              </span>
            </label>
          </div>
          <input
            className="tg-notefield dark" value={note}
            placeholder="why (goes in the log)"
            onChange={(event) => setNote(event.target.value)} />
          <button
            type="button" className="tg-go"
            disabled={busy || (!(Number(stop) > 0) && !(Number(target) > 0))}
            onClick={apply}
          >
            {busy ? "Moving…" : "MOVE THE LEVELS"}
          </button>
        </div>
      )}
      {said && <p className="tg-said">{said}</p>}
      {error && <p className="tg-err">{error}</p>}
    </section>
  );
}

/* The way out of a position the ladder was never built for.
 *
 * The evidence sits above the buttons on purpose: how long it has been held, how long
 * since anything moved, and what the book will take. A force exit is a judgement, and
 * a button with no numbers next to it is a judgement made blind.
 */
function ForceExitPanel({
  record, depth, lastRung, onChanged,
}: {
  record: Record_;
  depth: { shares: number; price: number } | null;
  lastRung?: string;
  onChanged: () => void;
}) {
  const [confirming, setConfirming] = useState<"" | "stop" | "exit">("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const held = record.quantity;
  const cover = depth && depth.shares > 0 ? held / depth.shares : 0;
  const thin = cover > 3 || !depth;

  const call = async (path: string, body: unknown) => {
    setBusy(true);
    setError("");
    try {
      const response = await fetch(`${API}${path}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const answer = await response.json();
      if (!response.ok) throw new Error(answer?.error ?? `HTTP ${response.status}`);
      setConfirming("");
      onChanged();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "that was refused");
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="tg-plane tg-forceexit">
      <h2 className="tg-fetitle">FORCE EXIT</h2>
      <p className="tg-fewhat">
        For the case the ladder was never built for: it never reached the last rung, it
        is not going down either, and the money is just sitting there. Nothing here is
        automatic — you decide.
      </p>

      <div className="tg-festats">
        <div className="tg-brstat">
          <span>Time in position</span>
          <b>{since(record.opened_at)}</b>
        </div>
        <div className="tg-brstat">
          <span>Since last rung</span>
          <b className="warn">{lastRung ? since(lastRung) : "no rung yet"}</b>
        </div>
        <div className="tg-brstat">
          <span>Book will take</span>
          <b className={thin ? "bad" : ""}>
            {depth ? `${depth.shares.toLocaleString()} sh` : "unknown"}
          </b>
          <em>
            {depth
              ? cover <= 1
                ? "the whole position leaves on the bid"
                : `${cover.toFixed(1)}× the best bid`
              : "no bid observed"}
          </em>
        </div>
      </div>

      {confirming === "" ? (
        <div className="tg-ferow">
          <button type="button" className="tg-febtn" onClick={() => setConfirming("stop")}>
            Stop managing it
          </button>
          <button type="button" className="tg-febtn danger" onClick={() => setConfirming("exit")}>
            Exit at market now
          </button>
        </div>
      ) : (
        <div className="tg-ferow">
          <button type="button" className="tg-febtn" disabled={busy}
            onClick={() => setConfirming("")}>
            Cancel
          </button>
          <button
            type="button" className="tg-febtn danger" disabled={busy}
            onClick={() =>
              confirming === "exit"
                ? call(`/brackets/${record.id}/exit`, { note: "force exit at market" })
                : call(`/brackets/${record.id}/close`, {
                    state: "CANCELLED",
                    note: "stopped managing from the bracket screen",
                  })
            }
          >
            {busy
              ? "Working…"
              : confirming === "exit"
                ? `Confirm — sell ${held.toLocaleString()} ${record.ticker} at market`
                : "Confirm — stop managing, keep the shares"}
          </button>
        </div>
      )}

      <p className="tg-fenote">
        <strong>Stop managing it</strong> pulls both legs and leaves the shares open
        with nothing protecting them. <strong>Exit at market</strong> sells{" "}
        {held.toLocaleString()} shares immediately at whatever the book gives you —
        {thin
          ? " and the book here is thinner than the position, so expect to pay for the speed."
          : " the honest way out of a position that has stopped going anywhere."}
      </p>
      {error && <p className="tg-err">{error}</p>}
    </section>
  );
}
