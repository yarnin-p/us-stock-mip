"use client";

/* The burst scanner.
 *
 * One list, newest first, and every row is a claim with its evidence attached: the
 * name, how far it travelled, how long that took, and from what price to what price.
 * "+23%" on its own is a number an operator has to go and check. "+23% in 47s, 1.02
 * to 1.26" is a thing they can act on or dismiss without leaving the screen.
 *
 * Clicking a row opens the order terminal with the ticker and the price already in
 * it. The whole point of catching a move early is spent if the next step is typing
 * a symbol into another screen.
 *
 * The health line is not decoration. A screen with no alerts and a screen with a
 * dead feed look identical, and one of them means the market is quiet while the
 * other means nothing is being watched at all.
 */

import { useCallback, useEffect, useState } from "react";
import { useCurrency } from "./currency";

const API = process.env.NEXT_PUBLIC_API_BASE ?? "http://localhost:8080";

type BurstAlert = {
  ticker: string;
  gain: number;
  price: number;
  low: number;
  took_seconds: number;
  at: string;
};

type BurstFeed = {
  running: boolean;
  alerts: BurstAlert[] | null;
  watching?: number;
  fired_today?: number;
  detail?: string;
};

const money = (value: number) =>
  value.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 4 });

// Seconds, then minutes. A move that took 47 seconds and one that took 4 minutes are
// different animals and rounding both to "moments ago" throws that away.
function took(seconds: number): string {
  if (seconds < 90) return `${Math.round(seconds)}s`;
  return `${Math.floor(seconds / 60)}m${String(Math.round(seconds % 60)).padStart(2, "0")}s`;
}

function ago(iso: string, now: number): string {
  const seconds = Math.max(0, (now - new Date(iso).getTime()) / 1000);
  if (seconds < 60) return `${Math.round(seconds)}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  return `${Math.floor(seconds / 3600)}h ago`;
}

export function ScannerView() {
  const [feed, setFeed] = useState<BurstFeed | null>(null);
  const [error, setError] = useState("");
  // Null until mounted, for the same reason the clock is: this route is prerendered
  // and the server cannot know what time it is when the page opens.
  const [now, setNow] = useState<number | null>(null);
  const { rate, symbol } = useCurrency();

  const load = useCallback(() => {
    fetch(`${API}/bursts`)
      .then((response) => response.json())
      .then((payload: BurstFeed) => {
        setFeed(payload);
        setError("");
      })
      .catch((cause) =>
        setError(cause instanceof Error ? cause.message : "could not read the scanner"),
      );
  }, []);

  useEffect(() => {
    setNow(Date.now());
    load();
    // Two seconds. The alert's whole value is that it is current, and a screen that
    // refreshes on a minute is a screen that shows moves which have already run.
    const timer = setInterval(() => {
      setNow(Date.now());
      load();
    }, 2000);
    return () => clearInterval(timer);
  }, [load]);

  const alerts = feed?.alerts ?? [];

  return (
    <div className="tg tg-page tg-scanpage">
      <header className="tg-bar tg-scanbar">
        <a className="tg-back" href="/hub">
          ‹ All apps
        </a>
        <div className="tg-scantitle">
          <h1>Bursts</h1>
          <span>a name that moved 20% inside five minutes — not a name that is up 20%</span>
        </div>
        <div className={`tg-scanhealth ${health(feed)}`}>
          {!feed && "connecting…"}
          {feed && !feed.running && (feed.detail ?? "no scanner in this deployment")}
          {feed?.running && (
            <>
              <b>{feed.watching ?? 0}</b> watched · <b>{feed.fired_today ?? 0}</b> today
            </>
          )}
        </div>
      </header>

      {error && <p className="tg-err">{error}</p>}

      {feed?.running && feed.watching === 0 && (
        <p className="tg-scanwarn">
          The scanner is running and watching nothing. An empty list below means the
          feed is not delivering, not that the market is quiet.
        </p>
      )}

      <div className="tg-scanlist">
        {alerts.map((alert) => (
          <a
            key={`${alert.ticker}-${alert.at}`}
            className="tg-scanrow"
            /* Straight into the ticket, with the ticker and the price it fired at.
               Catching a move early is spent if the next step is retyping a symbol. */
            href={`/terminal?ticker=${encodeURIComponent(alert.ticker)}&entry=${alert.price}`}
          >
            <span className="tg-scanwhen">{now === null ? "—" : ago(alert.at, now)}</span>
            <span className="tg-scanticker">{alert.ticker}</span>
            <span className="tg-scangain">+{(alert.gain * 100).toFixed(1)}%</span>
            <span className="tg-scantook">in {took(alert.took_seconds)}</span>
            <span className="tg-scanprices">
              ${money(alert.low)} → ${money(alert.price)}
            </span>
            <span className="tg-scanrisk">
              {symbol}
              {Math.round(alert.price * rate).toLocaleString()} / share
            </span>
            <i className="tg-scango">›</i>
          </a>
        ))}

        {alerts.length === 0 && feed?.running && (feed.watching ?? 0) > 0 && (
          <p className="tg-scanempty">
            Nothing yet. {feed.watching} names are being watched and none of them has
            moved 20% inside five minutes.
          </p>
        )}
        {alerts.length === 0 && feed && !feed.running && (
          <p className="tg-scanempty">
            No scanner is running here, so this list would be empty whatever the market
            was doing.
          </p>
        )}
      </div>
    </div>
  );
}

// health decides how loud the status line is. A scanner watching nothing is the one
// state worth colouring, because it is the one that lies by omission.
function health(feed: BurstFeed | null): string {
  if (!feed) return "waiting";
  if (!feed.running) return "off";
  if ((feed.watching ?? 0) === 0) return "alarm";
  return "live";
}
