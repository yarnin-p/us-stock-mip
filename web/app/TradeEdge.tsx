"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { NAV, NAV_KEYS, type NavKey } from "./nav";
import { TerminalView } from "./Terminal";

// TradeEdge — the warm-white admin shell.
//
// Every figure on this screen comes from an endpoint. Where the platform has
// no source for a panel yet, the panel says so rather than showing a plausible
// number: a dashboard that invents its own data is worse than one with gaps,
// because the gaps are the only honest prompt to go and build the source.

const API =
  process.env.NEXT_PUBLIC_API_URL?.replace(/\/$/, "") ?? "http://127.0.0.1:8080";

/* ── types ─────────────────────────────────────────────────────────────── */

type Envelope<T> = { data: T };

type GainerReason = { tag: string; label: string };
type GainerRow = {
  rank: number;
  ticker: string;
  reference_price: number;
  reference_source: string;
  high?: number;
  low?: number;
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
  reasons: GainerReason[];
};
type GainersPayload = {
  trading_date: string;
  session: string;
  rows: GainerRow[];
  dates?: string[];
  note?: string;
};

type DailyPnl = {
  trading_date: string;
  mode: string;
  gross_pnl: number;
  fees: number;
  net_pnl: number;
  entries: number;
  exits: number;
  transactions: number;
};

type WatchItem = { ticker: string; note?: string };

type ExecutionPosition = {
  ticker: string;
  quantity: number;
  average_price: number;
  market_value?: number;
  unrealized_pnl?: number;
};

type Candidate = {
  ticker: string;
  price?: number;
  change_ratio?: number;
  volume?: number;
  score?: number;
};

/* ── formatting ────────────────────────────────────────────────────────── */

const money = (value?: number, digits = 2) =>
  value == null || Number.isNaN(value)
    ? "—"
    : `$${value.toLocaleString("en-US", {
        minimumFractionDigits: digits,
        maximumFractionDigits: digits,
      })}`;

const compact = (value?: number) => {
  if (!value) return "—";
  if (value >= 1e9) return `${(value / 1e9).toFixed(2)}B`;
  if (value >= 1e6) return `${(value / 1e6).toFixed(2)}M`;
  if (value >= 1e3) return `${(value / 1e3).toFixed(0)}K`;
  return value.toFixed(0);
};

const pct = (value?: number, digits = 2) =>
  value == null || Number.isNaN(value) ? "—" : `${value >= 0 ? "+" : ""}${value.toFixed(digits)}%`;

const clock = (at: Date, zone: string) =>
  new Intl.DateTimeFormat("en-GB", {
    timeZone: zone, hour: "2-digit", minute: "2-digit", second: "2-digit",
    hour12: false,
  }).format(at);

/* ── market session ────────────────────────────────────────────────────── */

/* The market session, or the honest absence of one.
 *
 * now is nullable because this page is prerendered: the HTML is written at build
 * time, so the server cannot know what time it is when somebody opens it. Rendering
 * a clock anyway is what broke hydration -- the build-time second and the load-time
 * second are never the same, React 19 treats that as an error rather than a warning,
 * and the whole screen went behind the dev overlay.
 *
 * So nothing time-derived is rendered until the browser has a clock. known says which
 * of the two states this is, so a caller shows dashes rather than a confident "Market
 * Closed" that it worked out from nothing. */
function marketState(now: Date | null) {
  if (!now) {
    return {
      open: false, label: "—", countdown: "--:--:--", weekend: false, known: false,
    };
  }
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: "America/New_York",
    hour: "2-digit", minute: "2-digit", second: "2-digit",
    weekday: "short", hour12: false,
  }).formatToParts(now);
  const get = (type: string) => parts.find((part) => part.type === type)?.value ?? "0";
  const weekday = get("weekday");
  const minutes = Number(get("hour")) * 60 + Number(get("minute"));
  const weekend = weekday === "Sat" || weekday === "Sun";
  const open = !weekend && minutes >= 9 * 60 + 30 && minutes < 16 * 60;
  const label = weekend
    ? "Market Closed"
    : minutes < 4 * 60 ? "Overnight"
    : minutes < 9 * 60 + 30 ? "Pre-Market"
    : minutes < 16 * 60 ? "Market Open"
    : minutes < 20 * 60 ? "After-Hours"
    : "Overnight";
  // Counting down to the next boundary is only meaningful while the regular
  // session is running; outside it the useful number is the next open.
  const boundary = open ? 16 * 60 : minutes < 9 * 60 + 30 ? 9 * 60 + 30 : 24 * 60 + 9 * 60 + 30;
  const remain = Math.max(0, boundary * 60 - (minutes * 60 + Number(get("second"))));
  const countdown = [
    Math.floor(remain / 3600), Math.floor((remain % 3600) / 60), remain % 60,
  ].map((part) => String(part).padStart(2, "0")).join(":");
  return { open, label, countdown, weekend, known: true };
}

/* ── tiny charts (no dependency; 2px strokes, no frame) ────────────────── */

