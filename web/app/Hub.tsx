"use client";

/* The portal hub — the launcher the handoff describes.
 *
 * It answers two questions and then gets out of the way: what happened while you were
 * away, and where are you going. The terminal is reachable in one keystroke because
 * that is the thing most often wanted, and every other app is a card.
 *
 * Numbers here are wired to the API where the API has them. Where it does not, the
 * figure reads as unavailable rather than borrowing the design's fixture: a hub whose
 * headline equity is a number nobody measured is worse than one admitting it does not
 * know yet.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { live as isLive } from "./bracketState";
import { useCurrency } from "./currency";
import { sanitizeTicker } from "./inputs";

const API = process.env.NEXT_PUBLIC_API_BASE ?? "http://localhost:8080";

type BracketRow = { id: number; ticker: string; state: string; updated_at?: string };

const APPS = [
  {
    key: "terminal",
    href: "/terminal",
    tag: "PLAN",
    name: "Order Terminal",
    what:
      "Size a trade and write the bracket spec — entry, stop, target and the rung " +
      "rules — then submit it.",
    skin: "amber",
    mark: "dot",
  },
  {
    key: "scanner",
    href: "/scanner",
    tag: "FIND",
    name: "Scanner",
    what:
      "Daily scan, gainers and your watchlist in one table — send a candidate " +
      "straight to the terminal.",
    skin: "dark",
    mark: "ring",
  },
  {
    key: "positions",
    href: "/positions",
    tag: "MANAGE",
    name: "Positions & Orders",
    what:
      "Brackets the engine is maintaining right now, every order it sent, and the " +
      "full history.",
    skin: "green",
    mark: "bar",
  },
  {
    key: "performance",
    href: "/performance",
    tag: "REVIEW",
    name: "Performance",
    what:
      "What the plans actually returned, and which of the rules were carrying the " +
      "result.",
    skin: "dark",
    mark: "tall",
  },
  {
    key: "risk",
    href: "/risk",
    tag: "GUARD",
    name: "Risk & Controls",
    what: "Position limits, the daily risk budget, and the kill switch that overrides both.",
    skin: "red",
    mark: "diamond",
  },
] as const;

export function HubView() {
  const [ticker, setTicker] = useState("");
  const [rows, setRows] = useState<BracketRow[]>([]);
  const [loadError, setLoadError] = useState("");
  const [mode, setMode] = useState("");
  /* The risk card should say what the gate is actually set to. "limits and kill
   * switch" describes the screen; a ceiling and a switch state describe the system,
   * and the second is the only one worth a glance from here. */
  const [risk, setRisk] = useState<{
    kill_switch: boolean; max_position_value: number; mode: string;
  } | null>(null);
  // Same control as the terminal, same stored choice.
  const { currency, setCurrency } = useCurrency();

  useEffect(() => {
    fetch(`${API}/execution/config`)
      .then((response) => (response.ok ? response.json() : null))
      .then((answer) => { if (answer) setRisk(answer); })
      .catch(() => {});
  }, []);

  useEffect(() => {
    fetch(`${API}/brackets?limit=50`)
      .then(async (response) => {
        const answer = await response.json();
        if (!response.ok) throw new Error(answer?.error ?? `HTTP ${response.status}`);
        setRows(answer?.brackets ?? []);
        setMode(answer?.mode ?? "");
        setLoadError("");
      })
      .catch((cause) =>
        setLoadError(cause instanceof Error ? cause.message : "could not reach the API"),
      );
  }, []);

  const live = useMemo(
    () => rows.filter((row: BracketRow) => isLive(row.state)),
    [rows],
  );

  const go = useCallback(() => {
    const clean = ticker.trim().toUpperCase();
    window.location.href = clean ? `/terminal?ticker=${clean}` : "/terminal";
  }, [ticker]);

  /* T jumps to the ticker field, and is ignored while something else is being typed
   * into -- a shortcut that steals a keystroke mid-word is worse than no shortcut. */
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "t" && event.key !== "T") return;
      const active = document.activeElement;
      if (active instanceof HTMLInputElement || active instanceof HTMLTextAreaElement) return;
      event.preventDefault();
      document.getElementById("hub-ticker")?.focus();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  /* Picked after mount, not during render. The server has no idea what hour it is
   * where you are, so choosing the greeting in the render body guarantees the server
   * HTML and the first client pass disagree -- which is a hydration failure, and it
   * puts a full-screen error over the app in development. Empty until mounted. */
  const [greeting, setGreeting] = useState("");
  useEffect(() => {
    const hour = new Date().getHours();
    setGreeting(hour < 12 ? "Good morning" : hour < 18 ? "Good afternoon" : "Good evening");
  }, []);

  return (
    <div className="tg tg-hub">
      <div className="tg-portalbar">
        <span className="tg-pill tg-brand">
          <i>T</i> TradeEdge portal
        </span>
        <span className="tg-barspacer" />
        <span className="tg-fx">
          <span className="tg-seg">
            {(["THB", "USD"] as const).map((code) => (
              <button
                key={code} type="button" aria-pressed={currency === code}
                className={currency === code ? "on" : ""}
                onClick={() => setCurrency(code)}
              >
                {code}
              </button>
            ))}
          </span>
        </span>
        {mode && (
          <span className={`tg-pill tg-modepill${mode === "live" ? " live" : ""}`}>
            {mode.toUpperCase()}
          </span>
        )}
        <span className="tg-avatar">Y</span>
      </div>

      <div className="tg-hero">
        <section className="tg-heroleft">
          <p className="tg-greeting">{greeting ? `${greeting}, Yarnin` : " "}</p>
          <h1 className="tg-heroh1">Where to today?</h1>
          <div className="tg-herostats">
            <div className="tg-herostat">
              <span>Brackets live</span>
              <b>{loadError ? "—" : live.length}</b>
              <em>engine maintained</em>
            </div>
            <div className="tg-herostat">
              <span>Plans in history</span>
              <b>{loadError ? "—" : rows.length}</b>
              <em>written from the terminal</em>
            </div>
            <div className="tg-herostat">
              <span>Portfolio equity</span>
              <b>—</b>
              <em>not wired yet</em>
            </div>
            <div className="tg-herostat">
              <span>Open P/L</span>
              <b>—</b>
              <em>not wired yet</em>
            </div>
          </div>
          {loadError && <p className="tg-err">{loadError}</p>}
        </section>

        <section className="tg-heroright">
          <h2>START A NEW BRACKET</h2>
          <label className="tg-heroticker">
            <span>Ticker</span>
            <input
              id="hub-ticker" value={ticker} placeholder="KWM"
              spellCheck={false} autoComplete="off"
              onChange={(event) => setTicker(sanitizeTicker(event.target.value))}
              onKeyDown={(event) => { if (event.key === "Enter") go(); }}
            />
          </label>
          <p className="tg-heronote">
            Nothing is stored before you submit — the terminal opens empty every time,
            and a bracket only exists once the engine accepts it.
          </p>
          <button type="button" className="tg-herocta" onClick={go}>
            Open the terminal ›
          </button>
          <p className="tg-herohint">or press T to jump to this field</p>
        </section>
      </div>

      <section>
        <div className="tg-appshead">
          <h2>All apps</h2>
          <span>five tools, one login — each opens full-screen</span>
        </div>
        <div className="tg-appgrid">
          {APPS.map((app) => (
            <a
              key={app.key}
              className={`tg-app skin-${app.skin}${
                app.key === "risk" && risk?.kill_switch ? " alarm" : ""
              }`}
              href={app.href}
            >
              <span className={`tg-mark mark-${app.mark}`} />
              <span className="tg-apptag">{app.tag}</span>
              <span className="tg-appname">{app.name}</span>
              <span className="tg-appwhat">{app.what}</span>
              <span className="tg-appfoot">
                <em>
                  {app.key === "terminal" && "opens empty"}
                  {app.key === "positions" &&
                    (loadError ? "—" : `${live.length} maintained · ${rows.length} total`)}
                  {app.key === "scanner" && "open the table"}
                  {app.key === "performance" && "review the record"}
                  {app.key === "risk" &&
                    (risk
                      ? risk.kill_switch
                        ? "KILL SWITCH ON"
                        : `$${risk.max_position_value.toLocaleString()} cap · gate open`
                      : "limits and kill switch")}
                </em>
                <i>›</i>
              </span>
            </a>
          ))}
        </div>
      </section>

      <section className="tg-plane">
        <div className="tg-planehead">
          <div>
            <h2>Today in the portal</h2>
            <p className="tg-laddersub">whatever happened while you were away</p>
          </div>
        </div>
        {live.length === 0 ? (
          <p className="tg-empty">
            Nothing is live. Type a ticker above and the terminal opens on it.
          </p>
        ) : (
          <div className="tg-feed">
            {live.slice(0, 8).map((row) => (
              <a key={row.id} className="tg-feedrow" href={`/terminal`}>
                <span className="tg-feedapp">
                  <i className="tg-dot-live" /> Order Terminal
                </span>
                <span className="tg-feedtext">
                  {row.ticker} — the engine is maintaining this bracket
                </span>
                <span className="tg-feedvalue">{row.state}</span>
              </a>
            ))}
          </div>
        )}
      </section>
    </div>
  );
}
