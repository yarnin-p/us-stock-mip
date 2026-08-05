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

type PlacedOrder = {
  id: number;
  state: string;
  ticker: string;
  quantity: number;
  limit_price: number;
};

type SubmitResponse = {
  ticket: Ticket;
  entry_order: PlacedOrder;
  mode: string;
  protection: { stop_price: number; quantity: number; note: string };
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
  const [sending, setSending] = useState(false);
  const [placed, setPlaced] = useState<PlacedOrder | null>(null);
  const [sendError, setSendError] = useState("");
  const [confirming, setConfirming] = useState(false);
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
    // A new set of numbers is a new idea; the order created from the previous
    // one must not stay on screen as though it still describes them.
    setPlaced(null);
    setSendError("");
    setConfirming(false);
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

  // Escape backs out of the confirmation. A person who reaches for it has
  // changed their mind, and hunting for a cancel button is the wrong thing to
  // be doing at that moment.
  useEffect(() => {
    if (!confirming) return;
    function onKey(event: KeyboardEvent) {
      if (event.key === "Escape" && !sending) setConfirming(false);
    }
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [confirming, sending]);

  async function send() {
    if (!ticket || sending) return;
    setSending(true);
    setSendError("");
    try {
      const response = await fetch(`${api}/ticket/submit`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          ticker: ticket.ticker,
          side: ticket.side,
          entry: ticket.entry,
          stop: ticket.stop,
          target: ticket.target || undefined,
          risk_amount: Number(risk),
          send: true,
        }),
      });
      const body = await response.json();
      if (!response.ok) {
        setSendError(body?.error ?? `send failed (${response.status})`);
        return;
      }
      setConfirming(false);
      setPlaced((body as SubmitResponse).entry_order);
    } catch (cause) {
      setSendError(cause instanceof Error ? cause.message : "create failed");
    } finally {
      setSending(false);
    }
  }


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

      {/* Enter moves from the numbers to the confirmation without leaving the
          keyboard; the confirm button is focused there, so a second Enter
          sends. Two deliberate presses, no reaching for the mouse. */}
      <div
        className="ticket-form"
        onKeyDown={(event) => {
          if (event.key !== "Enter" || confirming || !ticket) return;
          event.preventDefault();
          setConfirming(true);
        }}
      >
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

      <p className="ticket-hint">
        <kbd>Enter</kbd> review · <kbd>Enter</kbd> send · <kbd>Esc</kbd> cancel
      </p>

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
          {/* Creating fills in the order; approving still sends it. That
              second step is the last point a person can refuse, so it is not
              collapsed away however much time it costs. */}
          {placed ? (
            <div className="ticket-placed">
              <strong>
                Order #{placed.id} · {placed.state}
              </strong>
              <span>
                {placed.quantity.toLocaleString()} {placed.ticker} @{" "}
                {placed.limit_price}
              </span>
            </div>
          ) : confirming ? (
            /* One screen, every number that matters, and the mode spelled out.
               This is the review step — after it the order goes out, so it
               repeats the figures rather than asking "are you sure?". */
            <div className={`ticket-confirm ${live ? "live" : "paper"}`}>
              <strong>
                {ticket.side} {ticket.shares.toLocaleString()} {ticket.ticker} @{" "}
                {ticket.entry}
              </strong>
              <span>
                stop {ticket.stop} · risk ${money(ticket.actual_risk)} ·{" "}
                {ticket.target ? `target ${ticket.target} · ` : ""}
                {live ? "LIVE — real money" : "paper — simulated"}
              </span>
              <div>
                <button
                  type="button"
                  className={`ticket-send ${live ? "live" : "paper"}`}
                  onClick={send}
                  disabled={sending}
                  autoFocus
                >
                  {sending ? "sending…" : "CONFIRM & SEND"}
                </button>
                <button
                  type="button"
                  className="ticket-cancel"
                  onClick={() => setConfirming(false)}
                  disabled={sending}
                >
                  cancel
                </button>
              </div>
            </div>
          ) : (
            <button
              type="button"
              className={`ticket-send ${live ? "live" : "paper"}`}
              onClick={() => setConfirming(true)}
            >
              {`SEND ${ticket.side} · ${ticket.shares.toLocaleString()} ${ticket.ticker}`}
            </button>
          )}
          {sendError && <p className="ticket-error">{sendError}</p>}
        </div>
      )}
      {pending && !ticket && <p className="ticket-note">sizing…</p>}
    </article>
  );
}