function Sparkline({
  values, stroke, fill, width = 96, height = 34,
}: {
  values: number[]; stroke: string; fill?: string; width?: number; height?: number;
}) {
  if (values.length < 2) return <svg className="te-spark" width={width} height={height} />;
  const min = Math.min(...values);
  const max = Math.max(...values);
  const span = max - min || 1;
  const step = width / (values.length - 1);
  const point = (value: number, index: number) =>
    `${index * step},${height - ((value - min) / span) * (height - 4) - 2}`;
  const line = values.map(point).join(" ");
  return (
    <svg className="te-spark" width={width} height={height} viewBox={`0 0 ${width} ${height}`}>
      {fill && (
        <polygon points={`0,${height} ${line} ${width},${height}`} fill={fill} />
      )}
      <polyline points={line} fill="none" stroke={stroke} strokeWidth={2}
        strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function EquityChart({ points }: { points: { date: string; value: number }[] }) {
  const width = 720;
  const height = 210;
  if (points.length < 2) {
    return <div className="te-note">Not enough data to draw this yet</div>;
  }
  const values = points.map((point) => point.value);
  const min = Math.min(...values, 0);
  const max = Math.max(...values, 0);
  const span = max - min || 1;
  const step = width / (points.length - 1);
  const coords = points.map((point, index) =>
    `${index * step},${height - ((point.value - min) / span) * (height - 20) - 10}`);
  const line = coords.join(" ");
  const gridlines = [0, 0.25, 0.5, 0.75, 1].map((ratio) => height - ratio * (height - 20) - 10);
  return (
    <svg width="100%" height={height} viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none">
      <defs>
        <linearGradient id="te-equity-fill" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="rgba(116,98,235,.15)" />
          <stop offset="100%" stopColor="rgba(116,98,235,0)" />
        </linearGradient>
      </defs>
      {gridlines.map((y) => (
        <line key={y} x1="0" x2={width} y1={y} y2={y} stroke="#efecef" strokeWidth="1" />
      ))}
      <polygon points={`0,${height} ${line} ${width},${height}`} fill="url(#te-equity-fill)" />
      <polyline points={line} fill="none" stroke="#7462eb" strokeWidth="2"
        strokeLinecap="round" strokeLinejoin="round" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

function Donut({ segments }: { segments: { value: number; color: string }[] }) {
  const size = 116;
  const stroke = 17;
  const radius = (size - stroke) / 2;
  const circumference = 2 * Math.PI * radius;
  const total = segments.reduce((sum, segment) => sum + segment.value, 0) || 1;
  let offset = 0;
  return (
    <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`}>
      <g transform={`rotate(-90 ${size / 2} ${size / 2})`}>
        {segments.map((segment, index) => {
          const length = (segment.value / total) * circumference;
          const element = (
            <circle key={index} cx={size / 2} cy={size / 2} r={radius}
              fill="none" stroke={segment.color} strokeWidth={stroke}
              strokeDasharray={`${length} ${circumference - length}`}
              strokeDashoffset={-offset} />
          );
          offset += length;
          return element;
        })}
      </g>
    </svg>
  );
}

/* ── icons (inline, 17px, 1.7 stroke) ──────────────────────────────────── */

const Icon = ({ d }: { d: string }) => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7"
    strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    {d.split("|").map((part, index) => <path key={index} d={part} />)}
  </svg>
);


/* ── data hook ─────────────────────────────────────────────────────────── */

function useJSON<T>(path: string | null, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(Boolean(path));
  useEffect(() => {
    if (!path) return;
    const controller = new AbortController();
    setLoading(true);
    fetch(`${API}${path}`, { signal: controller.signal })
      .then(async (response) => {
        const answer = await response.json();
        if (!response.ok) throw new Error(answer?.error ?? `HTTP ${response.status}`);
        setData((answer?.data ?? answer) as T);
        setError("");
      })
      .catch((cause) => {
        if (!controller.signal.aborted) {
          setError(cause instanceof Error ? cause.message : "load failed");
        }
      })
      .finally(() => setLoading(false));
    return () => controller.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path, ...deps]);
  return { data, error, loading };
}

/* ── shell ─────────────────────────────────────────────────────────────── */

// The views that exist. One list rather than a condition repeated per view: the
// placeholder and the screens were two places to edit, and a screen added to one and
// not the other renders both at once -- or neither, which is how the terminal came to
// be reachable by nothing.
const BUILT: readonly string[] = [
  "dashboard", "gainers", "scanner", "watchlist", "positions", "performance",
  "terminal",
];

export function TradeEdgeApp({ section }: { section: string }) {
  const router = useRouter();
  const view = (NAV_KEYS.includes(section)
    ? section : "scanner") as NavKey;
  // Navigation is a route change, not a state change: the URL is what survives
  // a refresh and what can be sent to someone else.
  const setView = useCallback((key: NavKey) => {
    // "/" is the hub now, so every rail item is its own route.
    router.push(`/${key}`);
  }, [router]);
  /* Null until the browser has mounted, and deliberately so -- see marketState. The
   * first read happens in the effect rather than waiting for the interval's first
   * tick, so the clock appears immediately rather than a second late. */
  const [now, setNow] = useState<Date | null>(null);
  const [focus, setFocus] = useState("");

  useEffect(() => {
    setNow(new Date());
    const timer = setInterval(() => setNow(new Date()), 1000);
    return () => clearInterval(timer);
  }, []);
  const session = useMemo(() => marketState(now), [now]);

  const { data: pnl } = useJSON<DailyPnl[]>("/execution/daily-pnl");
  const { data: watch } = useJSON<WatchItem[]>("/watchlist");
  const { data: positions } = useJSON<ExecutionPosition[]>("/execution/positions");

  const paper = useMemo(
    () => (pnl ?? []).filter((row) => row.mode === "paper")
      .slice().sort((a, b) => a.trading_date.localeCompare(b.trading_date)),
    [pnl],
  );

  return (
    <div className="te">
      <Sidebar
        view={view} onView={setView} session={session}
        counts={{ watchlist: watch?.length ?? 0, positions: positions?.length ?? 0 }}
      />
      <div className="te-main">
        <Header session={session} now={now} />
        <div className="te-content">
          {/* The dashboard branch went with the rail item. The hub answers what that
              screen answered, and better, so leaving a second version of it reachable
              by URL would only be a way of seeing worse numbers. */}
          {view === "gainers" && <GainersView />}
          {view === "scanner" && <ScannerView />}
          {view === "watchlist" && <WatchlistView items={watch ?? []} />}
          {view === "positions" && <PositionsView positions={positions ?? []} />}
          {view === "performance" && <PerformanceView paper={paper} />}
          {/* The terminal is the one screen that sends instructions about money, so it
            * brings its own surface rather than being themed like the rest of the
            * console. It was written and never mounted: the rail linked to a route that
            * rendered the not-built placeholder, so every field on it -- the exit
            * ladder, arming, the manual hold -- was unreachable. */}
          {view === "terminal" && <TerminalView />}
          {!BUILT.includes(view) && (
            <NotBuilt label={NAV.find((item) => item.key === view)?.label ?? ""} />
          )}
        </div>
      </div>
    </div>
  );
}

/* ── sidebar / header ──────────────────────────────────────────────────── */

function Sidebar({
  view, onView, session, counts,
}: {
  view: NavKey;
  onView: (key: NavKey) => void;
  session: ReturnType<typeof marketState>;
  counts: { watchlist: number; positions: number };
}) {
  return (
    <aside className="te-side">
      <div className="te-brand">
        <span className="te-brand-mark">
          <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor"
            strokeWidth="2.1" strokeLinecap="round" strokeLinejoin="round">
            <path d="M4 18V9M10 18V4M16 18v-6M22 18H2" />
          </svg>
        </span>
        <span>
          <b>TradeEdge</b>
          <small>Momentum Suite</small>
        </span>
      </div>

      <nav className="te-nav" aria-label="Primary">
        {NAV.map((item) => {
          const count =
            item.key === "watchlist" ? counts.watchlist
            : item.key === "positions" ? counts.positions
            : 0;
          return (
            <button key={item.key} className={view === item.key ? "active" : ""}
              onClick={() => onView(item.key)}
              aria-current={view === item.key ? "page" : undefined}>
              <Icon d={item.icon} />
              <span>{item.label}</span>
              {count > 0 && <span className="te-nav-count">{count}</span>}
            </button>
          );
        })}
      </nav>

      <div className={`te-market-card ${session.open ? "" : "closed"}`}>
        <b><i />{session.open ? "Market is Open" : session.label}</b>
        <small>
          {!session.known ? "Checking the clock…"
            : session.weekend ? "Reopens Monday"
            : session.open ? `Closes in ${session.countdown}`
            : `Opens in ${session.countdown}`}
        </small>
      </div>

      <div className="te-profile">
        <span className="te-avatar">Y</span>
        <span>
          <b>Yarnin</b>
          <small>Trader</small>
        </span>
        <span aria-hidden="true">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor"
            strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">
            <path d="M9 18l6-6-6-6" />
          </svg>
        </span>
      </div>
    </aside>
  );
}

function Header({
  session, now,
}: {
  session: ReturnType<typeof marketState>; now: Date | null;
}) {
  // "Welcome back" until there is a clock to say better. It is true at any hour,
  // which is the point: the alternative is picking one of the three and being wrong
  // for two thirds of the day on the first frame.
  const greeting = now
    ? (() => {
        const hour = Number(
          new Intl.DateTimeFormat("en-US", {
            timeZone: "Asia/Bangkok", hour: "2-digit", hour12: false,
          }).format(now),
        );
        return hour < 12 ? "Good morning" : hour < 18 ? "Good afternoon" : "Good evening";
      })()
    : "Welcome back";
  return (
    <header className="te-header">
      <div className="te-greet">
        <h1>{greeting}, Yarnin 👋</h1>
        <p>Here&apos;s what&apos;s happening in the market today.</p>
      </div>
      <div className="te-header-right">
        <div className={`te-clock ${session.open ? "" : "closed"}`}>
          <span className="te-clock-flag" aria-hidden="true">🇺🇸</span>
          <span>
            <b><i />{session.label}</b>
            <small>{now ? `${clock(now, "Asia/Bangkok")} ICT` : "--:--:-- ICT"}</small>
          </span>
        </div>
        <div className="te-search">
          <span className="te-search-icon" aria-hidden="true">
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor"
              strokeWidth="1.7" strokeLinecap="round">
              <circle cx="11" cy="11" r="7" /><path d="M20 20l-4-4" />
            </svg>
          </span>
          <input placeholder="Search anything..." aria-label="Search" />
          <kbd>⌘K</kbd>
        </div>
        <button className="te-icon-btn" aria-label="Notifications">
          <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor"
            strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">
            <path d="M18 8a6 6 0 1 0-12 0c0 7-3 8-3 8h18s-3-1-3-8M13.7 21a2 2 0 0 1-3.4 0" />
          </svg>
        </button>
      </div>
    </header>
  );
}

/* ── dashboard ─────────────────────────────────────────────────────────── */

function Dashboard({
  paper, watch, positions, focus, onFocus, onView,
}: {
  paper: DailyPnl[];
  watch: WatchItem[];
  positions: ExecutionPosition[];
  focus: string;
  onFocus: (ticker: string) => void;
  onView: (key: NavKey) => void;
}) {
  const { data: preMarket } = useJSON<GainersPayload>("/gainers?session=PRE_MARKET");
  const top = preMarket?.rows?.[0];

  const equity = useMemo(() => {
    let running = 0;
    return paper.map((row) => {
      running += row.net_pnl;
      return { date: row.trading_date, value: running };
    });
  }, [paper]);

  // Win rate is measured on days, not trades: the daily P&L roll-up is what
  // the platform actually stores, and pretending otherwise would put a
  // trade-level number on a day-level source.
  const wins = paper.filter((row) => row.net_pnl > 0).length;
  const losses = paper.filter((row) => row.net_pnl < 0).length;
  const winRate = wins + losses ? (wins / (wins + losses)) * 100 : 0;
  const grossWin = paper.filter((row) => row.net_pnl > 0)
    .reduce((sum, row) => sum + row.net_pnl, 0);
  const grossLoss = Math.abs(paper.filter((row) => row.net_pnl < 0)
    .reduce((sum, row) => sum + row.net_pnl, 0));
  const profitFactor = grossLoss ? grossWin / grossLoss : 0;
  const trades = paper.reduce((sum, row) => sum + row.transactions, 0);
  const netPnl = paper.reduce((sum, row) => sum + row.net_pnl, 0);

  return (
    <>
      <div className="te-kpis">
        <UnsourcedKpi label="Market Trend"
          why="no market-index endpoint yet (SPY / breadth)" />

        <div className="te-kpi">
          <span className="te-kpi-label">Top Gainer (Pre-Market)</span>
          {top ? (
            <>
              <div style={{ display: "flex", alignItems: "baseline", gap: 8, marginTop: 12 }}>
                <b className="te-kpi-ticker">{top.ticker}</b>
                <b className="te-kpi-pct">{pct(top.change_pct, 2)}</b>
              </div>
              <span style={{ fontSize: 11, color: "#625f61", marginTop: 4 }}>
                {money(top.close, top.close < 1 ? 4 : 2)}
              </span>
              <div className="te-kpi-foot">
                <span>Vol: {compact(top.volume)}</span>
                {top.float_rotation ? <span>rot {top.float_rotation.toFixed(1)}×</span> : null}
              </div>
            </>
          ) : <span className="te-kpi-value">—</span>}
        </div>

        <div className="te-kpi">
          <span className="te-kpi-label">Net P&amp;L (paper)</span>
          <b className={`te-kpi-value ${netPnl >= 0 ? "" : ""}`}>{money(netPnl)}</b>
          <div className="te-kpi-foot">
            <span className={netPnl >= 0 ? "te-up" : "te-down"}>
              {paper.length} trading days
            </span>
            <Sparkline values={equity.map((point) => point.value)}
              stroke={netPnl >= 0 ? "#4ac77d" : "#eb5a5a"}
              fill={netPnl >= 0 ? "rgba(74,199,125,.12)" : "rgba(235,90,90,.12)"} />
          </div>
        </div>

        <div className="te-kpi">
          <span className="te-kpi-label">Win Rate (by day)</span>
          <b className="te-kpi-value purple">{winRate.toFixed(1)}%</b>
          <div className="te-bar"><i style={{ width: `${winRate}%` }} /></div>
          <div className="te-kpi-foot"><span>{wins}W / {losses}L</span></div>
        </div>

        <div className="te-kpi">
          <span className="te-kpi-label">Total Transactions</span>
          <b className="te-kpi-value">{trades.toLocaleString()}</b>
          <div className="te-kpi-foot">
            <span>{paper.length} days recorded</span>
            <Sparkline values={paper.map((row) => row.transactions)} stroke="#b2aae9"
              fill="rgba(178,170,233,.16)" />
          </div>
        </div>

        <div className="te-kpi">
          <span className="te-kpi-label">Profit Factor</span>
          <b className="te-kpi-value">{profitFactor ? profitFactor.toFixed(2) : "—"}</b>
          <div className="te-kpi-foot">
            <span>gross {money(grossWin, 0)} / {money(grossLoss, 0)}</span>
            <Sparkline values={paper.map((row) => row.net_pnl)} stroke="#f2aa70" />
          </div>
        </div>
      </div>

      <div className="te-equity">
        <section className="te-card">
          <div className="te-card-head">
            <h2>Equity Curve</h2>
            <div className="te-legend te-spacer">
              <span><i style={{ background: "#7462eb" }} />Cumulative net P&amp;L</span>
            </div>
          </div>
          <div className="te-card-body"><EquityChart points={equity} /></div>
          <div className="te-equity-foot">
            <div><small>Days</small><b>{paper.length}</b></div>
            <div><small>Net P&amp;L</small>
              <b className={netPnl >= 0 ? "te-up" : "te-down"}>{money(netPnl)}</b></div>
            <div><small>Win days</small><b>{wins}</b></div>
            <div><small>Loss days</small><b>{losses}</b></div>
            <div><small>Profit factor</small>
              <b>{profitFactor ? profitFactor.toFixed(2) : "—"}</b></div>
          </div>
        </section>

        <section className="te-card">
          <div className="te-card-head">
            <h2>Watchlist</h2>
            <button className="te-ghost-btn te-spacer" onClick={() => onView("watchlist")}>
              + Add
            </button>
          </div>
          {watch.length === 0 ? (
            <div className="te-empty">
              <b>Nothing on the watchlist</b>
              <small>Add a ticker and the system follows its price and news</small>
            </div>
          ) : (
            <div className="te-table-scroll">
              <table className="te-table">
                <thead><tr><th>Symbol</th><th>Note</th></tr></thead>
                <tbody>
                  {watch.slice(0, 8).map((item) => (
                    <tr key={item.ticker} onClick={() => onFocus(item.ticker)}
                      className={focus === item.ticker ? "focus" : ""}>
                      <td className="te-sym">{item.ticker}</td>
                      <td style={{ color: "#7b7879" }}>{item.note || "—"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <button className="te-card-link" onClick={() => onView("watchlist")}>
            View all watchlist →
          </button>
        </section>
      </div>

      <div className="te-bottom">
        <ScannerCard compact onView={onView} />
        <PerformanceCard paper={paper} />
        <PortfolioCard positions={positions} onView={onView} />
      </div>
    </>
  );
}

function UnsourcedKpi({ label, why }: { label: string; why: string }) {
  // A dashboard that invents a number is worse than one with a gap: the gap is
  // the only honest prompt to go and build the source.
  return (
    <div className="te-kpi">
      <span className="te-kpi-label">{label}</span>
      <b className="te-kpi-value" style={{ color: "#c8c5c2" }}>—</b>
      <div className="te-kpi-foot">
        <span style={{ fontSize: 10, color: "#a8a5a2", lineHeight: 1.4 }}>{why}</span>
      </div>
    </div>
  );
}

/* ── gainers ───────────────────────────────────────────────────────────── */

const SESSION_TABS = [
  { key: "PRE_MARKET", label: "Pre-Market" },
  { key: "REGULAR", label: "Regular" },
  { key: "AFTER_HOURS", label: "After-Hours" },
] as const;

const STRUCTURAL = new Set([
  "EXTREME_ROTATION", "HIGH_ROTATION", "MICRO_FLOAT", "LOW_FLOAT",
  "EXTREME_RVOL", "HIGH_RVOL", "STRONG_CATALYST",
]);

type SortKey = "rank" | "change" | "best" | "giveback" | "volume" | "rvol"
  | "float" | "rotation";

// Giveback is the distance between the best print the session offered and
// where it finished. It is the number that separates a move worth entering
// from one that only looked good on the leaderboard.
const giveback = (row: GainerRow) => {
  if (!row.max_change_pct || row.max_change_pct <= row.change_pct) return 0;
  const high = 1 + row.max_change_pct / 100;
  const close = 1 + row.change_pct / 100;
  return ((high - close) / high) * 100;
};

const SESSION_META: Record<string, { label: string; clock: string }> = {
  PRE_MARKET: { label: "Pre-Market", clock: "04:00 – 09:30 ET" },
  REGULAR: { label: "Regular", clock: "09:30 – 16:00 ET" },
  AFTER_HOURS: { label: "After-Hours", clock: "16:00 – 20:00 ET" },
};

// Each tag is restated as a sentence carrying the row's own number. A chip
// reading "float turned over heavily" tells you a threshold was crossed; it does not
// tell you the float turned over eleven times, which is the fact worth having.
function explain(tag: string, row: GainerRow): string {
  const rotation = row.float_rotation?.toFixed(1);
  const rvol = row.relative_volume?.toFixed(1);
  const float = compact(row.float_shares);
  switch (tag) {
    case "EXTREME_ROTATION":
      return `The entire float changed hands ${rotation} times in one session — at that level people are genuinely chasing, not just marking the price`;
    case "HIGH_ROTATION":
      return `The float turned over ${rotation} times — past the 2× threshold measured to lift spike odds from 11% to 46%`;
    case "MICRO_FLOAT":
      return `A float of only ${float} shares — modest money moves the price, and it falls just as fast`;
    case "LOW_FLOAT":
      return `A float of ${float} shares is small, so the price reacts harder than usual`;
    case "EXTREME_RVOL":
      return `Volume ${rvol}× the 10-day average — clearly abnormal`;
    case "HIGH_RVOL":
      return `Volume ${rvol}× the average — more interest than usual`;
    case "STRONG_CATALYST":
      return `The news scored as a strong catalyst (${row.catalyst_score?.toFixed(2) ?? "—"})`;
    case "NEWS_CATALYST":
      return "There is news in the window, but not enough to score as a strong catalyst";
    case "NO_STORED_NEWS":
      return "No news in the database — a fact about our feed, not about the world";
    case "SUB_DOLLAR":
      return "Under $1 — in Nasdaq minimum-bid territory if it stays there long enough";
    case "NANO_CAP":
      return `Market cap ${money(row.market_cap, 0)} — small enough that modest buying moves it`;
    case "FADED_FROM_HIGH":
      return `Gave back ${giveback(row).toFixed(0)}% from the session high — anyone chasing the top was down before the close`;
    case "CLOSED_AT_HIGH":
      return "Closed the session near its high — the buying held to the end rather than being unloaded";
    case "NEW_52W_HIGH":
      return "A new 52-week high — nobody is trapped above this price";
    case "NEAR_52W_LOW":
      return "Near the 52-week low — below it there is no past support to refer to";
    default:
      return tag;
  }
}

/* ── gainers: a day, then a name ───────────────────────────────────────── */

function GainersView() {
  const router = useRouter();
  const params = useSearchParams();
  // Day, session and the opened name all live in the URL: a refresh has to
  // land back on what was being read, and a screen worth showing someone is a
  // screen worth linking to.
  const day = params.get("date") ?? "";
  const session = params.get("session") ?? "REGULAR";
  const detail = params.get("ticker");

  const setParam = useCallback((patch: Record<string, string | null>) => {
    const next = new URLSearchParams(params.toString());
    for (const [key, value] of Object.entries(patch)) {
      if (value) next.set(key, value); else next.delete(key);
    }
    const query = next.toString();
    router.replace(query ? `/gainers?${query}` : "/gainers", { scroll: false });
  }, [params, router]);

  const pre = useJSON<GainersPayload>(
    `/gainers?session=PRE_MARKET${day ? `&date=${day}` : ""}`);
  const reg = useJSON<GainersPayload>(
    `/gainers?session=REGULAR${day ? `&date=${day}` : ""}`);
  const ah = useJSON<GainersPayload>(
    `/gainers?session=AFTER_HOURS${day ? `&date=${day}` : ""}`);

  const loading = pre.loading || reg.loading || ah.loading;
  const dates = reg.data?.dates ?? pre.data?.dates ?? ah.data?.dates ?? [];
  const tradingDate = reg.data?.trading_date || pre.data?.trading_date
    || ah.data?.trading_date || "";

  const sessions = useMemo(() => [
    { key: "PRE_MARKET", payload: pre.data },
    { key: "REGULAR", payload: reg.data },
    { key: "AFTER_HOURS", payload: ah.data },
  ], [pre.data, reg.data, ah.data]);

  if (detail) {
    return (
      <GainerDetail
        ticker={detail}
        tradingDate={tradingDate}
        sessions={sessions}
        onBack={() => setParam({ ticker: null })}
      />
    );
  }

  const activePayload = sessions.find((entry) => entry.key === session)?.payload ?? null;

  return (
    <>
      <section className="te-card">
        <div className="te-card-head">
          <h2>Gainers</h2>
          <div className="te-spacer te-datepick">
            <label>
              <span>Date</span>
              <input
                type="date"
                value={day || tradingDate}
                min={dates.length ? dates[dates.length - 1] : undefined}
                max={dates.length ? dates[0] : undefined}
                onChange={(event) => setParam({ date: event.target.value || null })}
              />
            </label>
            {/* A day with no capture is not an error, but it is worth saying
                out loud rather than leaving three empty tabs to imply the
                market was quiet. */}
            {day && !dates.includes(day) && (
              <em className="te-datepick-warn">nothing stored for this date</em>
            )}
            {day && (
              <button className="te-ghost-btn" onClick={() => setParam({ date: null })}>
                Latest
              </button>
            )}
            <span className="te-datepick-count">{dates.length} dates stored</span>
          </div>
        </div>
      </section>

      <section className="te-card">
        <div className="te-tabs">
          {sessions.map(({ key, payload }) => {
            const meta = SESSION_META[key];
            return (
              <button key={key} className={session === key ? "active" : ""}
                onClick={() => setParam({ session: key })}>
                {meta.label}
                <i>{payload?.rows?.length ? `${payload.rows.length}` : "0"}</i>
              </button>
            );
          })}
          <span className="te-tab-note">{SESSION_META[session]?.clock}</span>
        </div>

        {loading && <p className="te-note">Loading…</p>}
        {!loading && (
          <SessionTable sessionKey={session} payload={activePayload}
            onOpen={(ticker) => setParam({ ticker })} />
        )}
      </section>
    </>
  );
}

function SessionTable({
  sessionKey, payload, onOpen,
}: {
  sessionKey: string;
  payload: GainersPayload | null;
  onOpen: (ticker: string) => void;
}) {
  const [sort, setSort] = useState<{ key: SortKey; desc: boolean }>({
    key: "rank", desc: true,
  });
  const all = payload?.rows ?? [];

  const rows = useMemo(() => {
    const pick = (row: GainerRow) => {
      switch (sort.key) {
        case "change": return row.change_pct;
        case "best": return row.max_change_pct ?? 0;
        case "giveback": return giveback(row);
        case "volume": return row.volume ?? 0;
        case "rvol": return row.relative_volume ?? 0;
        case "float": return row.float_shares ?? 0;
        case "rotation": return row.float_rotation ?? 0;
        default: return -row.rank;
      }
    };
    return all.slice().sort((a, b) => sort.desc ? pick(b) - pick(a) : pick(a) - pick(b));
  }, [all, sort]);

  const sortBy = useCallback((key: SortKey) => setSort((current) =>
    current.key === key ? { key, desc: !current.desc } : { key, desc: true }), []);

  const rotated = all.filter((row) => (row.float_rotation ?? 0) >= 2).length;
  const held = all.filter((row) => giveback(row) <= 15).length;
  const silent = all.filter((row) => !row.has_news).length;

  return (
    <>
      <div className="te-stats">
        <div><small>Ranked</small><b>{all.length}</b></div>
        <div><small>Rotation ≥2×</small>
          <b style={{ color: rotated ? "#6e5ce7" : undefined }}>{rotated}</b></div>
        <div><small>Held the move</small>
          <b style={{ color: held ? "#35b06b" : undefined }}>{held}</b></div>
        <div><small>No stored news</small><b>{silent}</b></div>
      </div>

      {payload?.note && (
        <div className="te-empty"><b>No data</b><small>{payload.note}</small></div>
      )}

      {rows.length > 0 && (
        <div className="te-table-scroll">
          <table className="te-table te-sortable">
            <thead>
              <tr>
                <SortHead label="#" col="rank" sort={sort} onSort={sortBy} plain />
                <th>Symbol</th>
                <th className="num">Close</th>
                <SortHead label="Change %" col="change" sort={sort} onSort={sortBy} />
                <SortHead label="Best" col="best" sort={sort} onSort={sortBy} />
                <SortHead label="Giveback" col="giveback" sort={sort} onSort={sortBy} />
                <SortHead label="RVol" col="rvol" sort={sort} onSort={sortBy} />
                <SortHead label="Float" col="float" sort={sort} onSort={sortBy} />
                <SortHead label="Rotation" col="rotation" sort={sort} onSort={sortBy} />
                <th />
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => {
                const gave = giveback(row);
                return (
                  <tr key={row.ticker} onClick={() => onOpen(row.ticker)}>
                    <td style={{ color: "#abacaf" }}>{row.rank}</td>
                    <td>
                      <div className="te-sym">{row.ticker}</div>
                      {row.news_title && (
                        <div className="te-row-news" title={row.news_title}>
                          {row.news_title}
                        </div>
                      )}
                    </td>
                    <td className="num">{money(row.close, row.close < 1 ? 4 : 2)}</td>
                    <td className="num te-up">{pct(row.change_pct, 1)}</td>
                    <td className="num" style={{ color: "#7462eb" }}>
                      {row.max_change_pct ? pct(row.max_change_pct, 1) : "—"}
                    </td>
                    <td className="num">
                      {gave > 0
                        // Red only past a third given back: some fade is normal,
                        // and colouring all of it would make the column noise.
                        ? <span style={{ color: gave >= 33 ? "#eb5a5a" : "#9b989a" }}>
                            −{gave.toFixed(0)}%
                          </span>
                        : <span style={{ color: "#35b06b" }}>held</span>}
                    </td>
                    <td className="num">
                      {row.relative_volume ? `${row.relative_volume.toFixed(1)}×` : "—"}
                    </td>
                    <td className="num">{compact(row.float_shares)}</td>
                    <td className="num">
                      {row.float_rotation ? (
                        <span className={`te-badge ${row.float_rotation >= 2 ? "purple" : "low"}`}>
                          {row.float_rotation.toFixed(1)}×
                        </span>
                      ) : "—"}
                    </td>
                    <td className="te-open-cell">Open →</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

/* ── detail: one name, one day, all three sessions ─────────────────────── */

function GainerDetail({
  ticker, tradingDate, sessions, onBack,
}: {
  ticker: string;
  tradingDate: string;
  sessions: { key: string; payload: GainersPayload | null }[];
  onBack: () => void;
}) {
  // The same name is looked up in every session so the day reads as one story:
  // a ticker that ran in pre-market and was sold through the regular session is
  // a different lesson from one that built all day.
  const appearances = sessions
    .map(({ key, payload }) => ({
      key,
      row: payload?.rows?.find((row) => row.ticker === ticker),
    }))
    .filter((entry): entry is { key: string; row: GainerRow } => Boolean(entry.row));

  const primary = appearances.find((entry) => entry.key === "REGULAR")?.row
    ?? appearances[0]?.row;

  if (!primary) {
    return (
      <section className="te-card">
        <div className="te-detail-head">
          <button className="te-back" onClick={onBack}>← Back</button>
        </div>
        <div className="te-empty"><b>{ticker}</b><small>nothing found for this date</small></div>
      </section>
    );
  }

  return (
    <>
      <section className="te-card">
        <div className="te-detail-head">
          <button className="te-back" onClick={onBack}>← Back</button>
          <div className="te-detail-title">
            <h2>{ticker}</h2>
            <small>{tradingDate}</small>
          </div>
          <div className="te-detail-key">
            <span><small>float</small><b>{compact(primary.float_shares)}</b></span>
            <span><small>mcap</small><b>{money(primary.market_cap, 0)}</b></span>
            <span><small>appears in</small><b>{appearances.length} sessions</b></span>
          </div>
        </div>

        <div className="te-journey">
          {["PRE_MARKET", "REGULAR", "AFTER_HOURS"].map((key) => {
            const entry = appearances.find((item) => item.key === key);
            const meta = SESSION_META[key];
            if (!entry) {
              return (
                <div className="te-journey-cell muted" key={key}>
                  <small>{meta.label}</small>
                  <b>—</b>
                  <em>unranked</em>
                </div>
              );
            }
            const gave = giveback(entry.row);
            return (
              <div className="te-journey-cell" key={key}>
                <small>{meta.label}</small>
                <b className="te-up">{pct(entry.row.change_pct, 1)}</b>
                <em>
                  {money(entry.row.reference_price, entry.row.reference_price < 1 ? 4 : 2)}
                  {" → "}
                  {money(entry.row.close, entry.row.close < 1 ? 4 : 2)}
                </em>
                <span className="te-journey-meta">
                  best {entry.row.max_change_pct ? pct(entry.row.max_change_pct, 1) : "—"}
                  {gave > 0 ? ` · gave back ${gave.toFixed(0)}%` : " · closed near the high"}
                </span>
                <span className="te-journey-meta">
                  rank #{entry.row.rank}
                  {entry.row.float_rotation
                    ? ` · turned ${entry.row.float_rotation.toFixed(1)}×` : ""}
                </span>
              </div>
            );
          })}
        </div>
      </section>

      <div className="te-detail-grid">
        <section className="te-card">
          <div className="te-card-head"><h2>Why it moved</h2></div>
          <div className="te-reasons">
            {appearances.map(({ key, row }) => (
              <div className="te-reason-group" key={key}>
                <span className="te-reason-session">{SESSION_META[key].label}</span>
                {row.reasons.map((reason) => (
                  <div className="te-reason" key={reason.tag}>
                    <b className={STRUCTURAL.has(reason.tag) ? "structural" : ""}>
                      {reason.label}
                    </b>
                    <p>{explain(reason.tag, row)}</p>
                  </div>
                ))}
              </div>
            ))}
          </div>
        </section>

        <div style={{ display: "grid", gap: "var(--te-gap)", alignContent: "start" }}>
          <section className="te-card">
            <div className="te-card-head"><h2>News tied to the move</h2></div>
            {primary.has_news && primary.news_title ? (
              <div className="te-card-body">
                <p className="te-news-title">{primary.news_title}</p>
                <div className="te-news-meta">
                  {primary.news_published_at && (
                    <span>published {new Date(primary.news_published_at)
                      .toLocaleString("th-TH", { timeZone: "Asia/Bangkok" })}</span>
                  )}
                  {primary.catalyst_score ? (
                    <span>catalyst score {primary.catalyst_score.toFixed(2)}</span>
                  ) : null}
                </div>
                <p className="te-news-caveat">
                  News is matched by time window, which is not proof of cause —
                  the system only picks the highest-scoring item inside that window.
                </p>
              </div>
            ) : (
              <div className="te-empty">
                <b>No news in the database</b>
                <small>
                  This is a fact about our feed, not about the world —
                  names with no news but exploding volume measured a higher hit rate than the ones with news.
                </small>
              </div>
            )}
          </section>

          <section className="te-card">
            <div className="te-card-head"><h2>Every number</h2></div>
            <table className="te-table">
              <thead>
                <tr>
                  <th>Session</th><th className="num">Ref</th><th className="num">High</th>
                  <th className="num">Close</th><th className="num">Vol</th>
                  <th className="num">RVol</th><th className="num">Rot</th>
                </tr>
              </thead>
              <tbody>
                {appearances.map(({ key, row }) => (
                  <tr key={key}>
                    <td>{SESSION_META[key].label}</td>
                    <td className="num">{money(row.reference_price, row.close < 1 ? 4 : 2)}</td>
                    <td className="num">{money(row.high, row.close < 1 ? 4 : 2)}</td>
                    <td className="num">{money(row.close, row.close < 1 ? 4 : 2)}</td>
                    <td className="num">{compact(row.volume)}</td>
                    <td className="num">
                      {row.relative_volume ? `${row.relative_volume.toFixed(1)}×` : "—"}
                    </td>
                    <td className="num">
                      {row.float_rotation ? `${row.float_rotation.toFixed(1)}×` : "—"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <p className="te-news-caveat" style={{ padding: "0 20px 16px" }}>
              Ref is the price each session's move is measured from — pre-market and regular
              measure from the prior close, after-hours from that day's regular close.
            </p>
          </section>
        </div>
      </div>
    </>
  );
}

function SortHead({
  label, col, sort, onSort, plain,
}: {
  label: string;
  col: SortKey;
  sort: { key: SortKey; desc: boolean };
  onSort: (key: SortKey) => void;
  plain?: boolean;
}) {
  const on = sort.key === col;
  return (
    <th className={plain ? "" : "num"}>
      <button className={`te-sort ${on ? "on" : ""}`} onClick={() => onSort(col)}>
        {label}
        <i aria-hidden="true">{on ? (sort.desc ? "\u25be" : "\u25b4") : ""}</i>
      </button>
    </th>
  );
}


/* ── scanner ───────────────────────────────────────────────────────────── */

function ScannerCard({ compact: isCompact, onView }: { compact?: boolean; onView: (key: NavKey) => void }) {
  const { data } = useJSON<GainersPayload>("/gainers?session=REGULAR");
  const rows = (data?.rows ?? []).slice(0, isCompact ? 5 : 30);
  return (
    <section className="te-card">
      <div className="te-card-head"><h2>Daily Scanner</h2></div>
      {rows.length === 0 ? (
        <div className="te-empty"><b>No data yet</b><small>waiting for the next collection</small></div>
      ) : (
        <div className="te-table-scroll">
          <table className="te-table">
            <thead>
              <tr><th>#</th><th>Symbol</th><th className="num">Price</th>
                <th className="num">Change %</th><th className="num">Volume</th>
                <th className="num">Float</th></tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.ticker}>
                  <td style={{ color: "#abacaf" }}>{row.rank}</td>
                  <td className="te-sym">{row.ticker}</td>
                  <td className="num">{money(row.close, row.close < 1 ? 4 : 2)}</td>
                  <td className="num te-up">{pct(row.change_pct, 1)}</td>
                  <td className="num">{compact(row.volume)}</td>
                  <td className="num">{compact(row.float_shares)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <button className="te-card-link" onClick={() => onView("gainers")}>View full scanner →</button>
    </section>
  );
}

function ScannerView() {
  const { data, loading } = useJSON<Candidate[]>("/candidates");
  const rows = data ?? [];
  return (
    <section className="te-card">
      <div className="te-card-head"><h2>Daily Scanner — live candidates</h2></div>
      {loading && <p className="te-note">Loading…</p>}
      {!loading && rows.length === 0 && (
        <div className="te-empty"><b>No candidates right now</b>
          <small>the ranker only runs while the market is open</small></div>
      )}
      {rows.length > 0 && (
        <div className="te-table-scroll">
          <table className="te-table">
            <thead><tr><th>Symbol</th><th className="num">Price</th>
              <th className="num">Change %</th><th className="num">Volume</th>
              <th className="num">Score</th></tr></thead>
            <tbody>
              {rows.slice(0, 40).map((row) => (
                <tr key={row.ticker}>
                  <td className="te-sym">{row.ticker}</td>
                  <td className="num">{money(row.price)}</td>
                  <td className={`num ${(row.change_ratio ?? 0) >= 0 ? "te-up" : "te-down"}`}>
                    {pct((row.change_ratio ?? 0) * 100, 1)}
                  </td>
                  <td className="num">{compact(row.volume)}</td>
                  <td className="num">
                    <span className={`te-badge ${(row.score ?? 0) >= 80 ? "high"
                      : (row.score ?? 0) >= 60 ? "mid" : "low"}`}>
                      {(row.score ?? 0).toFixed(0)}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

/* ── other views ───────────────────────────────────────────────────────── */

function WatchlistView({ items }: { items: WatchItem[] }) {
  return (
    <section className="te-card">
      <div className="te-card-head"><h2>Watchlist</h2></div>
      {items.length === 0 ? (
        <div className="te-empty"><b>Nothing here yet</b>
          <small>add a ticker to follow its price and news</small></div>
      ) : (
        <table className="te-table">
          <thead><tr><th>Symbol</th><th>Note</th></tr></thead>
          <tbody>
            {items.map((item) => (
              <tr key={item.ticker}>
                <td className="te-sym">{item.ticker}</td>
                <td style={{ color: "#7b7879" }}>{item.note || "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

function PositionsView({ positions }: { positions: ExecutionPosition[] }) {
  return (
    <section className="te-card">
      <div className="te-card-head"><h2>Positions</h2></div>
      {positions.length === 0 ? (
        <div className="te-empty"><b>No open positions</b><small>everything is closed</small></div>
      ) : (
        <table className="te-table">
          <thead><tr><th>Symbol</th><th className="num">Shares</th>
            <th className="num">Avg Price</th><th className="num">Market Value</th>
            <th className="num">P&amp;L</th></tr></thead>
          <tbody>
            {positions.map((row) => (
              <tr key={row.ticker}>
                <td className="te-sym">{row.ticker}</td>
                <td className="num">{row.quantity}</td>
                <td className="num">{money(row.average_price)}</td>
                <td className="num">{money(row.market_value)}</td>
                <td className={`num ${(row.unrealized_pnl ?? 0) >= 0 ? "te-up" : "te-down"}`}>
                  {money(row.unrealized_pnl)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

function PerformanceCard({ paper }: { paper: DailyPnl[] }) {
  const wins = paper.filter((row) => row.net_pnl > 0);
  const losses = paper.filter((row) => row.net_pnl < 0);
  const flat = paper.length - wins.length - losses.length;
  const net = paper.reduce((sum, row) => sum + row.net_pnl, 0);
  const fees = paper.reduce((sum, row) => sum + row.fees, 0);
  const best = paper.reduce((top, row) => (row.net_pnl > (top?.net_pnl ?? -Infinity) ? row : top),
    undefined as DailyPnl | undefined);
  const worst = paper.reduce((low, row) => (row.net_pnl < (low?.net_pnl ?? Infinity) ? row : low),
    undefined as DailyPnl | undefined);
  return (
    <section className="te-card">
      <div className="te-card-head"><h2>Performance Summary</h2></div>
      <div className="te-card-body te-perf">
        <div className="te-metrics">
          <div><small>Net P&amp;L</small>
            <b className={net >= 0 ? "te-up" : "te-down"}>{money(net)}</b></div>
          <div><small>Fees paid</small><b>{money(fees)}</b></div>
          <div><small>Win days</small><b>{wins.length}</b></div>
          <div><small>Loss days</small><b>{losses.length}</b></div>
          <div><small>Best day</small>
            <b className="te-up">{money(best?.net_pnl)}</b></div>
          <div><small>Worst day</small>
            <b className="te-down">{money(worst?.net_pnl)}</b></div>
        </div>
        <div className="te-donut-wrap">
          <Donut segments={[
            { value: wins.length, color: "#7ad096" },
            { value: losses.length, color: "#f1b985" },
            { value: Math.max(flat, 0), color: "#efe4c6" },
          ]} />
          <div className="te-donut-center">
            <b>{paper.length ? ((wins.length / paper.length) * 100).toFixed(1) : "—"}%</b>
            <small>Win days</small>
          </div>
        </div>
      </div>
    </section>
  );
}

function PerformanceView({ paper }: { paper: DailyPnl[] }) {
  return (
    <>
      <PerformanceCard paper={paper} />
      <section className="te-card">
        <div className="te-card-head"><h2>Daily P&amp;L</h2></div>
        <div className="te-table-scroll">
          <table className="te-table">
            <thead><tr><th>Date</th><th className="num">Gross</th><th className="num">Fees</th>
              <th className="num">Net</th><th className="num">Entries</th>
              <th className="num">Exits</th></tr></thead>
            <tbody>
              {paper.slice().reverse().map((row) => (
                <tr key={row.trading_date}>
                  <td>{row.trading_date}</td>
                  <td className="num">{money(row.gross_pnl)}</td>
                  <td className="num">{money(row.fees)}</td>
                  <td className={`num ${row.net_pnl >= 0 ? "te-up" : "te-down"}`}>
                    {money(row.net_pnl)}
                  </td>
                  <td className="num">{row.entries}</td>
                  <td className="num">{row.exits}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </>
  );
}

function PortfolioCard({
  positions, onView,
}: { positions: ExecutionPosition[]; onView: (key: NavKey) => void }) {
  const value = positions.reduce((sum, row) => sum + (row.market_value ?? 0), 0);
  const pnlTotal = positions.reduce((sum, row) => sum + (row.unrealized_pnl ?? 0), 0);
  return (
    <section className="te-card">
      <div className="te-card-head"><h2>Portfolio Overview</h2></div>
      <div className="te-card-body">
        <b style={{ fontSize: 23, fontWeight: 700, letterSpacing: "-.02em" }}>{money(value)}</b>
        <div style={{ fontSize: 11, marginTop: 4 }}
          className={pnlTotal >= 0 ? "te-up" : "te-down"}>
          {money(pnlTotal)} unrealised
        </div>
      </div>
      <div className="te-stats">
        <div><small>Positions</small><b>{positions.length}</b></div>
        <div><small>Market value</small><b>{money(value, 0)}</b></div>
        <div><small>Unrealised</small>
          <b className={pnlTotal >= 0 ? "te-up" : "te-down"}>{money(pnlTotal, 0)}</b></div>
      </div>
      {positions.length > 0 && (
        <table className="te-table">
          <thead><tr><th>Symbol</th><th className="num">Shares</th>
            <th className="num">Avg</th><th className="num">Value</th></tr></thead>
          <tbody>
            {positions.slice(0, 4).map((row) => (
              <tr key={row.ticker}>
                <td className="te-sym">{row.ticker}</td>
                <td className="num">{row.quantity}</td>
                <td className="num">{money(row.average_price)}</td>
                <td className="num">{money(row.market_value)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <button className="te-card-link" onClick={() => onView("positions")}>
        View full positions →
      </button>
    </section>
  );
}

function NotBuilt({ label }: { label: string }) {
  return (
    <section className="te-card">
      <div className="te-empty">
        <b>{label}</b>
        <small>
          This screen is not wired to real data yet, and no invented numbers are shown
          — a dashboard that makes figures up is more dangerous than one with a gap in it.
        </small>
      </div>
    </section>
  );
}
