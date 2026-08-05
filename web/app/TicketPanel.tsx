"use client";

import { useEffect, useRef, useState } from "react";

// The broker app is the bottleneck, not the chart: every leg is its own screen
// and each submission re-authenticates, so the stop lands after the move has
// already turned. This panel asks for the four numbers a chart read produces
// and sizes the rest, previewing as you type so the ticket is ready before the
// decision is.

type Ticket = {
  ticker: string;
  side: "BUY" | "SELL";
  entry: number;
  stop: number;
  target?: number;
  shares: number;
  notional: number;
  risk_amount: number;
  risk_per_share: number;
  actual_risk: number;
  reward_risk?: number;
  stop_distance: number;
  capped_by?: string;
};

type PreviewResponse = {
  ticket: Ticket;
  mode: string;
  warnings?: string[];
};

const money = (value: number) =>
  value.toLocaleString(undefined, { maximumFractionDigits: 2 });

export default function TicketPanel({
  api,
  defaultRisk,
}: {
  api: string;
  defaultRisk: number;
}) {
  const [side, setSide] = useState<"BUY" | "SELL">("BUY");
  const [ticker, setTicker] = useState("");
  const [entry, setEntry] = useState("");
  const [stop, setStop] = useState("");
  const [target, setTarget] = useState("");
  const [risk, setRisk] = useState(String(defaultRisk));
  const [preview, setPreview] = useState<PreviewResponse | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const latest = useRef(0);

  // Previewing as the numbers are typed is the point: a ticket that has to be
  // submitted before it can be checked is the slow path this replaces.
  useEffect(() => {
    const numeric = {
      entry: Number(entry),
      stop: Number(stop),
      target: Number(target),
      risk_amount: Number(risk),
    };
    if (!ticker.trim() || !numeric.entry || !numeric.stop || !numeric.risk_amount) {
      setPreview(null);
      setError("");
      return;
    }
    const requestID = ++latest.current;
    const controller = new AbortController();
    const timer = setTimeout(async () => {
      setPending(true);
      try {
        const response = await fetch(`${api}/ticket/preview`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            ticker: ticker.trim(),
            side,
            entry: numeric.entry,
            stop: numeric.stop,
            target: numeric.target || undefined,
            risk_amount: numeric.risk_amount,
          }),
          signal: controller.signal,
        });
        // A slower earlier request must not overwrite a newer answer.
        if (requestID !== latest.current) return;
        const body = await response.json();
        if (!response.ok) {
          setPreview(null);
          setError(body?.error ?? `request failed (${response.status})`);
          return;
        }
        setError("");
        setPreview(body as PreviewResponse);
      } catch (cause) {
        if (requestID === latest.current && !controller.signal.aborted) {
          setPreview(null);
          setError(cause instanceof Error ? cause.message : "preview failed");
        }
      } finally {
        if (requestID === latest.current) setPending(false);
      }
    }, 180);
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [api, ticker, side, entry, stop, target, risk]);

  const ticket = preview?.ticket;
  const live = preview?.mode === "live";

  return (
    <article className="ticket-panel">
      <header>
        <span>
          <small>RISK-SIZED ORDER</small>
          <strong>Ticket</strong>
        </span>
        {/* The mode must be unmistakable: a ticket that looks the same on paper
            and live invites the wrong assumption at the worst moment. */}
        <b className={live ? "mode-live" : "mode-paper"}>
          {preview?.mode?.toUpperCase() ?? "—"}
        </b>
      </header>

      <div className="ticket-form">
        <div className="ticket-side">
          {(["BUY", "SELL"] as const).map((option) => (
            <button
              key={option}
              type="button"
              className={side === option ? `active ${option.toLowerCase()}` : ""}
              onClick={() => setSide(option)}
            >
              {option}
            </button>
          ))}
        </div>
        <label>
          <small>TICKER</small>
          <input
            value={ticker}
            autoCapitalize="characters"
            onChange={(event) => setTicker(event.target.value.toUpperCase())}
            placeholder="ABCD"
          />
        </label>
        <label>
          <small>ENTRY</small>
          <input
            value={entry}
            inputMode="decimal"
            onChange={(event) => setEntry(event.target.value)}
            placeholder="10.00"
          />
        </label>
        <label>
          <small>STOP</small>
          <input
            value={stop}
            inputMode="decimal"
            onChange={(event) => setStop(event.target.value)}
            placeholder="9.50"
          />
        </label>
        <label>
          <small>TARGET</small>
          <input
            value={target}
            inputMode="decimal"
            onChange={(event) => setTarget(event.target.value)}
            placeholder="optional"
          />
        </label>
        <label>
          <small>RISK</small>
          <input
            value={risk}
            inputMode="decimal"
            onChange={(event) => setRisk(event.target.value)}
          />
        </label>
      </div>

      {error && <p className="ticket-error">{error}</p>}

      {ticket && (
        <div className="ticket-result">
          <div className="ticket-headline">
            <span>
              <small>SHARES</small>
              <strong>{ticket.shares.toLocaleString()}</strong>
            </span>
            <span>
              <small>NOTIONAL</small>
              <strong>${money(ticket.notional)}</strong>
            </span>
            <span>
              <small>RISK</small>
              <strong>${money(ticket.actual_risk)}</strong>
            </span>
            <span>
              <small>R:R</small>
              <strong>{ticket.reward_risk ? `${ticket.reward_risk}:1` : "—"}</strong>
            </span>
          </div>
          <p className="ticket-detail">
            {ticket.side} {ticket.shares.toLocaleString()} {ticket.ticker} @{" "}
            {ticket.entry} · stop {ticket.stop} ({(ticket.stop_distance * 100).toFixed(1)}%
            away, ${money(ticket.risk_per_share)}/share)
            {ticket.target ? ` · target ${ticket.target}` : ""}
          </p>
          {ticket.capped_by && (
            <p className="ticket-note">
              Size reduced by {ticket.capped_by.replace(/_/g, " ").toLowerCase()};
              this ticket risks less than the budget asked for.
            </p>
          )}
          {preview?.warnings?.map((warning) => (
            <p className="ticket-warning" key={warning}>
              {warning}
            </p>
          ))}
          {/* Sizing only. Sending the order stays a deliberate act on the
              execution path, which keeps its own approval and kill switch. */}
          <p className="ticket-note">
            Preview only — this does not place an order.
          </p>
        </div>
      )}
      {pending && !ticket && <p className="ticket-note">sizing…</p>}
    </article>
  );
}
