"use client";

import { useEffect, useRef, useState } from "react";

// The broker app is the bottleneck, not the chart: every leg is its own screen
// and each submission re-authenticates, so the stop lands after the move has
// already turned. This panel keeps the shape a trader already knows from those
// apps — side tabs, stacked fields with steppers, one large action button — and
// removes the part that costs the setup its timing.

type Ticket = {
  ticker: string;
  side: "BUY" | "SELL";
  entry: number;
  stop: number;
  target?: number;
  shares: number;
  notional: number;
  actual_risk: number;
  risk_per_share: number;
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

type PreviewResponse = {
  ticket: Ticket;
  mode: string;
  usd_thb: number;
  warnings?: string[];
};

const money = (value: number) =>
  value.toLocaleString(undefined, { maximumFractionDigits: 2 });

// step nudges a price field the way a broker app's +/- does. The increment
// follows the price: a cent is meaningless on a $300 share and far too coarse
// on one trading at twenty cents.
function step(value: string, direction: 1 | -1): string {
  const current = Number(value);
  if (!Number.isFinite(current)) return value;
  const increment = current >= 100 ? 0.1 : current >= 1 ? 0.01 : 0.001;
  const next = Math.max(0, current + increment * direction);
  return next.toFixed(increment === 0.001 ? 4 : 2);
}

export default function TicketPanel({ api }: { api: string }) {
  const [side, setSide] = useState<"BUY" | "SELL">("BUY");
  const [ticker, setTicker] = useState("");
  const [price, setPrice] = useState("");
  const [sl, setSL] = useState("");
  const [tp, setTP] = useState("");
  const [shares, setShares] = useState("");
  const [preview, setPreview] = useState<PreviewResponse | null>(null);
  const [error, setError] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [sending, setSending] = useState(false);
  const [placed, setPlaced] = useState<PlacedOrder | null>(null);
  const [rate, setRate] = useState(33.6);
  const latest = useRef(0);

  const payload = () => ({
    ticker: ticker.trim(),
    side,
    entry: Number(price),
    stop: Number(sl),
    target: Number(tp) || undefined,
    shares: Number(shares),
  });

  // Priced as the numbers are typed. A ticket that must be submitted before it
  // can be checked is the slow path this replaces.
  useEffect(() => {
    if (!ticker.trim() || !Number(price) || !Number(sl) || !Number(shares)) {
      setPreview(null);
      setError("");
      return;
    }
    // A new set of numbers is a new idea; the order placed from the previous
    // one must not stay on screen as though it still describes them.
    setPlaced(null);
    setConfirming(false);
    const requestID = ++latest.current;
    const controller = new AbortController();
    const timer = setTimeout(async () => {
      try {
        const response = await fetch(`${api}/ticket/preview`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(payload()),
          signal: controller.signal,
        });
        // A slower earlier request must not overwrite a newer answer.
        if (requestID !== latest.current) return;
        const answer = await response.json();
        if (!response.ok) {
          setPreview(null);
          setError(answer?.error ?? `request failed (${response.status})`);
          return;
        }
        setError("");
        if (answer.usd_thb > 0) setRate(answer.usd_thb);
        setPreview(answer as PreviewResponse);
      } catch (cause) {
        if (requestID === latest.current && !controller.signal.aborted) {
          setPreview(null);
          setError(cause instanceof Error ? cause.message : "preview failed");
        }
      }
    }, 180);
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [api, ticker, side, price, sl, tp, shares]);

  const ticket = preview?.ticket;
  const live = preview?.mode === "live";

  // Escape backs out. Someone reaching for it has changed their mind, and that
  // is the wrong moment to be hunting for a cancel button.
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
    setError("");
    try {
      const response = await fetch(`${api}/ticket/submit`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ...payload(), send: true }),
      });
      const answer = await response.json();
      if (!response.ok) {
        setError(answer?.error ?? `send failed (${response.status})`);
        return;
      }
      setConfirming(false);
      setPlaced(answer.entry_order as PlacedOrder);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "send failed");
    } finally {
      setSending(false);
    }
  }

  const priceField = (
    label: string,
    value: string,
    set: (next: string) => void,
    placeholder: string,
  ) => (
    <label className="tk-row">
      <span>{label}</span>
      <div className="tk-stepper">
        <button type="button" onClick={() => set(step(value, -1))} tabIndex={-1}>
          −
        </button>
        <input
          value={value}
          inputMode="decimal"
          onChange={(event) => set(event.target.value)}
          placeholder={placeholder}
        />
        <button type="button" onClick={() => set(step(value, 1))} tabIndex={-1}>
          +
        </button>
      </div>
    </label>
  );

  return (
    <article className="tk">
      <header className="tk-head">
        <input
          className="tk-ticker"
          value={ticker}
          autoCapitalize="characters"
          onChange={(event) => setTicker(event.target.value.toUpperCase())}
          placeholder="TICKER"
        />
        {/* The mode must be unmistakable: a ticket that looks the same on paper
            and live invites the wrong assumption at the worst moment. */}
        <b className={live ? "tk-live" : "tk-paper"}>
          {preview?.mode?.toUpperCase() ?? "—"}
        </b>
      </header>

      {/* Side first and full width, the way every broker app puts it: the
          direction is the one thing that must never be picked by accident. */}
      <div className="tk-side">
        {(["BUY", "SELL"] as const).map((option) => (
          <button
            key={option}
            type="button"
            className={side === option ? `on ${option.toLowerCase()}` : ""}
            onClick={() => setSide(option)}
          >
            {option}
          </button>
        ))}
      </div>

      {/* Enter carries the ticket to the review without leaving the keyboard;
          the send button holds focus there, so a second Enter places it. */}
      <div
        className="tk-fields"
        onKeyDown={(event) => {
          if (event.key !== "Enter" || confirming || !ticket) return;
          event.preventDefault();
          setConfirming(true);
        }}
      >
        {priceField("Price", price, setPrice, "2.06")}
        {priceField("Stop loss", sl, setSL, "1.95")}
        {priceField("Take profit", tp, setTP, "optional")}
        <label className="tk-row">
          <span>Shares</span>
          <div className="tk-stepper">
            <button
              type="button"
              tabIndex={-1}
              onClick={() =>
                setShares(String(Math.max(0, (Number(shares) || 0) - 100)))
              }
            >
              −
            </button>
            <input
              value={shares}
              inputMode="numeric"
              onChange={(event) => setShares(event.target.value)}
              placeholder="500"
            />
            <button
              type="button"
              tabIndex={-1}
              onClick={() => setShares(String((Number(shares) || 0) + 100))}
            >
              +
            </button>
          </div>
        </label>
      </div>

      <p className="tk-hint">
        <kbd>Enter</kbd> review · <kbd>Enter</kbd> send · <kbd>Esc</kbd> cancel
      </p>

      {error && <p className="tk-error">{error}</p>}

      {ticket && (
        <div className="tk-summary">
          <div>
            <span>Cost</span>
            <b>${money(ticket.notional)}</b>
          </div>
          {/* Risk follows from the size rather than setting it, and is shown in
              the currency the account is budgeted in as well as the one it
              trades in. */}
          <div>
            <span>Loss if stopped</span>
            <b>฿{money(ticket.actual_risk * rate)}</b>
            <em>${money(ticket.actual_risk)}</em>
          </div>
          <div>
            <span>Reward : risk</span>
            <b>{ticket.reward_risk ? `${ticket.reward_risk} : 1` : "—"}</b>
          </div>
          <div>
            <span>Stop distance</span>
            <b>{(ticket.stop_distance * 100).toFixed(1)}%</b>
          </div>
        </div>
      )}

      {ticket?.capped_by && (
        <p className="tk-note">
          Size reduced by {ticket.capped_by.replace(/_/g, " ").toLowerCase()}.
        </p>
      )}
      {preview?.warnings?.map((warning) => (
        <p className="tk-warn" key={warning}>
          {warning}
        </p>
      ))}

      {ticket && placed && (
        <div className="tk-placed">
          <b>
            Order #{placed.id} · {placed.state}
          </b>
          <span>
            {placed.quantity.toLocaleString()} {placed.ticker} @{" "}
            {placed.limit_price}
          </span>
        </div>
      )}

      {ticket && !placed && !confirming && (
        <button
          type="button"
          className={`tk-go ${side.toLowerCase()}`}
          onClick={() => setConfirming(true)}
        >
          {side} {ticket.shares.toLocaleString()} {ticket.ticker}
        </button>
      )}

      {ticket && !placed && confirming && (
        /* The review repeats the figures rather than asking "are you sure?" —
           the numbers are what needs checking, and the mode is spelled out so a
           live order is never sent believing it was paper. */
        <div className={`tk-confirm ${live ? "live" : "paper"}`}>
          <b>
            {ticket.side} {ticket.shares.toLocaleString()} {ticket.ticker} @{" "}
            {ticket.entry}
          </b>
          <span>
            SL {ticket.stop}
            {ticket.target ? ` · TP ${ticket.target}` : ""} · risk ฿
            {money(ticket.actual_risk * rate)} ·{" "}
            {live ? "LIVE — real money" : "paper — simulated"}
          </span>
          <div>
            <button
              type="button"
              className={`tk-go ${side.toLowerCase()}`}
              onClick={send}
              disabled={sending}
              autoFocus
            >
              {sending ? "sending…" : "CONFIRM"}
            </button>
            <button
              type="button"
              className="tk-cancel"
              onClick={() => setConfirming(false)}
              disabled={sending}
            >
              cancel
            </button>
          </div>
        </div>
      )}
    </article>
  );
}
