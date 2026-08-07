"use client";

import { useEffect, useMemo, useState } from "react";

// A ranked list of tickers answers nothing on its own. The same +40% means one
// thing on a two-million-share float that rotated eighty times and something
// else on a large cap that drifted up on a broker note, so every row carries
// the evidence beside the move and the tags stay clickable: the question this
// screen exists to answer is "what did the ones that ran have in common", and
// that needs filtering by cause, not scrolling.

type Reason = { tag: string; label: string };

type Row = {
  rank: number;
  ticker: string;
  reference_price: number;
  reference_source: string;
  high?: number;
  close: number;
  change_pct: number;
  max_change_pct?: number;
  volume?: number;
  relative_volume?: number;
  float_shares?: number;
  float_rotation?: number;
  market_cap?: number;
  has_news: boolean;
  news_title?: string;
  news_published_at?: string;
  catalyst_score?: number;
  reasons: Reason[];
};

type Payload = {
  trading_date: string;
  session: string;
  rows: Row[];
  dates?: string[];
  note?: string;
};

const SESSIONS = [
  { key: "PRE_MARKET", label: "Pre-market", clock: "04:00–09:30 ET" },
  { key: "REGULAR", label: "Regular", clock: "09:30–16:00 ET" },
  { key: "AFTER_HOURS", label: "After-hours", clock: "16:00–20:00 ET" },
] as const;

// Tags that mark the structure a runner tends to have, kept visually distinct
// from the ones that merely describe what happened afterwards.
const STRUCTURAL = new Set([
  "EXTREME_ROTATION",
  "HIGH_ROTATION",
  "MICRO_FLOAT",
  "LOW_FLOAT",
  "EXTREME_RVOL",
  "HIGH_RVOL",
  "STRONG_CATALYST",
]);

const compact = (value?: number) => {
  if (!value) return "—";
  if (value >= 1e9) return `${(value / 1e9).toFixed(2)}B`;
  if (value >= 1e6) return `${(value / 1e6).toFixed(2)}M`;
  if (value >= 1e3) return `${(value / 1e3).toFixed(0)}K`;
  return value.toFixed(0);
};

const price = (value?: number) =>
  value == null ? "—" : value < 1 ? value.toFixed(4) : value.toFixed(2);

