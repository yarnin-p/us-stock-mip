"use client";

/* Risk and controls.
 *
 * It exists because the ceilings were only ever in the environment, which made them
 * both invisible and immovable: the first sign that MAX_POSITION_VALUE was still at a
 * test value of 75 was an order refused at $197, and widening it meant an edit and a
 * restart — the one thing you cannot do mid-session with a position open.
 *
 * What is here can be changed while the service runs and survives a restart. What is
 * not here is the trading mode, and that is deliberate: paper and live decide which
 * broker adapter gets built at boot, so a switch on this screen would look like it
 * worked and change nothing about where the orders go. That is the worst kind of
 * control to put on a page about money. The mode section shows what is in force and
 * what to do about it instead.
 */

import { useCallback, useEffect, useState } from "react";
import { sanitizeDecimal } from "./inputs";

const API = process.env.NEXT_PUBLIC_API_BASE ?? "http://localhost:8080";

type Config = {
  mode: string;
  automatic_trading: boolean;
  approval_required: boolean;
  kill_switch: boolean;
  allowed_sessions: string[];
  live_entries_enabled: boolean;
  max_position_value: number;
  max_gross_exposure: number;
  max_capital_allocation: number;
  max_daily_loss: number;
  max_risk_per_trade: number;
};

const CEILINGS = [
  {
    key: "max_position_value" as const,
    label: "Max position value",
    unit: "$",
    what: "The most one position may cost. This is the ceiling that refuses an order outright.",
  },
  {
    key: "max_gross_exposure" as const,
    label: "Max gross exposure",
    unit: "$",
    what: "Everything open, added up. A new order is refused if it would take the total past this.",
  },
  {
    key: "max_daily_loss" as const,
    label: "Max daily loss",
    unit: "$",
    what: "Losses past this stop new entries for the rest of the session.",
  },
  {
    key: "max_risk_per_trade" as const,
    label: "Max risk per trade",
    unit: "$",
    what: "What reaching the stop is allowed to cost. Sizing is capped to respect it.",
  },
  {
    key: "max_capital_allocation" as const,
    label: "Max capital allocation",
    unit: "×",
    what: "Share of buying power one position may use. 0.30 means thirty per cent.",
  },
];

const SESSIONS = ["PRE_MARKET", "REGULAR", "AFTER_HOURS"];

