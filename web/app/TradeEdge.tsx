"use client";

import { useCallback, useEffect, useMemo, useState } from "react";

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

function marketState(now: Date) {
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
  return { open, label, countdown, weekend };
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
    return <div className="te-note">ยังไม่มีข้อมูลพอสำหรับวาดกราฟ</div>;
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

const NAV = [
  { key: "dashboard", label: "Dashboard", icon: "M3 12h5l2 6 4-14 2 8h5" },
  { key: "market", label: "Market Overview", icon: "M3 3v18h18|M7 14l3-4 3 3 5-7" },
  { key: "scanner", label: "Daily Scanner", icon: "M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14z|M20 20l-4-4" },
  { key: "gainers", label: "Gainers", icon: "M3 17l6-6 4 4 8-8|M15 7h6v6" },
  { key: "watchlist", label: "Watchlist", icon: "M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7z|M12 14a2 2 0 1 0 0-4 2 2 0 0 0 0 4z" },
  { key: "positions", label: "Positions", icon: "M3 7h18v12H3z|M3 7l3-4h12l3 4" },
  { key: "orders", label: "Orders", icon: "M8 6h12|M8 12h12|M8 18h12|M3 6h.01|M3 12h.01|M3 18h.01" },
  { key: "performance", label: "Performance", icon: "M4 19V9|M10 19V5|M16 19v-7|M22 19H2" },
  { key: "backtest", label: "Backtest", icon: "M4 4v6h6|M4 10a8 8 0 1 1 2 5" },
  { key: "analytics", label: "Analytics", icon: "M12 3a9 9 0 1 0 9 9h-9z|M14 3.5A9 9 0 0 1 20.5 10H14z" },
  { key: "alerts", label: "Alerts", icon: "M18 8a6 6 0 1 0-12 0c0 7-3 8-3 8h18s-3-1-3-8|M13.7 21a2 2 0 0 1-3.4 0" },
  { key: "news", label: "News & Catalyst", icon: "M4 4h13v16H4z|M17 8h3v9a3 3 0 0 1-3 3|M7 8h7|M7 12h7|M7 16h4" },
  { key: "journal", label: "Trading Journal", icon: "M4 4h14v16H4z|M8 4v16|M11 9h4|M11 13h4" },
  { key: "settings", label: "Settings", icon: "M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z|M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-2.9 1.2 2 2 0 1 1-4 0 1.7 1.7 0 0 0-2.9-1.2l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1A1.7 1.7 0 0 0 4.6 15a2 2 0 1 1 0-4 1.7 1.7 0 0 0 1.2-2.9l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1A1.7 1.7 0 0 0 11.5 4a2 2 0 1 1 4 0 1.7 1.7 0 0 0 2.9 1.2l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0 1.2 2.9 2 2 0 1 1 0 4z" },
] as const;

type NavKey = (typeof NAV)[number]["key"];

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

export function TradeEdge() {
  const [view, setView] = useState<NavKey>("dashboard");
  const [now, setNow] = useState(() => new Date());
  const [focus, setFocus] = useState("");

  useEffect(() => {
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
          {view === "dashboard" && (
            <Dashboard paper={paper} watch={watch ?? []} positions={positions ?? []}
              onFocus={setFocus} focus={focus} onView={setView} />
          )}
          {view === "gainers" && <GainersView />}
          {view === "scanner" && <ScannerView />}
          {view === "watchlist" && <WatchlistView items={watch ?? []} />}
          {view === "positions" && <PositionsView positions={positions ?? []} />}
          {view === "performance" && <PerformanceView paper={paper} />}
          {!["dashboard", "gainers", "scanner", "watchlist", "positions", "performance"]
            .includes(view) && <NotBuilt label={NAV.find((item) => item.key === view)?.label ?? ""} />}
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
          {session.weekend ? "Reopens Monday"
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

function Header({ session, now }: { session: ReturnType<typeof marketState>; now: Date }) {
  const hour = Number(
    new Intl.DateTimeFormat("en-US", { timeZone: "Asia/Bangkok", hour: "2-digit", hour12: false })
      .format(now),
  );
  const greeting = hour < 12 ? "Good morning" : hour < 18 ? "Good afternoon" : "Good evening";
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
            <small>{clock(now, "Asia/Bangkok")} ICT</small>
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
          why="ยังไม่มี endpoint ดัชนีตลาด (SPY / breadth)" />

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
              <b>ยังไม่มีรายการเฝ้าดู</b>
              <small>เพิ่ม ticker เพื่อให้ระบบติดตามราคาและข่าวให้</small>
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

function GainersView() {
  const [session, setSession] = useState<string>("REGULAR");
  const [day, setDay] = useState("");
  const [active, setActive] = useState<Set<string>>(new Set());
  const [sort, setSort] = useState<{ key: SortKey; desc: boolean }>({
    key: "rank", desc: true,
  });
  const query = `/gainers?session=${session}${day ? `&date=${day}` : ""}`;
  const { data, error, loading } = useJSON<GainersPayload>(query);

  const tags = useMemo(() => {
    const counts = new Map<string, { label: string; count: number }>();
    for (const row of data?.rows ?? []) {
      for (const reason of row.reasons) {
        const entry = counts.get(reason.tag);
        if (entry) entry.count += 1;
        else counts.set(reason.tag, { label: reason.label, count: 1 });
      }
    }
    return [...counts.entries()].sort((a, b) => b[1].count - a[1].count);
  }, [data]);

  const rows = useMemo(() => {
    const all = data?.rows ?? [];
    const filtered = !active.size ? all : all.filter((row) => {
      // Every selected tag must be present: narrowing is the point, and a name
      // that merely gapped is not the same as one that gapped on a micro float.
      const present = new Set(row.reasons.map((reason) => reason.tag));
      return [...active].every((tag) => present.has(tag));
    });
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
    return filtered.slice().sort((a, b) =>
      sort.desc ? pick(b) - pick(a) : pick(a) - pick(b));
  }, [data, active, sort]);

  const toggle = useCallback((tag: string) => setActive((current) => {
    const next = new Set(current);
    if (next.has(tag)) next.delete(tag); else next.add(tag);
    return next;
  }), []);

  const sortBy = useCallback((key: SortKey) => setSort((current) =>
    current.key === key ? { key, desc: !current.desc } : { key, desc: true }), []);

  // The character of the session, which is the question the history exists to
  // answer: did the movers rotate their float, and did they hold the move?
  const held = rows.filter((row) => giveback(row) <= 15).length;
  const rotated = rows.filter((row) => (row.float_rotation ?? 0) >= 2).length;
  const silent = rows.filter((row) => !row.has_news).length;

  return (
    <section className="te-card">
      <div className="te-card-head">
        <h2>Session Gainers</h2>
        <div className="te-spacer" style={{ display: "flex", gap: 8, alignItems: "center" }}>
          <span style={{ fontSize: 10, color: "#898588" }}>
            {session === "AFTER_HOURS" ? "measured from regular close" : "measured from prior close"}
          </span>
          <select className="te-select" value={day} onChange={(event) => setDay(event.target.value)}
            aria-label="Trading date">
            <option value="">Latest</option>
            {(data?.dates ?? []).map((value) => <option key={value} value={value}>{value}</option>)}
          </select>
        </div>
      </div>

      <div className="te-tabs">
        {SESSION_TABS.map((tab) => (
          <button key={tab.key} className={session === tab.key ? "active" : ""}
            onClick={() => { setSession(tab.key); setActive(new Set()); }}>
            {tab.label}
          </button>
        ))}
      </div>

      {tags.length > 0 && (
        <div className="te-filters">
          <span className="te-filters-label">
            กรองตามเหตุผล — เลือกได้หลายอัน (ต้องมีครบทุกอันที่เลือก)
          </span>
          <div className="te-filters-row">
            {tags.map(([tag, entry]) => (
              <button key={tag}
                className={`${active.has(tag) ? "active" : ""} ${STRUCTURAL.has(tag) ? "structural" : ""}`}
                onClick={() => toggle(tag)}>
                {entry.label}<b>{entry.count}</b>
              </button>
            ))}
            {active.size > 0 && (
              <button onClick={() => setActive(new Set())} style={{ color: "#eb5a5a" }}>Clear</button>
            )}
          </div>
        </div>
      )}

      {error && <p className="te-note error">{error}</p>}
      {loading && <p className="te-note">Loading…</p>}
      {!loading && data?.note && (
        <div className="te-empty"><b>{data.trading_date || "—"}</b><small>{data.note}</small></div>
      )}

      {rows.length > 0 && (
        <>
          <div className="te-stats">
            <div><small>Ranked</small><b>{rows.length}</b></div>
            <div><small>Rotation ≥2×</small>
              <b style={{ color: rotated ? "#6e5ce7" : undefined }}>{rotated}</b></div>
            <div><small>Held the move</small>
              <b style={{ color: held ? "#35b06b" : undefined }}>{held}</b></div>
            <div><small>No stored news</small><b>{silent}</b></div>
          </div>
          <div className="te-table-scroll">
            <table className="te-table te-sortable">
              <thead>
                <tr>
                  <SortHead label="Rank" col="rank" sort={sort} onSort={sortBy} plain />
                  <th>Symbol</th>
                  <th className="num">Close</th>
                  <SortHead label="Change %" col="change" sort={sort} onSort={sortBy} />
                  <SortHead label="Best" col="best" sort={sort} onSort={sortBy} />
                  <SortHead label="Giveback" col="giveback" sort={sort} onSort={sortBy} />
                  <SortHead label="Volume" col="volume" sort={sort} onSort={sortBy} />
                  <SortHead label="RVol" col="rvol" sort={sort} onSort={sortBy} />
                  <SortHead label="Float" col="float" sort={sort} onSort={sortBy} />
                  <SortHead label="Rotation" col="rotation" sort={sort} onSort={sortBy} />
                  <th>Why it ranked</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => {
                  const gave = giveback(row);
                  return (
                    <tr key={row.ticker}>
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
                        {gave > 0 ? (
                          // Red only past a third given back: some fade is normal,
                          // and colouring all of it would make the column useless.
                          <span style={{ color: gave >= 33 ? "#eb5a5a" : "#9b989a" }}>
                            −{gave.toFixed(0)}%
                          </span>
                        ) : <span style={{ color: "#35b06b" }}>held</span>}
                      </td>
                      <td className="num">{compact(row.volume)}</td>
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
                      <td>
                        <div className="te-why">
                          {row.reasons.map((reason) => (
                            <i key={reason.tag}
                              className={STRUCTURAL.has(reason.tag) ? "structural" : ""}>
                              {reason.label}
                            </i>
                          ))}
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </>
      )}

      {!loading && !data?.note && rows.length === 0 && (data?.rows?.length ?? 0) > 0 && (
        <p className="te-note">ไม่มีตัวไหนตรงกับตัวกรองที่เลือก</p>
      )}
    </section>
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
        <div className="te-empty"><b>ยังไม่มีข้อมูล</b><small>รอการเก็บรอบถัดไป</small></div>
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
        <div className="te-empty"><b>ไม่มี candidate ตอนนี้</b>
          <small>ตัวจัดอันดับทำงานเฉพาะช่วงที่ตลาดเปิด</small></div>
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
        <div className="te-empty"><b>ยังไม่มีรายการ</b>
          <small>เพิ่ม ticker เพื่อติดตามราคาและข่าว</small></div>
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
        <div className="te-empty"><b>ไม่มีสถานะเปิด</b><small>ทุกไม้ปิดหมดแล้ว</small></div>
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
          หน้านี้ยังไม่ได้ต่อกับข้อมูลจริง — ผมจะไม่ใส่ตัวเลขสมมติไว้
          เพราะแดชบอร์ดที่แต่งตัวเลขเองอันตรายกว่าแดชบอร์ดที่มีช่องว่าง
        </small>
      </div>
    </section>
  );
}