export default function GainersBoard({ api }: { api: string }) {
  const [session, setSession] = useState<string>("REGULAR");
  const [date, setDate] = useState<string>("");
  const [payload, setPayload] = useState<Payload | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [active, setActive] = useState<Set<string>>(new Set());

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    const query = new URLSearchParams({ session });
    if (date) query.set("date", date);
    fetch(`${api}/gainers?${query}`, { signal: controller.signal })
      .then(async (response) => {
        const answer = await response.json();
        if (!response.ok) throw new Error(answer?.error ?? "request failed");
        setPayload(answer as Payload);
        setError("");
      })
      .catch((cause) => {
        if (!controller.signal.aborted)
          setError(cause instanceof Error ? cause.message : "load failed");
      })
      .finally(() => setLoading(false));
    return () => controller.abort();
  }, [api, session, date]);

  // Tag counts come from the day being shown, so the filter bar doubles as the
  // answer to "what did today's runners have in common".
  const tags = useMemo(() => {
    const counts = new Map<string, { label: string; count: number }>();
    for (const row of payload?.rows ?? []) {
      for (const reason of row.reasons) {
        const entry = counts.get(reason.tag);
        if (entry) entry.count += 1;
        else counts.set(reason.tag, { label: reason.label, count: 1 });
      }
    }
    return [...counts.entries()].sort((a, b) => b[1].count - a[1].count);
  }, [payload]);

  const rows = useMemo(() => {
    const all = payload?.rows ?? [];
    if (active.size === 0) return all;
    // Every selected tag must be present: narrowing is the point, and a name
    // that merely gapped is not the same as one that gapped on a micro float.
    return all.filter((row) => {
      const present = new Set(row.reasons.map((reason) => reason.tag));
      return [...active].every((tag) => present.has(tag));
    });
  }, [payload, active]);

  const toggle = (tag: string) =>
    setActive((current) => {
      const next = new Set(current);
      if (next.has(tag)) next.delete(tag);
      else next.add(tag);
      return next;
    });

  return (
    <section className="gb">
      <header className="gb-head">
        <h2>Session gainers</h2>
        <div className="gb-dates">
          <select value={date} onChange={(event) => setDate(event.target.value)}>
            <option value="">ล่าสุด</option>
            {(payload?.dates ?? []).map((day) => (
              <option key={day} value={day}>
                {day}
              </option>
            ))}
          </select>
          {payload?.trading_date && <b>{payload.trading_date}</b>}
        </div>
      </header>

      <div className="gb-sessions">
        {SESSIONS.map((option) => (
          <button
            key={option.key}
            type="button"
            className={session === option.key ? "on" : ""}
            onClick={() => setSession(option.key)}
          >
            {option.label}
            <em>{option.clock}</em>
          </button>
        ))}
      </div>

      {tags.length > 0 && (
        <div className="gb-tags">
          {tags.map(([tag, entry]) => (
            <button
              key={tag}
              type="button"
              className={`${active.has(tag) ? "on" : ""} ${
                STRUCTURAL.has(tag) ? "structural" : ""
              }`}
              onClick={() => toggle(tag)}
            >
              {entry.label}
              <span>{entry.count}</span>
            </button>
          ))}
          {active.size > 0 && (
            <button type="button" className="gb-clear" onClick={() => setActive(new Set())}>
              ล้างตัวกรอง
            </button>
          )}
        </div>
      )}

      {error && <p className="gb-error">{error}</p>}
      {loading && <p className="gb-note">กำลังโหลด…</p>}
      {!loading && payload?.note && <p className="gb-note">{payload.note}</p>}

      {rows.length > 0 && (
        <div className="gb-scroll">
          <table className="gb-table">
            <thead>
              <tr>
                <th>#</th>
                <th>Ticker</th>
                <th className="num">ปิด</th>
                <th className="num">เปลี่ยน</th>
                <th className="num">สูงสุดที่ทำได้</th>
                <th className="num">Vol</th>
                <th className="num">RVol</th>
                <th className="num">Float</th>
                <th className="num">Rotation</th>
                <th>ทำไมถึงติด</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.ticker}>
                  <td className="gb-rank">{row.rank}</td>
                  <td className="gb-ticker">
                    <b>{row.ticker}</b>
                    {row.news_title && (
                      // The headline is the evidence for the news tag; hiding
                      // it behind a click would make the tag unverifiable.
                      <span className="gb-news" title={row.news_title}>
                        {row.news_title}
                      </span>
                    )}
                  </td>
                  <td className="num">${price(row.close)}</td>
                  <td className="num gb-up">+{row.change_pct.toFixed(1)}%</td>
                  <td className="num gb-mfe">
                    {row.max_change_pct ? `+${row.max_change_pct.toFixed(1)}%` : "—"}
                  </td>
                  <td className="num">{compact(row.volume)}</td>
                  <td className="num">
                    {row.relative_volume ? `${row.relative_volume.toFixed(1)}x` : "—"}
                  </td>
                  <td className="num">{compact(row.float_shares)}</td>
                  <td className={`num ${(row.float_rotation ?? 0) >= 2 ? "gb-hot" : ""}`}>
                    {row.float_rotation ? `${row.float_rotation.toFixed(1)}x` : "—"}
                  </td>
                  <td>
                    <div className="gb-reasons">
                      {row.reasons.map((reason) => (
                        <span
                          key={reason.tag}
                          className={STRUCTURAL.has(reason.tag) ? "structural" : ""}
                        >
                          {reason.label}
                        </span>
                      ))}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {!loading && !payload?.note && rows.length === 0 && (payload?.rows?.length ?? 0) > 0 && (
        <p className="gb-note">ไม่มีตัวไหนตรงกับตัวกรองที่เลือก</p>
      )}
    </section>
  );
}