export function RiskView() {
  const [config, setConfig] = useState<Config | null>(null);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [sessions, setSessions] = useState<string[]>([]);
  const [kill, setKill] = useState(false);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [said, setSaid] = useState("");
  const [error, setError] = useState("");

  const load = useCallback(() => {
    fetch(`${API}/execution/config`)
      .then(async (response) => {
        const answer = await response.json();
        if (!response.ok) throw new Error(answer?.error ?? `HTTP ${response.status}`);
        setConfig(answer);
        setSessions(answer.allowed_sessions ?? []);
        setKill(Boolean(answer.kill_switch));
        setDraft(
          Object.fromEntries(CEILINGS.map((c) => [c.key, String(answer[c.key] ?? 0)])),
        );
        setError("");
      })
      .catch((cause) =>
        setError(cause instanceof Error ? cause.message : "could not read the settings"),
      );
  }, []);

  useEffect(load, [load]);

  const save = async () => {
    setBusy(true);
    setError("");
    setSaid("");
    try {
      const body: Record<string, unknown> = { allowed_sessions: sessions, kill_switch: kill };
      for (const ceiling of CEILINGS) body[ceiling.key] = Number(draft[ceiling.key] ?? 0);
      if (note.trim()) body.note = note.trim();
      const response = await fetch(`${API}/execution/config`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const answer = await response.json();
      if (!response.ok) throw new Error(answer?.error ?? `HTTP ${response.status}`);
      setConfig(answer);
      setSaid("Saved. In force now, and after a restart.");
      setNote("");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "the change was refused");
    } finally {
      setBusy(false);
    }
  };

  const live = config?.mode === "live";

  return (
    <div className="tg tg-page">
      <div className="tg-portalbar">
        <a className="tg-back" href="/hub">‹ All apps</a>
        <span className="tg-crumb">
          TradeEdge portal <b>Risk &amp; Controls</b>
        </span>
        <span className="tg-barspacer" />
        {config && (
          <span className={`tg-pill tg-modepill${live ? " live" : ""}`}>
            {config.mode.toUpperCase()}
          </span>
        )}
        <span className="tg-avatar">Y</span>
      </div>

      <div className="tg-titlerow">
        <div>
          <div className="tg-status">
            <i className="tg-dot-live" /> Changes take effect immediately and survive a restart
          </div>
          <h1 className="tg-h1">Risk &amp; Controls</h1>
        </div>
      </div>

      {error && <p className="tg-err">{error}</p>}

      <section className="tg-plane">
        <div className="tg-planehead">
          <h2>Risk ceilings</h2>
          <span className="tg-step">refuse an order before it reaches the broker</span>
        </div>
        <div className="tg-setgrid">
          {CEILINGS.map((ceiling) => (
            <label className="tg-card" key={ceiling.key}>
              <span>{ceiling.label}</span>
              <span className="tg-inwrap">
                <i className="tg-prefix">{ceiling.unit}</i>
                <input
                  className="tg-in" inputMode="decimal"
                  value={draft[ceiling.key] ?? ""}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      [ceiling.key]: sanitizeDecimal(event.target.value),
                    }))
                  }
                />
              </span>
              <em className="tg-setwhat">{ceiling.what}</em>
            </label>
          ))}
        </div>
        <p className="tg-setnote">
          Zero means no ceiling. That is a real setting and not a way of clearing the
          field — leaving max position value at zero lets one order spend everything.
        </p>
      </section>

      <section className="tg-plane">
        <div className="tg-planehead">
          <h2>Gates</h2>
          <span className="tg-step">when the system will let an entry through at all</span>
        </div>
        <label className="tg-switch">
          <input type="checkbox" checked={kill}
            onChange={(event) => setKill(event.target.checked)} />
          <span>
            <b>Kill switch</b>
            <em>
              Refuses every new buy while it is on. Exits are never blocked — a switch
              that stopped you selling would be the opposite of a safety control.
            </em>
          </span>
        </label>
        <div className="tg-sessions">
          {SESSIONS.map((session) => (
            <label className="tg-switch" key={session}>
              <input
                type="checkbox" checked={sessions.includes(session)}
                onChange={(event) =>
                  setSessions((current) =>
                    event.target.checked
                      ? [...current, session]
                      : current.filter((item) => item !== session),
                  )
                }
              />
              <span><b>{session.replace("_", " ")}</b></span>
            </label>
          ))}
        </div>
      </section>

      <section className="tg-plane">
        <div className="tg-planehead">
          <h2>Trading mode</h2>
          <span className="tg-step">not changeable from here, and why</span>
        </div>
        <div className={`tg-modeplane${live ? " live" : ""}`}>
          <b>{config?.mode?.toUpperCase() ?? "—"}</b>
          <p>
            {live
              ? "Orders from this system reach the real broker and move real money."
              : "Orders are filled by the paper adapter. Nothing reaches the broker."}
          </p>
        </div>
        <p className="tg-setnote">
          The mode decides which broker adapter is built when the service starts, so it
          cannot be switched while it runs — a toggle here would appear to work and send
          orders to the same place as before. Change <code>TRADING_MODE</code> in{" "}
          <code>.env</code> and restart:
        </p>
        <pre className="tg-setcode">scripts/run-local.sh stop &amp;&amp; scripts/run-local.sh serve</pre>
        <p className="tg-setnote">
          Worth keeping as a restart rather than a button. Paper to live is the single
          most consequential change in this system, and it should cost more than one
          click.
        </p>
      </section>

      <div className="tg-bar">
        <div className="tg-barstat">
          <span>Position ceiling</span>
          <b>${Number(draft.max_position_value || 0).toLocaleString()}</b>
        </div>
        <div className="tg-barstat">
          <span>Gross ceiling</span>
          <b>${Number(draft.max_gross_exposure || 0).toLocaleString()}</b>
        </div>
        <div className="tg-barstat">
          <span>Kill switch</span>
          <b className={kill ? "risk" : ""}>{kill ? "ON" : "off"}</b>
        </div>
        <div className="tg-barright">
          <input
            className="tg-notefield" value={note} placeholder="why (goes in the log)"
            onChange={(event) => setNote(event.target.value)}
          />
          <button type="button" className="tg-go" disabled={busy || !config} onClick={save}>
            {busy ? "Saving…" : "SAVE CHANGES"}
          </button>
        </div>
      </div>
      {said && <p className="tg-said">{said}</p>}
    </div>
  );
}
