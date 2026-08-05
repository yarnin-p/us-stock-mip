"use client";

import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";

import TicketPanel from "./TicketPanel";

type Quote = {
  bid_price: number;
  bid_size: number;
  ask_price: number;
  ask_size: number;
  observed_at: string;
  source: string;
};
type Snapshot = {
  price: number;
  volume: number;
  change_ratio: number;
  score: number;
  observed_at: string;
};
type Candidate = {
  ticker: string;
  trading_date: string;
  rank: number;
  score: number;
  coverage: number;
  selected: boolean;
  snapshot?: Snapshot;
  quote?: Quote;
};
type ScanSignal = {
  ticker: string;
  price: number;
  volume: number;
  change_ratio: number;
  score: number;
  observed_at: string;
  quote?: Quote;
};
type WatchItem = {
  ticker: string;
  thesis?: string;
  added_at: string;
  snapshot?: Snapshot;
  quote?: Quote;
};
type ScorePoint = {
  observed_at: string;
  score: number;
  coverage?: number;
  source: string;
};
type Alert = {
  id: number;
  type: string;
  severity: "INFO" | "WARNING" | "CRITICAL";
  ticker?: string;
  title: string;
  message: string;
  created_at: string;
  acknowledged_at?: string;
};
type NewsCatalyst = {
  id: number;
  ticker: string;
  external_id?: string;
  published_at: string;
  available_at: string;
  title: string;
  description?: string;
  source_url?: string;
  sentiment?: string;
  stored_score: number;
  classification: {
    kind: string;
    strength: number;
    tradeable: boolean;
    negative: boolean;
    reasons: string[];
  };
  snapshot?: Snapshot;
  quote?: Quote;
};
type ComponentHealth = {
  name: string;
  status: string;
  detail?: string;
  last_update?: string;
  latency_ms?: number;
};
type BrokerPosition = {
  account_id: string;
  position_id: string;
  ticker: string;
  quantity: number;
  average_price?: number;
  unrealized_pnl?: number;
  synced_at: string;
};
type BrokerOrder = {
  account_id: string;
  client_order_id: string;
  order_id?: string;
  ticker: string;
  side: string;
  status: string;
  total_quantity: number;
  filled_quantity: number;
  filled_price?: number;
  commission: number;
  fees: number;
  placed_at?: string;
  filled_at?: string;
  synced_at: string;
};
type SystemHealth = {
  components: ComponentHealth[];
  scheduler: {
    enabled: boolean;
    timezone: string;
    next_job?: string;
    next_run?: string;
    last_job?: string;
    last_status?: string;
    last_run?: string;
    last_message?: string;
  };
  updated_at: string;
};
type ExecutionRisk = {
  allowed: boolean;
  estimated_cost: number;
  risk_level: "LOW" | "MEDIUM" | "HIGH";
  violations: { code: string; message: string }[];
};
type ExecutionOrder = {
  id: number;
  client_order_id: string;
  mode: "paper" | "shadow" | "live";
  ticker: string;
  side: "BUY" | "SELL";
  quantity: number;
  limit_price: number;
  state: string;
  estimated_cost: number;
  estimated_fee: number;
  risk: ExecutionRisk;
  reason?: string;
  ai_score?: number;
  catalyst_score?: number;
  filled_quantity?: number;
  average_fill_price?: number;
  approval_expires_at?: string;
  created_at?: string;
  updated_at: string;
};
type ExecutionPosition = {
  mode: "paper" | "shadow" | "live";
  ticker: string;
  quantity: number;
  average_cost: number;
  current_price: number;
  unrealized_pnl: number;
  realized_pnl: number;
  updated_at: string;
};
type StrategyPlan = {
  mode: "paper" | "shadow" | "live";
  ticker: string;
  rank: number;
  score: number;
  status: string;
  strategy_version: string;
  session_high: number;
  pullback_low: number;
  entry_price: number;
  stop_price: number;
  trailing_stop: number;
  quantity: number;
  entry_order_id?: number;
  exit_order_id?: number;
  last_price: number;
  order_flow: {
    quote_updates: number;
    trade_ticks: number;
    aggressive_buy_ratio: number;
    uptick_ratio: number;
    average_book_pressure: number;
    price_velocity: number;
  };
  last_reason?: string;
  updated_at: string;
};
type ExecutionTransaction = {
  id: number;
  order_id: number;
  mode: "paper" | "shadow" | "live";
  ticker: string;
  side: "BUY" | "SELL";
  quantity: number;
  price: number;
  fee: number;
  realized_pnl?: number;
  reason?: string;
  strategy: string;
  source: "EXECUTION" | "WEBULL";
  executed_at: string;
};
type DailyPnL = {
  trading_date: string;
  mode: "paper" | "shadow" | "live";
  gross_pnl: number;
  fees: number;
  net_pnl: number;
  entries: number;
  exits: number;
  transactions: number;
};
type ExecutionConfig = {
  mode: "paper" | "live";
  automatic_trading: boolean;
  approval_required: boolean;
  kill_switch: boolean;
  allowed_sessions: string[];
  live_entries_enabled: boolean;
};
type SpikeWatch = {
  model_id: number;
  model_name: string;
  algorithm: string;
  target_trading_date: string;
  ranking_as_of: string;
  generated_at: string;
  ranking_count: number;
  evidence: {
    kind: "FORWARD" | "RETROSPECTIVE_BACKTEST";
    status: "FORWARD_READY" | "RETROSPECTIVE" | "STALE";
    point_in_time_causal: boolean;
  };
  model_gate: {
    eligible: boolean;
    reason: string;
  };
  validation_samples: number;
  positive_count: number;
  recall_at_20: number;
  recall_at_100: number;
  pattern_version: string;
  pattern_status: "COLLECTING" | "FORWARD_SHADOW";
  pattern_note: string;
  candidates: {
    ticker: string;
    rank: number;
    probability: number;
    phase: string;
    confirmation: "LIVE_CONFIRMED" | "WAITING_MARKET" | "STALE";
    pattern_rank?: number;
    pattern_score?: number;
    pattern_coverage?: number;
    pattern_state?: "EARLY" | "BUILDING" | "CONFIRMED" | "TOO_LATE";
    pattern_version?: string;
    pattern_observed_at?: string;
    pattern_reasons?: string[];
    pattern_missing_features?: string[];
    snapshot?: Snapshot;
    quote?: Quote;
  }[];
  note: string;
};
type LearningReport = {
  champion?: {
    id: number;
    name: string;
    algorithm: string;
    stage: string;
    trained_from: string;
    trained_to: string;
    validation_from?: string;
    validation_to?: string;
    training_metrics: Record<string, number>;
    validation_metrics: Record<string, number>;
    created_at: string;
  };
  challenger?: {
    id: number;
    name: string;
    algorithm: string;
    stage: string;
    trained_from: string;
    trained_to: string;
    validation_from?: string;
    validation_to?: string;
    training_metrics: Record<string, number>;
    validation_metrics: Record<string, number>;
    created_at: string;
  };
  spike_model?: {
    id: number;
    name: string;
    algorithm: string;
    stage: string;
    trained_from: string;
    trained_to: string;
    validation_from?: string;
    validation_to?: string;
    training_metrics: Record<string, number>;
    validation_metrics: Record<string, number>;
    created_at: string;
  };
  spike_evaluation?: {
    trading_date: string;
    model_name: string;
    common_stocks: number;
    evidence: {
      kind: "FORWARD" | "RETROSPECTIVE_BACKTEST";
      ranking_as_of?: string;
      ranking_created_at?: string;
      model_created_at?: string;
      model_promoted_at?: string;
      scanner_started_at?: string;
      scanner_ended_at?: string;
      point_in_time_causal: boolean;
    };
    thresholds: {
      threshold: number;
      positives: number;
      excluded_corporate_action: number;
      scanner_hits: number;
      scanner_late_hits: number;
      scanner_unknown_hits: number;
      scanner_misses: number;
      scanner_coverage: number;
      dynamic_hits: number;
      dynamic_late_hits: number;
      dynamic_unknown_hits: number;
      dynamic_misses: number;
      dynamic_coverage: number;
      top_k: {
        k: number;
        hits: number;
        recall: number;
        precision: number;
      }[];
    }[];
    patterns: {
      dimension: string;
      value: string;
      count: number;
      average_return: number;
      average_remaining_upside: number;
    }[];
    outcomes: {
      ticker: string;
      return: number;
      rank: number;
      open_return: number;
      origin_session: string;
      discovery_phase: string;
      catalyst_pattern: string;
      price_action_pattern: string;
      left_censored: boolean;
      minutes_signal_to_peak?: number;
      remaining_upside?: number;
    }[];
  };
  last_decision?: {
    decision: string;
    reason: string;
    created_at: string;
  };
  coverage: {
    trading_date: string;
    daily_bars: number;
    feature_snapshots: number;
    candidate_rankings: number;
    market_quotes: number;
    market_quote_tickers: number;
    market_ticks: number;
    market_tick_tickers: number;
    scanner_signals: number;
    news_items: number;
    sec_filings: number;
    execution_orders: number;
    execution_fills: number;
    strategy_cycles: number;
    first_market_event?: string;
    last_market_event?: string;
  };
  strategy: {
    trading_date?: string;
    shadow_strategy_version?: string;
    shadow_first_trading_date?: string;
    shadow_last_trading_date?: string;
    shadow_trading_days: number;
    valid_shadow_cycles: number;
    excluded_cycles: number;
    shadow_net_pnl: number;
    shadow_gross_profit: number;
    shadow_gross_loss: number;
    shadow_profit_factor: number;
    shadow_fees: number;
    replay_cycles: number;
    replay_matched_closed: number;
    replay_inconclusive: number;
    actual_net_pnl: number;
    challenger_net_pnl_delta: number;
  };
  certification: {
    eligible: boolean;
    decision: string;
    checks: {
      code: string;
      label: string;
      required: boolean;
      passed: boolean;
      detail: string;
    }[];
  };
  updated_at: string;
};
type SizingResult = {
  ticker: string;
  shares: number;
  risk_amount: number;
  risk_per_share: number;
  estimated_cost: number;
  capped_by?: string;
  calculation_rule: string;
};
type ExecutionApproval = {
  order: ExecutionOrder;
  confirmation_token: string;
  confirmation_text: string;
};
type Envelope<T> = { data: T; generated_at: string };
type View =
  | "dashboard"
  | "catalysts"
  | "watchlist"
  | "execution"
  | "positions"
  | "review";

const API =
  process.env.NEXT_PUBLIC_API_URL?.replace(/\/$/, "") ??
  "http://127.0.0.1:8080";
const demoCandidates: Candidate[] = [
  {
    ticker: "BIYA",
    trading_date: "2026-07-28",
    rank: 1,
    score: 58.47,
    coverage: 0.7,
    selected: true,
    snapshot: {
      price: 6.42,
      volume: 28661940,
      change_ratio: 0.539568,
      score: 61.41,
      observed_at: "2026-07-28T19:36:53.367Z",
    },
  },
  { ticker: "LVWR", trading_date: "2026-07-28", rank: 2, score: 54.06, coverage: 0.7, selected: true },
  { ticker: "INLF", trading_date: "2026-07-28", rank: 3, score: 44.45, coverage: 0.7, selected: true },
  {
    ticker: "OPK",
    trading_date: "2026-07-28",
    rank: 8,
    score: 7.55,
    coverage: 0.4,
    selected: false,
    snapshot: {
      price: 1.635,
      volume: 32429005,
      change_ratio: 0.318548,
      score: 39.37,
      observed_at: "2026-07-28T19:53:43.160Z",
    },
    quote: {
      bid_price: 1.67,
      bid_size: 15017,
      ask_price: 1.68,
      ask_size: 34481,
      observed_at: "2026-07-28T19:57:03.427Z",
      source: "webull",
    },
  },
];
const demoWatch: WatchItem[] = [{
  ticker: "OPK",
  thesis: "After-hours continuation; require bid support before entry",
  added_at: "2026-07-28T20:08:30.794Z",
  snapshot: demoCandidates[3].snapshot,
  quote: demoCandidates[3].quote,
}];
const demoMode = process.env.NEXT_PUBLIC_DEMO_MODE === "true";
const compact = new Intl.NumberFormat("en-US", {
  notation: "compact",
  maximumFractionDigits: 1,
});
const money = (value?: number, digits = 2) =>
  value == null ? "—" : `$${value.toFixed(digits)}`;
const pct = (value?: number) =>
  value == null ? "—" : `${value >= 0 ? "+" : ""}${(value * 100).toFixed(1)}%`;
const boundaryReasonPrefix = "AUTO BOUNDARY_CATALYST:";
type BoundaryOrderDetails = {
  rank?: number;
  kind?: string;
  strength?: number;
  score?: number;
  bid?: number;
  ask?: number;
  spread?: number;
  volume?: number;
  title?: string;
};
function marketDate(value: number | string | undefined) {
  if (!value) return "";
  const date = typeof value === "number" ? new Date(value) : new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: "America/New_York",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(date);
  const read = (type: Intl.DateTimeFormatPartTypes) =>
    parts.find((part) => part.type === type)?.value ?? "";
  return `${read("year")}-${read("month")}-${read("day")}`;
}
function parseBoundaryOrder(reason?: string): BoundaryOrderDetails | null {
  if (!reason?.startsWith(boundaryReasonPrefix)) return null;
  const token = (key: string) =>
    new RegExp(`(?:^|\\s)${key}=([^\\s]+)`).exec(reason)?.[1];
  const number = (key: string) => {
    const value = Number(token(key));
    return Number.isFinite(value) ? value : undefined;
  };
  const titleToken =
    /(?:^|\s)title=("(?:\\.|[^"\\])*")/.exec(reason)?.[1];
  let title: string | undefined;
  if (titleToken) {
    try {
      title = JSON.parse(titleToken) as string;
    } catch {
      title = titleToken.slice(1, -1);
    }
  }
  return {
    rank: number("rank"),
    kind: token("kind"),
    strength: number("strength"),
    score: number("score"),
    bid: number("bid"),
    ask: number("ask"),
    spread: number("spread"),
    volume: number("volume"),
    title,
  };
}
function age(timestamp: string | undefined, now: number) {
  if (!timestamp || !now) return "NO FEED";
  const seconds = Math.round((now - Date.parse(timestamp)) / 1000);
  if (seconds < -2) return "CLOCK +";
  if (seconds < 10) return "LIVE";
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  return `${Math.floor(seconds / 3600)}h`;
}
function wireTime(timestamp: string | undefined, timeZone: string) {
  if (!timestamp) return "—";
  const parsed = Date.parse(timestamp);
  if (Number.isNaN(parsed)) return "—";
  return new Intl.DateTimeFormat("en-US", {
    timeZone,
    month: "short",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).format(parsed);
}
function ingestionLag(item: NewsCatalyst) {
  const seconds = Math.max(
    0,
    Math.round(
      (Date.parse(item.available_at) - Date.parse(item.published_at)) / 1000,
    ),
  );
  if (!Number.isFinite(seconds)) return "—";
  if (seconds < 60) return `${seconds}s`;
  return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}
function sourceHost(url: string | undefined) {
  if (!url) return "WIRE";
  try {
    return new URL(url).hostname.replace(/^www\./, "").toUpperCase();
  } catch {
    return "WIRE";
  }
}
function countdownTo(
  day: number,
  currentSeconds: number,
  targetMinutes: number,
) {
  const targetSeconds = targetMinutes * 60;
  let daysAhead = currentSeconds < targetSeconds && day >= 1 && day <= 5 ? 0 : 1;
  while ((day + daysAhead) % 7 === 0 || (day + daysAhead) % 7 === 6) {
    daysAhead++;
  }
  return daysAhead * 86400 + targetSeconds - currentSeconds;
}
function duration(value: number) {
  const seconds = Math.max(0, Math.floor(value));
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  return `${String(hours).padStart(2, "0")}:${String(minutes).padStart(2, "0")}:${String(seconds % 60).padStart(2, "0")}`;
}
function marketSession(now: number, allowedSessions: string[] = []) {
  if (!now) return {
    label: "CHECKING", detail: "", waiting: false,
    premarketIn: "--:--:--", regularIn: "--:--:--",
  };
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: "America/New_York",
    weekday: "short",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
  }).formatToParts(now);
  const value = (type: Intl.DateTimeFormatPartTypes) =>
    parts.find((part) => part.type === type)?.value ?? "";
  const weekdays: Record<string, number> = {
    Sun: 0, Mon: 1, Tue: 2, Wed: 3, Thu: 4, Fri: 5, Sat: 6,
  };
  const day = weekdays[value("weekday")];
  const minute = Number(value("hour")) * 60 + Number(value("minute"));
  const currentSeconds = minute * 60 + Number(value("second"));
  let label = "CLOSED";
  let detail = "NO ORDER ROUTING";
  let waiting = true;
  const overnight =
    (day === 0 && minute >= 1200) ||
    (day >= 1 && day <= 5 && minute < 240) ||
    (day >= 1 && day <= 4 && minute >= 1200);
  if (overnight) {
    label = "OVERNIGHT";
    if (allowedSessions.includes("OVERNIGHT")) {
      detail = "WEBULL NIGHT";
      waiting = false;
    } else {
      detail = "DATA DISABLED · WAIT PRE-MARKET";
      waiting = true;
    }
  } else if (day >= 1 && day <= 5 && minute >= 240 && minute < 570) {
    label = "PRE-MARKET";
    detail = "WEBULL EXTENDED";
    waiting = false;
  } else if (day >= 1 && day <= 5 && minute >= 570 && minute < 960) {
    label = "REGULAR";
    detail = "WEBULL CORE";
    waiting = false;
  } else if (day >= 1 && day <= 5 && minute >= 960 && minute < 1200) {
    label = "AFTER HOURS";
    detail = "WEBULL EXTENDED";
    waiting = false;
  }
  return {
    label,
    detail,
    waiting,
    premarketIn: duration(countdownTo(day, currentSeconds, 240)),
    regularIn: duration(countdownTo(day, currentSeconds, 570)),
  };
}
const timeAt = (now: number, timeZone: string) =>
  now
    ? new Intl.DateTimeFormat("en-US", {
        timeZone, hour: "2-digit", minute: "2-digit", second: "2-digit",
        hour12: true,
      }).format(now)
    : "--:--:--";
async function get<T>(path: string, signal: AbortSignal): Promise<T> {
  const response = await fetch(`${API}${path}`, {
    signal,
    headers: { Accept: "application/json" },
  });
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  return response.json() as Promise<T>;
}

export function MomentumDashboard() {
  const [view, setView] = useState<View>("dashboard");
  const [candidates, setCandidates] = useState<Candidate[]>(
    demoMode ? demoCandidates : [],
  );
  const [watchlist, setWatchlist] = useState<WatchItem[]>(
    demoMode ? demoWatch : [],
  );
  const [brokerPositions, setBrokerPositions] = useState<BrokerPosition[]>([]);
  const [brokerOrders, setBrokerOrders] = useState<BrokerOrder[]>([]);
  const [executionOrders, setExecutionOrders] = useState<ExecutionOrder[]>([]);
  const [executionPositions, setExecutionPositions] = useState<ExecutionPosition[]>([]);
  const [strategyPlans, setStrategyPlans] = useState<StrategyPlan[]>([]);
  const [executionTransactions, setExecutionTransactions] = useState<ExecutionTransaction[]>([]);
  const [dailyPnL, setDailyPnL] = useState<DailyPnL[]>([]);
  const [learningReport, setLearningReport] = useState<LearningReport | null>(null);
  const [spikeWatch, setSpikeWatch] = useState<SpikeWatch | null>(null);
  const [executionConfig, setExecutionConfig] = useState<ExecutionConfig | null>(null);
  const [executionApproval, setExecutionApproval] = useState<ExecutionApproval | null>(null);
  const [sizingResult, setSizingResult] = useState<SizingResult | null>(null);
  const [signals, setSignals] = useState<ScanSignal[]>([]);
  const [health, setHealth] = useState<SystemHealth | null>(null);
  const [alerts, setAlerts] = useState<Alert[]>([]);
  const [newsCatalysts, setNewsCatalysts] = useState<NewsCatalyst[]>([]);
  const [scoreHistory, setScoreHistory] = useState<ScorePoint[]>([]);
  const [focusTicker, setFocusTicker] = useState("OPK");
  const [link, setLink] = useState<"connecting" | "live" | "offline">("connecting");
  const [now, setNow] = useState(0);
  const [notice, setNotice] = useState("");

  const refresh = useCallback(async (signal: AbortSignal) => {
    try {
      void get<Envelope<LearningReport>>("/learning-report", signal)
        .then((response) => setLearningReport(response.data))
        .catch((error: Error) => {
          if (error.name !== "AbortError") {
            console.warn("learning report unavailable", error);
          }
        });
      void get<Envelope<SpikeWatch>>("/spike-watch", signal)
        .then((response) => setSpikeWatch(response.data))
        .catch((error: Error) => {
          if (error.name !== "AbortError") {
            console.warn("spike watch unavailable", error);
          }
        });
      void get<Envelope<NewsCatalyst[]>>(
        "/news-catalysts?hours=24&limit=100",
        signal,
      )
        .then((response) => setNewsCatalysts(response.data))
        .catch((error: Error) => {
          if (error.name !== "AbortError") {
            console.warn("news catalysts unavailable", error);
          }
        });
      const [c, s, w, bp, bo, h, a, eo, ep, ec, sp, tx, dp] = await Promise.all([
        get<Envelope<Candidate[]>>("/candidates", signal),
        get<Envelope<ScanSignal[]>>("/scan", signal),
        get<Envelope<WatchItem[]>>("/watchlist", signal),
        get<Envelope<BrokerPosition[]>>("/broker-positions", signal),
        get<Envelope<BrokerOrder[]>>("/broker-orders", signal),
        get<Envelope<SystemHealth>>("/system-health", signal),
        get<Envelope<Alert[]>>("/alerts", signal),
        get<Envelope<ExecutionOrder[]>>("/execution/orders", signal),
        get<Envelope<ExecutionPosition[]>>("/execution/positions", signal),
        get<ExecutionConfig>("/execution/config", signal),
        get<Envelope<StrategyPlan[]>>("/strategy/plans", signal),
        get<Envelope<ExecutionTransaction[]>>("/execution/transactions", signal),
        get<Envelope<DailyPnL[]>>("/execution/daily-pnl", signal),
      ]);
      setCandidates(c.data);
      setSignals(s.data);
      setWatchlist(w.data);
      setBrokerPositions(bp.data);
      setBrokerOrders(bo.data);
      setHealth(h.data);
      setAlerts(a.data);
      setExecutionOrders(eo.data);
      setExecutionPositions(ep.data);
      setExecutionConfig(ec);
      setStrategyPlans(sp.data);
      setExecutionTransactions(tx.data);
      setDailyPnL(dp.data);
      setLink("live");
    } catch (error) {
      if ((error as Error).name !== "AbortError") setLink("offline");
    }
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    const startTimer = window.setTimeout(() => {
      setNow(Date.now());
      void refresh(controller.signal);
    }, 0);
    const pull = async <T,>(path: string, setter: (value: T) => void) => {
      try {
        const response = await get<Envelope<T>>(path, controller.signal);
        setter(response.data);
        setLink("live");
      } catch (error) {
        if ((error as Error).name !== "AbortError") setLink("offline");
      }
    };
    const pullScope = (scope: string) => {
      if (scope === "candidates") void pull<Candidate[]>("/candidates", setCandidates);
      if (scope === "scan") void pull<ScanSignal[]>("/scan", setSignals);
      if (scope === "watchlist") void pull<WatchItem[]>("/watchlist", setWatchlist);
      if (scope === "positions") {
        void pull<BrokerPosition[]>("/broker-positions", setBrokerPositions);
      }
      if (scope === "trades") {
        void pull<BrokerOrder[]>("/broker-orders", setBrokerOrders);
      }
      if (scope === "score_history") {
        void pull<ScorePoint[]>(`/score-history/${focusTicker}`, setScoreHistory);
      }
      if (scope === "alerts") void pull<Alert[]>("/alerts", setAlerts);
      if (scope === "news") {
        void pull<NewsCatalyst[]>(
          "/news-catalysts?hours=24&limit=100",
          setNewsCatalysts,
        );
      }
      if (scope === "health") {
        void pull<SystemHealth>("/system-health", setHealth);
        void get<Envelope<LearningReport>>(
          "/learning-report",
          controller.signal,
        )
          .then((response) => setLearningReport(response.data))
          .catch((error: Error) => {
            if (error.name !== "AbortError") {
              console.warn("learning report unavailable", error);
            }
          });
        void get<Envelope<SpikeWatch>>(
          "/spike-watch",
          controller.signal,
        )
          .then((response) => setSpikeWatch(response.data))
          .catch((error: Error) => {
            if (error.name !== "AbortError") {
              console.warn("spike watch unavailable", error);
            }
          });
      }
      if (scope === "spike_watch") {
        void pull<SpikeWatch>("/spike-watch", setSpikeWatch);
      }
      if (scope === "execution") {
        void pull<ExecutionOrder[]>("/execution/orders", setExecutionOrders);
        void pull<ExecutionPosition[]>("/execution/positions", setExecutionPositions);
        void pull<ExecutionTransaction[]>("/execution/transactions", setExecutionTransactions);
        void pull<DailyPnL[]>("/execution/daily-pnl", setDailyPnL);
        void pull<LearningReport>("/learning-report", setLearningReport);
      }
      if (scope === "strategy") {
        void pull<StrategyPlan[]>("/strategy/plans", setStrategyPlans);
      }
    };
    const stream = new EventSource(`${API}/events`);
    const pending = new Set<string>();
    let debounce: number | undefined;
    const onEvent = (event: Event) => {
      try {
        const payload = JSON.parse((event as MessageEvent<string>).data) as { scope: string };
        payload.scope.split(",").forEach((scope) => pending.add(scope));
        if (debounce) window.clearTimeout(debounce);
        debounce = window.setTimeout(() => {
          pending.forEach(pullScope);
          pending.clear();
        }, 80);
      } catch {
        setLink("offline");
      }
    };
    stream.addEventListener("ready", onEvent);
    stream.addEventListener("change", onEvent);
    stream.onopen = () => setLink("live");
    stream.onerror = () => setLink("offline");
    const clockTimer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => {
      controller.abort();
      stream.close();
      clearTimeout(startTimer);
      if (debounce) clearTimeout(debounce);
      clearInterval(clockTimer);
    };
  }, [focusTicker, refresh]);

  useEffect(() => {
    const controller = new AbortController();
    void get<Envelope<ScorePoint[]>>(
      `/score-history/${focusTicker}`, controller.signal,
    ).then((result) => setScoreHistory(result.data)).catch(() => setScoreHistory([]));
    return () => controller.abort();
  }, [focusTicker]);

  const focus = useMemo(() => {
    const c = candidates.find((item) => item.ticker === focusTicker);
    const w = watchlist.find((item) => item.ticker === focusTicker);
    const s = signals.find((item) => item.ticker === focusTicker);
    const predicted = spikeWatch?.candidates.find(
      (item) => item.ticker === focusTicker,
    );
    const catalyst = newsCatalysts.find(
      (item) => item.ticker === focusTicker,
    );
    return {
      ticker: focusTicker,
      snapshot: c?.snapshot ?? w?.snapshot ?? predicted?.snapshot ??
        catalyst?.snapshot ?? (s ? {
        price: s.price,
        volume: s.volume,
        change_ratio: s.change_ratio,
        score: s.score,
        observed_at: s.observed_at,
      } : undefined),
      quote: c?.quote ?? w?.quote ?? predicted?.quote ??
        catalyst?.quote ?? s?.quote,
      rank: c?.rank,
      score: c?.score,
      selected: c?.selected,
    };
  }, [
    candidates,
    focusTicker,
    newsCatalysts,
    signals,
    spikeWatch,
    watchlist,
  ]);
  const quoteAge = age(focus.quote?.observed_at, now);
  const quoteFresh = quoteAge === "LIVE" || quoteAge.endsWith("s");
  const session = marketSession(now, executionConfig?.allowed_sessions);
  const focusPlan = strategyPlans.find((plan) =>
    plan.ticker === focus.ticker &&
    (!executionConfig || plan.mode === executionConfig.mode)
  );

  async function mutate(path: string, init: RequestInit, message: string) {
    try {
      const response = await fetch(`${API}${path}`, init);
      if (!response.ok) throw new Error();
      setNotice(message);
      const controller = new AbortController();
      await refresh(controller.signal);
      return true;
    } catch {
      setNotice("API unavailable — no changes were saved");
      return false;
    }
  }
  async function addWatch(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const ticker = String(form.get("ticker") ?? "").trim().toUpperCase();
    const thesis = String(form.get("thesis") ?? "").trim();
    if (!ticker) return;
    if (await mutate("/watchlist", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ticker, thesis }),
    }, `${ticker} added to watchlist`)) {
      setFocusTicker(ticker);
      event.currentTarget.reset();
    }
  }
  async function acknowledgeAlert(id: number) {
    await mutate(
      `/alerts/${id}/acknowledge`,
      { method: "POST" },
      "Alert acknowledged",
    );
  }
  async function executionRequest<T>(
    path: string,
    init: RequestInit,
    success: string,
  ): Promise<T | null> {
    try {
      const response = await fetch(`${API}${path}`, {
        ...init,
        headers: {
          Accept: "application/json",
          ...(init.body ? { "Content-Type": "application/json" } : {}),
          ...init.headers,
        },
      });
      const payload = await response.json() as T & { error?: string };
      if (!response.ok) throw new Error(payload.error ?? `HTTP ${response.status}`);
      setNotice(success);
      const controller = new AbortController();
      const [orders, positions] = await Promise.all([
        get<Envelope<ExecutionOrder[]>>("/execution/orders", controller.signal),
        get<Envelope<ExecutionPosition[]>>("/execution/positions", controller.signal),
      ]);
      setExecutionOrders(orders.data);
      setExecutionPositions(positions.data);
      return payload;
    } catch (error) {
      setNotice((error as Error).message || "Execution request failed");
      return null;
    }
  }
  async function createExecutionOrder(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const ticker = String(form.get("ticker") ?? "").trim().toUpperCase();
    const created = await executionRequest<ExecutionOrder>(
      "/execution/orders",
      {
        method: "POST",
        body: JSON.stringify({
          ticker,
          side: form.get("side"),
          quantity: Number(form.get("quantity")),
          limit_price: Number(form.get("limit_price")),
          time_in_force: form.get("time_in_force"),
          reason: String(form.get("reason") ?? ""),
          ai_score: focus.score,
        }),
      },
      `${ticker} risk validation completed`,
    );
    if (created?.risk.allowed) event.currentTarget.reset();
  }
  async function calculatePositionSize(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const result = await executionRequest<SizingResult>(
      "/execution/size",
      {
        method: "POST",
        body: JSON.stringify({
          ticker: String(form.get("ticker") ?? "").trim().toUpperCase(),
          risk_amount: Number(form.get("risk_amount")),
          entry_price: Number(form.get("entry_price")),
          stop_price: Number(form.get("stop_price")),
        }),
      },
      "Deterministic risk-based size calculated",
    );
    if (result) setSizingResult(result);
  }
  async function previewExecutionOrder(id: number) {
    await executionRequest<ExecutionOrder>(
      `/execution/orders/${id}/preview`, { method: "POST" },
      "Broker preview completed — no order sent",
    );
  }
  async function approveExecutionOrder(id: number) {
    const approval = await executionRequest<ExecutionApproval>(
      `/execution/orders/${id}/approve`, { method: "POST" },
      "Approval recorded — type the confirmation phrase to send",
    );
    if (approval) setExecutionApproval(approval);
  }
  async function submitExecutionOrder(
    id: number,
    confirmationText: string,
  ) {
    if (!executionApproval || executionApproval.order.id !== id) {
      setNotice("Approve this preview before submitting");
      return;
    }
    const order = await executionRequest<ExecutionOrder>(
      `/execution/orders/${id}/submit`,
      {
        method: "POST",
        body: JSON.stringify({
          confirmation_token: executionApproval.confirmation_token,
          confirmation_text: confirmationText,
        }),
      },
      executionConfig?.mode === "live"
        ? "Order submitted to Webull"
        : "Paper order filled — Webull was not called",
    );
    if (order) setExecutionApproval(null);
  }
  async function cancelExecutionOrder(id: number) {
    const order = await executionRequest<ExecutionOrder>(
      `/execution/orders/${id}/cancel`, { method: "POST" },
      "Order cancelled",
    );
    if (order && executionApproval?.order.id === id) setExecutionApproval(null);
  }

  return (
    <main className="terminal-shell">
      <header className="market-strip">
        <div className="brand"><b>MI</b><span><strong>MOMENTUM INTELLIGENCE</strong><small>DECISION CONSOLE · US EQUITIES</small></span></div>
        <div className={`session ${session.waiting ? "waiting" : ""}`}><i /><span><small>SESSION</small><strong>{session.label}</strong><em>{session.detail}</em></span></div>
        <div className="link"><small>DATA LINK</small><strong className={link}>{link === "live" ? "API ONLINE" : link === "offline" ? "FALLBACK / STALE" : "CONNECTING"}</strong></div>
        <time>{timeAt(now, "America/New_York")} <small>ET</small></time>
      </header>
      <nav className="rail" aria-label="Primary">
        {([
          "dashboard",
          "catalysts",
          "watchlist",
          "execution",
          "positions",
          "review",
        ] as View[]).map((item) => (
          <button className={view === item ? "active" : ""} key={item} onClick={() => setView(item)}>
            <span>{item[0].toUpperCase()}</span>{item}<b>{item === "watchlist" ? watchlist.length : item === "positions" ? (executionConfig?.mode === "live" ? brokerPositions.length : executionPositions.length) : ""}</b>
          </button>
        ))}
        <footer><small>LIVE UPDATES</small><strong>SSE EVENT BUS</strong><small>CHANGED DATA ONLY</small></footer>
      </nav>
      <section className="workspace">
        {notice && <button className="notice" onClick={() => setNotice("")}>{notice}<span>×</span></button>}
        {view === "dashboard" && <>
          <WorkspaceOverview
            now={now}
            session={session}
            health={health}
            link={link}
          />
          <CandidateTape
            items={candidates}
            signals={signals}
            plans={strategyPlans}
            focus={focusTicker}
            onFocus={setFocusTicker}
          />
          <BoundaryTopThree
            orders={executionOrders}
            now={now}
            focus={focusTicker}
            onFocus={setFocusTicker}
          />
          <SpikeWatchTape
            watch={spikeWatch}
            focus={focusTicker}
            onFocus={setFocusTicker}
          />
          <div className="insight-grid">
            <ScoreHistoryChart ticker={focusTicker} points={scoreHistory} />
            <AlertsPanel items={alerts} onAcknowledge={acknowledgeAlert} />
          </div>
          <TicketPanel api={API} />
          <div className="automation-note">
            <span><small>ANALYSIS MODE</small><strong>DETERMINISTIC</strong></span>
            <p>LLM is disabled. Scanner, research, ranking, score events, and alerts use reproducible rules only.</p>
          </div>
        </>}
        {view === "catalysts" && <NewsCatalystPage
          items={newsCatalysts}
          now={now}
          focus={focusTicker}
          onFocus={setFocusTicker}
        />}
        {view === "watchlist" && <Watchlist items={watchlist} onAdd={addWatch} onRemove={(ticker) => void mutate(`/watchlist/${ticker}`, { method: "DELETE" }, `${ticker} removed`)} onFocus={(ticker) => { setFocusTicker(ticker); setView("dashboard"); }} />}
        {view === "execution" && <ExecutionPanel
          config={executionConfig}
          learning={learningReport}
          orders={executionOrders}
          positions={executionPositions}
          plans={strategyPlans}
          approval={executionApproval}
          sizing={sizingResult}
          focus={focusTicker}
          quote={focus.quote}
          score={focus.score}
          onCreate={createExecutionOrder}
          onSize={calculatePositionSize}
          onPreview={previewExecutionOrder}
          onApprove={approveExecutionOrder}
          onSubmit={submitExecutionOrder}
          onCancel={cancelExecutionOrder}
        />}
        {view === "positions" && <>
          <AutomaticPositions
            items={executionPositions}
            brokerItems={brokerPositions}
            plans={strategyPlans}
            mode={executionConfig?.mode ?? "paper"}
            now={now}
          />
        </>}
        {view === "review" && <Review
          learning={learningReport}
          dailyPnL={dailyPnL}
          transactions={executionTransactions}
          brokerOrders={brokerOrders}
          now={now}
        />}
      </section>
      <aside className="decision">
        <div className="decision-head"><span><small>DECISION RAIL</small><h2>{focus.ticker}</h2></span><b className={quoteFresh ? "fresh" : ""}>{session.waiting && !quoteFresh ? "CLOSED" : quoteAge}</b></div>
        <div className="last"><small>LAST PRINT</small><strong>{money(focus.snapshot?.price, 3)}</strong><span>{pct(focus.snapshot?.change_ratio)}</span></div>
        <OrderBook quote={focus.quote} />
        <EntryPlan
          plan={focusPlan}
          selected={focus.selected === true}
          quoteFresh={quoteFresh}
          sessionWaiting={session.waiting}
        />
        <div className="evidence">
          <span><small>OPEN RANK</small><strong>{focus.rank ? `#${focus.rank}` : "—"}</strong></span>
          <span><small>OPEN SCORE</small><strong>{focus.score?.toFixed(1) ?? "—"}</strong></span>
          <span><small>VOLUME</small><strong>{focus.snapshot ? compact.format(focus.snapshot.volume) : "—"}</strong></span>
          <span><small>SELECTED</small><strong>{focus.selected ? "YES" : "NO"}</strong></span>
        </div>
        <p className="disclaimer">Rule-based decision support, not a promise of performance. Verify the live book before sending an order.</p>
      </aside>
    </main>
  );
}

function Title({ eyebrow, children, stats }: { eyebrow: string; children: string; stats?: React.ReactNode }) {
  return <div className="title"><span><small>{eyebrow}</small><h1>{children}</h1></span>{stats}</div>;
}
function WorkspaceOverview({
  now,
  session,
  health,
  link,
}: {
  now: number;
  session: ReturnType<typeof marketSession>;
  health: SystemHealth | null;
  link: "connecting" | "live" | "offline";
}) {
  const nextRun = health?.scheduler.next_run
    ? new Intl.DateTimeFormat("en-GB", {
        timeZone: "Asia/Bangkok",
        weekday: "short",
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
      }).format(Date.parse(health.scheduler.next_run))
    : "—";
  return <section className="workspace-overview" aria-label="Market time and system health">
    <article className="market-time-card">
      <header><span>MARKET TIME</span><small>DUAL CHRONOMETER</small></header>
      <div className="clock-pair">
        <span><small>🇺🇸 US EASTERN</small><strong>{timeAt(now, "America/New_York")}</strong><em>ET</em></span>
        <i aria-hidden="true" />
        <span><small>🇹🇭 THAILAND</small><strong>{timeAt(now, "Asia/Bangkok")}</strong><em>ICT · UTC+7</em></span>
      </div>
      <div className="session-rail">
        <span><small>STATUS</small><strong className={session.waiting ? "waiting" : ""}><i />{session.label}</strong></span>
        <span><small>PRE-MARKET OPENS IN</small><b>{session.premarketIn}</b></span>
        <span><small>REGULAR OPENS IN</small><b>{session.regularIn}</b></span>
      </div>
    </article>
    <article className="system-health-card">
      <header><span>SYSTEM HEALTH</span><small>{link === "live" ? "EVENT LINK LIVE" : "RECONNECTING"}</small></header>
      <div className="component-grid">
        {(health?.components ?? []).map((component) =>
          <span key={component.name}>
            <i className={healthTone(component.status)} />
            <small>{component.name.toUpperCase()}</small>
            <strong>{component.status}</strong>
            <em title={component.last_update
              ? `Last update ${new Date(component.last_update).toLocaleString()}`
              : "No update recorded"}>
              {component.latency_ms != null
                ? `${component.latency_ms}ms`
                : component.detail ?? "—"}
              {component.last_update ? ` · ${age(component.last_update, now)}` : ""}
            </em>
          </span>
        )}
        {!health && <p>Loading component status…</p>}
      </div>
      <footer>
        <span><small>NEXT AUTOMATION</small><strong>{health?.scheduler.next_job?.replaceAll("_", " ").toUpperCase() ?? "—"}</strong></span>
        <span><small>THAILAND TIME</small><strong>{nextRun}</strong></span>
        <span><small>LAST RESULT</small><strong className={health?.scheduler.last_status === "FAILED" ? "failed" : ""}>{health?.scheduler.last_status ?? "WAITING"}</strong></span>
      </footer>
    </article>
  </section>;
}
function healthTone(status: string) {
  if (["CONNECTED", "READY", "SUCCEEDED", "RUNNING"].includes(status)) return "good";
  if (["STALE", "FAILED", "DISCONNECTED"].includes(status)) return "bad";
  return "idle";
}
function ScoreHistoryChart({ ticker, points }: { ticker: string; points: ScorePoint[] }) {
  const values = points.slice(-40);
  const width = 420, height = 120, padding = 10;
  const minScore = values.length ? Math.min(...values.map((point) => point.score)) : 0;
  const maxScore = values.length ? Math.max(...values.map((point) => point.score)) : 100;
  const spread = Math.max(10, maxScore - minScore);
  const line = values.map((point, index) => {
    const x = padding + (index / Math.max(1, values.length - 1)) * (width - padding * 2);
    const y = height - padding - ((point.score - minScore) / spread) * (height - padding * 2);
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  }).join(" ");
  return <article className="score-history">
    <header><span><small>SCORE HISTORY</small><strong>{ticker}</strong></span><b>{values.at(-1)?.score.toFixed(1) ?? "—"}</b></header>
    {values.length > 1
      ? <svg viewBox={`0 0 ${width} ${height}`} role="img" aria-label={`${ticker} score history`}>
          <line x1="10" x2="410" y1="110" y2="110" />
          <polyline points={line} />
        </svg>
      : <p>Score timeline will draw after two scanner observations.</p>}
    <footer><span>{values[0] ? new Date(values[0].observed_at).toLocaleTimeString() : "—"}</span><span>RULE SCORE · 7D</span><span>{values.at(-1) ? new Date(values.at(-1)!.observed_at).toLocaleTimeString() : "—"}</span></footer>
  </article>;
}
function AlertsPanel({ items, onAcknowledge }: { items: Alert[]; onAcknowledge: (id: number) => void }) {
  const active = items.filter((item) => !item.acknowledged_at).slice(0, 5);
  return <article className="alerts-panel">
    <header><span><small>RULE EVENTS</small><strong>Alerts</strong></span><b>{active.length}</b></header>
    <div>{active.map((item) =>
      <button key={item.id} onClick={() => onAcknowledge(item.id)}>
        <i className={item.severity.toLowerCase()} />
        <span><strong>{item.ticker ? `${item.ticker} · ` : ""}{item.title}</strong><small>{item.message}</small></span>
        <em>ACK</em>
      </button>
    )}{active.length === 0 && <p>No active rule alerts.</p>}</div>
  </article>;
}
function strategyStage(item: Candidate, plans: StrategyPlan[]) {
  const plan = plans.find((candidate) => candidate.ticker === item.ticker);
  if (plan && ["PENDING_ENTRY", "ENTERED", "PENDING_EXIT"].includes(plan.status)) {
    const labels: Record<string, string> = {
      PENDING_ENTRY: "ORDER PENDING",
      ENTERED: "IN POSITION",
      PENDING_EXIT: "EXIT PENDING",
    };
    return {
      label: labels[plan.status],
      tone: plan.status === "ENTERED" ? "position" : "pending",
      detail: plan.last_reason ?? "Managing an active strategy order or position.",
    };
  }
  if (!item.selected) {
    return { label: "OBSERVE", tone: "", detail: "Outside the current dynamic Top N." };
  }
  if (!plan) {
    return {
      label: "WATCHING",
      tone: "selected",
      detail: "Ranked in the dynamic Top N; waiting for strategy initialization.",
    };
  }
  const labels: Record<string, string> = {
    WATCH: "WATCHING",
    PULLBACK: "PULLBACK",
    PENDING_ENTRY: "ORDER PENDING",
    ENTERED: "IN POSITION",
    PENDING_EXIT: "EXIT PENDING",
    CLOSED: "CLOSED",
    INVALIDATED: "INVALIDATED",
  };
  const label = labels[plan.status] ?? plan.status.replaceAll("_", " ");
  const tone = ["ENTERED"].includes(plan.status)
    ? "position"
    : ["PENDING_ENTRY", "PENDING_EXIT"].includes(plan.status)
      ? "pending"
      : ["CLOSED", "INVALIDATED"].includes(plan.status)
        ? "invalid"
        : "selected";
  return {
    label,
    tone,
    detail: plan.last_reason ?? "Watching realtime price and order-flow structure.",
  };
}

function CandidateTape({
  items,
  signals,
  plans,
  focus,
  onFocus,
}: {
  items: Candidate[];
  signals: ScanSignal[];
  plans: StrategyPlan[];
  focus: string;
  onFocus: (ticker: string) => void;
}) {
  return <>
    <Title eyebrow="CONTINUOUS RANKING / LIVE SESSION" stats={<div className="stats"><span><small>RANKED</small><strong>{items.length}</strong></span><span><small>TOP N</small><strong>{items.filter((x) => x.selected).length}</strong></span></div>}>Dynamic candidate tape</Title>
    <div className="tape-head"><span>RK</span><span>SYMBOL</span><span>LAST</span><span>MOVE</span><span>VOLUME</span><span>SCORE</span><span>COVER</span><span>STATE</span></div>
    <div className="tape">{items.map((item) => {
      const stage = strategyStage(item, plans);
      return <button
        className={`tape-row ${focus === item.ticker ? "focused" : ""}`}
        key={item.ticker}
        onClick={() => onFocus(item.ticker)}
        title={stage.detail}
      >
        <span>{String(item.rank).padStart(2, "0")}</span><strong>{item.ticker}</strong><span>{money(item.snapshot?.price, 3)}</span><span className="up">{pct(item.snapshot?.change_ratio)}</span><span>{item.snapshot ? compact.format(item.snapshot.volume) : "—"}</span><span className="score"><i style={{ width: `${item.score}%` }} />{item.score.toFixed(1)}</span><span>{Math.round(item.coverage * 100)}%</span><span className={`tag ${stage.tone}`}>{stage.label}</span>
      </button>;
    })}</div>
    <div className="live-scan"><header><span>CONTINUOUS SCANNER</span><small>15 SEC</small></header>{signals.slice(0, 5).map((signal) => <button key={signal.ticker} onClick={() => onFocus(signal.ticker)}><strong>{signal.ticker}</strong><span>{money(signal.price, 3)}</span><span className="up">{pct(signal.change_ratio)}</span><small>{signal.score.toFixed(1)}</small></button>)}{signals.length === 0 && <p>No fresh scanner signals in the current discovery window.</p>}</div>
    <div className="method"><span>SCAN → RANK → WATCH BOOK → EXECUTE</span><p>Every tradable session is handled continuously. The dynamic Top N can change on every scan; entries still require fresh QUOTE/TICK structure and deterministic risk checks.</p></div>
    <div className="state-pipeline">
      <small>STRATEGY STATE</small>
      <strong>WATCHING → PULLBACK → ORDER PENDING → IN POSITION → EXIT PENDING</strong>
      <span>Top N is a watch state, not a buy signal. Hover a ticker to see the current deterministic reason.</span>
    </div>
  </>;
}
function BoundaryTopThree({
  orders,
  now,
  focus,
  onFocus,
}: {
  orders: ExecutionOrder[];
  now: number;
  focus: string;
  onFocus: (ticker: string) => void;
}) {
  const currentMarketDate = marketDate(now);
  const boundaryOrders = orders
    .filter((order) =>
      order.mode === "shadow" &&
      order.side === "BUY" &&
      parseBoundaryOrder(order.reason) &&
      marketDate(order.created_at ?? order.updated_at) === currentMarketDate
    )
    .map((order) => ({ order, details: parseBoundaryOrder(order.reason)! }))
    .sort((left, right) =>
      (left.details.rank ?? Number.MAX_SAFE_INTEGER) -
        (right.details.rank ?? Number.MAX_SAFE_INTEGER) ||
      left.order.id - right.order.id
    )
    .slice(0, 3);
  const exits = orders.filter((order) =>
    order.mode === "shadow" &&
    order.side === "SELL" &&
    parseBoundaryOrder(order.reason) &&
    marketDate(order.created_at ?? order.updated_at) === currentMarketDate
  );
  const cards = Array.from({ length: 3 }, (_, index) => {
    const selected = boundaryOrders[index];
    if (!selected) return { index };
    const exit = exits.find((order) =>
      order.ticker === selected.order.ticker &&
      order.id > selected.order.id
    );
    return { index, ...selected, exit };
  });
  return <section className="boundary-top-three">
    <header>
      <span>
        <small>15:55 ET NEWS GATE</small>
        <strong>AH BOUNDARY TOP 3</strong>
      </span>
      <span>
        <small>100,000 THB TOTAL CAP</small>
        <strong>SHADOW ONLY</strong>
      </span>
    </header>
    <div className="boundary-grid">
      {cards.map((card) => {
        if (!card.order || !card.details) {
          return <article className="empty" key={`boundary-empty-${card.index}`}>
            <small>SLOT {card.index + 1}</small>
            <strong>WAITING FOR QUALIFIED CATALYST</strong>
            <p>Selection runs 15:55–16:00 ET. No ticker is forced.</p>
          </article>;
        }
        const status = card.exit
          ? card.exit.state === "FILLED" ? "EXITED" : "EXIT PENDING"
          : card.order.state === "FILLED" ? "ENTERED"
            : card.order.state === "PARTIALLY_FILLED" ? "PARTIAL"
              : card.order.state;
        const statusTone = ["FILLED", "PARTIALLY_FILLED"].includes(card.order.state)
          ? "up"
          : ["REJECTED", "FAILED", "CANCELLED"].includes(card.order.state)
            ? "down"
            : "warn";
        return <button
          className={focus === card.order.ticker ? "focused" : ""}
          key={card.order.id}
          onClick={() => onFocus(card.order.ticker)}
          title={card.details.title}
        >
          <span className="boundary-rank">
            <small>SLOT {card.details.rank ?? card.index + 1}</small>
            <strong>{card.order.ticker}</strong>
            <em className={statusTone}>{status}</em>
          </span>
          <p>{card.details.title ?? "Qualified hard catalyst"}</p>
          <dl>
            <div><dt>CATALYST</dt><dd>{card.details.kind ?? "—"}</dd></div>
            <div><dt>STRENGTH</dt><dd>{card.details.strength?.toFixed(2) ?? "—"}</dd></div>
            <div><dt>BID / ASK</dt><dd>{money(card.details.bid, 3)} / {money(card.details.ask, 3)}</dd></div>
            <div><dt>SPREAD</dt><dd>{card.details.spread == null ? "—" : pct(card.details.spread)}</dd></div>
            <div><dt>VOLUME</dt><dd>{card.details.volume == null ? "—" : compact.format(card.details.volume)}</dd></div>
            <div><dt>SIZE / LIMIT</dt><dd>{card.order.quantity} / {money(card.order.limit_price, 3)}</dd></div>
          </dl>
        </button>;
      })}
    </div>
    <footer>
      <strong>NEWS ≤ 8H → FRESH BOOK ≤ 30S → VOLUME → SPREAD → SHADOW ENTRY</strong>
      <span>These are actual boundary selections and order states—not the continuously changing scanner rank.</span>
    </footer>
  </section>;
}
function SpikeWatchTape({
  watch,
  focus,
  onFocus,
}: {
  watch: SpikeWatch | null;
  focus: string;
  onFocus: (ticker: string) => void;
}) {
  const status = watch?.pattern_status ??
    (watch && !watch.model_gate.eligible
      ? "MODEL_BLOCKED"
      : watch?.evidence.status ?? "LOADING");
  const forward = watch?.pattern_status === "FORWARD_SHADOW";
  return <section className="spike-watch">
    <header>
      <span>
        <small>POINT-IN-TIME PATTERN LAYER</small>
        <strong>PRE-SPIKE WATCH</strong>
      </span>
      <span>
        <small>{watch?.pattern_version ?? "pre_spike_pattern_v1"}</small>
        <strong className={forward ? "up" : status === "LOADING" ? "" : "warn"}>
          {status.replaceAll("_", " ")}
        </strong>
        {watch && <small>
          R@20 {Math.round(watch.recall_at_20 * 100)}% ·
          {" "}R@100 {Math.round(watch.recall_at_100 * 100)}%
        </small>}
      </span>
    </header>
    <div className="spike-watch-grid">
      {(watch?.candidates ?? []).slice(0, 10).map((item) => <button
        className={focus === item.ticker ? "focused" : ""}
        key={item.ticker}
        onClick={() => onFocus(item.ticker)}
        title={[
          ...(item.pattern_reasons ?? []),
          ...(item.pattern_missing_features ?? []).map((feature) => `missing: ${feature}`),
        ].join(" · ")}
      >
        <span><small>PRE-SPIKE RANK</small><strong>#{item.pattern_rank ?? "—"} · {item.ticker}</strong></span>
        <span>
          <small>PATTERN STATE</small>
          <strong className={item.pattern_state === "CONFIRMED"
            ? "up"
            : item.pattern_state === "TOO_LATE" ? "down" : "warn"}>
            {item.pattern_state ?? "MODEL SEED"}
          </strong>
        </span>
        <span><small>MATCH / COVERAGE</small><strong>{Math.round(item.pattern_score ?? 0)} / {Math.round((item.pattern_coverage ?? 0) * 100)}%</strong></span>
        <span><small>LIVE MOVE</small><strong>{pct(item.snapshot?.change_ratio)}</strong></span>
      </button>)}
      {!watch && <p>Loading the latest point-in-time model cohort.</p>}
      {watch && watch.candidates.length === 0 &&
        <p>No spike cohort is available for the target session.</p>}
    </div>
    <footer>
      <strong>MODEL SEED → LIVE CONFIRMATION</strong>
      <span>
        {watch
          ? `EARLY → BUILDING → CONFIRMED → TOO LATE. ${watch.pattern_note} ${watch.note} Hover a ticker for matched reasons and missing evidence.`
          : "EARLY → BUILDING → CONFIRMED → TOO LATE. Collecting fresh scanner, QUOTE, TICK, float, and catalyst evidence without using future prices."}
      </span>
    </footer>
  </section>;
}
type NewsFilter = "all" | "catalyst" | "confirmed" | "risk";

function NewsCatalystPage({
  items,
  now,
  focus,
  onFocus,
}: {
  items: NewsCatalyst[];
  now: number;
  focus: string;
  onFocus: (ticker: string) => void;
}) {
  const [filter, setFilter] = useState<NewsFilter>("all");
  const marketConfirmed = useCallback((item: NewsCatalyst) => {
    if (!item.snapshot || !now) return false;
    const freshness = now - Date.parse(item.snapshot.observed_at);
    return freshness >= -2_000 && freshness <= 120_000 &&
      item.snapshot.volume >= 25_000 &&
      item.snapshot.change_ratio > 0;
  }, [now]);
  const material = (item: NewsCatalyst) =>
    item.classification.strength >= .75 &&
    (item.classification.tradeable || item.classification.negative);
  const confirmedCount = items.filter((item) =>
    item.classification.tradeable && marketConfirmed(item)
  ).length;
  const catalystCount = items.filter((item) =>
    item.classification.tradeable && !item.classification.negative
  ).length;
  const riskCount = items.filter((item) =>
    item.classification.negative && item.classification.strength >= .75
  ).length;
  const visible = useMemo(() => items.filter((item) => {
    if (filter === "catalyst") {
      return item.classification.tradeable && !item.classification.negative;
    }
    if (filter === "confirmed") {
      return item.classification.tradeable && marketConfirmed(item);
    }
    if (filter === "risk") {
      return item.classification.negative &&
        item.classification.strength >= .75;
    }
    return material(item);
  }), [filter, items, marketConfirmed]);
  const latest = items.reduce<string | undefined>((result, item) => {
    if (!result || Date.parse(item.available_at) > Date.parse(result)) {
      return item.available_at;
    }
    return result;
  }, undefined);

  return <>
    <Title
      eyebrow="ALPACA + MASSIVE / DETERMINISTIC CLASSIFIER"
      stats={<div className="stats">
        <span><small>CATALYSTS</small><strong>{catalystCount}</strong></span>
        <span><small>CONFIRMED</small><strong>{confirmedCount}</strong></span>
        <span><small>RISK</small><strong>{riskCount}</strong></span>
      </div>}
    >
      News catalyst wire
    </Title>
    <section className="news-console">
      <header className="news-console-head">
        <span>
          <small>WIRE STATUS</small>
          <strong>{items.length ? "RECEIVING" : "WAITING"}</strong>
        </span>
        <span>
          <small>LAST INGESTED</small>
          <strong>{latest ? `${age(latest, now)} AGO` : "NO ARTICLES"}</strong>
        </span>
        <span>
          <small>CAUSAL PATH</small>
          <strong>PUBLISHED → INGESTED → MARKET</strong>
        </span>
      </header>
      <div className="news-filters" role="group" aria-label="News catalyst filter">
        {(["all", "catalyst", "confirmed", "risk"] as NewsFilter[]).map(
          (value) => <button
            className={filter === value ? "active" : ""}
            key={value}
            onClick={() => setFilter(value)}
          >
            {value === "all" ? "MATERIAL" : value.toUpperCase()}
          </button>,
        )}
        <p>
          Hard news is necessary; MARKET CONFIRMED additionally requires a
          fresh scanner print, positive move, and ≥25K volume.
        </p>
      </div>
      <div className="news-wire-head">
        <span>SYMBOL / STATE</span>
        <span>HEADLINE / CLASSIFICATION</span>
        <span>MARKET</span>
        <span>WIRE TIMING</span>
        <span>SOURCE</span>
      </div>
      <div className="news-wire">
        {visible.map((item) => {
          const momentumConfirmed = marketConfirmed(item);
          const confirmed = item.classification.tradeable &&
            momentumConfirmed;
          const state = item.classification.negative
            ? "risk"
            : confirmed
              ? "confirmed"
              : item.classification.tradeable
                ? "catalyst"
                : "context";
          const stateLabel = item.classification.negative
            ? momentumConfirmed
              ? "RISK + MOMENTUM"
              : "RISK"
            : confirmed
              ? "MARKET CONFIRMED"
              : item.classification.tradeable
                ? "HARD CATALYST"
                : "CONTEXT";
          return <article
            className={`news-wire-row ${state} ${focus === item.ticker ? "focused" : ""}`}
            key={item.id}
          >
            <button
              className="news-symbol"
              onClick={() => onFocus(item.ticker)}
            >
              <small>{stateLabel}</small>
              <strong>{item.ticker}</strong>
              <span>{(item.classification.strength * 100).toFixed(0)} / 100</span>
            </button>
            <div className="news-story">
              <strong>{item.title}</strong>
              {item.description && <p>{item.description}</p>}
              <span>
                {item.classification.kind.replaceAll("_", " ")}
                {item.classification.reasons[0]
                  ? ` · ${item.classification.reasons[0]}`
                  : ""}
              </span>
            </div>
            <div className="news-market">
              <strong>{money(item.snapshot?.price, 3)}</strong>
              <span className={
                (item.snapshot?.change_ratio ?? 0) >= 0 ? "up" : "down"
              }>
                {pct(item.snapshot?.change_ratio)}
              </span>
              <small>
                VOL {item.snapshot
                  ? compact.format(item.snapshot.volume)
                  : "—"}
              </small>
              <small>
                BOOK {item.quote
                  ? `${money(item.quote.bid_price, 3)} × ${money(item.quote.ask_price, 3)}`
                  : "—"}
              </small>
            </div>
            <div className="news-timing">
              <span>
                <small>PUBLISHED ET</small>
                <strong>{wireTime(item.published_at, "America/New_York")}</strong>
              </span>
              <span>
                <small>INGESTED ICT</small>
                <strong>{wireTime(item.available_at, "Asia/Bangkok")}</strong>
              </span>
              <span>
                <small>INGEST LAG</small>
                <strong>{ingestionLag(item)}</strong>
              </span>
            </div>
            <div className="news-source">
              <strong>{sourceHost(item.source_url)}</strong>
              <small>
                NEWS {age(item.available_at, now)} · BOOK{" "}
                {age(item.quote?.observed_at, now)}
              </small>
              {item.source_url
                ? <a
                  href={item.source_url}
                  target="_blank"
                  rel="noreferrer"
                >
                  OPEN SOURCE ↗
                </a>
                : <span>NO SOURCE URL</span>}
            </div>
          </article>;
        })}
        {!visible.length && <div className="news-empty">
          <strong>NO QUALIFIED ARTICLES IN THIS FILTER</strong>
          <p>
            The feed can still be connected. A ticker appears here only after
            deterministic materiality rules classify the article.
          </p>
        </div>}
      </div>
      <footer className="news-method">
        <strong>NO LLM · NO PROVIDER SENTIMENT-ONLY PROMOTION</strong>
        <span>
          Earnings beats, raised guidance, regulatory outcomes, material
          contracts, definitive transactions, and ownership events are hard
          catalysts. Offerings, distress, negative guidance, and reverse
          splits are risk events.
        </span>
      </footer>
    </section>
  </>;
}

function Watchlist({ items, onAdd, onRemove, onFocus }: { items: WatchItem[]; onAdd: (event: FormEvent<HTMLFormElement>) => void; onRemove: (ticker: string) => void; onFocus: (ticker: string) => void }) {
  return <><Title eyebrow="USER CONTROLLED">Watchlist</Title><form className="command watch-command" onSubmit={onAdd}><label>SYMBOL<input name="ticker" placeholder="OPK" required /></label><label>THESIS / CONDITION<input name="thesis" placeholder="Hold above VWAP; require bid support" /></label><button>ADD TO TAPE</button></form><div className="watch-grid">{items.map((item) => <article className="watch-item" key={item.ticker}><button className="symbol" onClick={() => onFocus(item.ticker)}><small>WATCHING</small><strong>{item.ticker}</strong></button><span><small>LAST</small><strong>{money(item.snapshot?.price, 3)}</strong></span><span><small>BID / ASK</small><strong>{money(item.quote?.bid_price)} / {money(item.quote?.ask_price)}</strong></span><p>{item.thesis || "No thesis recorded."}</p><button className="remove" onClick={() => onRemove(item.ticker)}>REMOVE</button></article>)}</div></>;
}
function ExecutionPanel({
  config,
  learning,
  orders,
  positions,
  plans,
  approval,
  sizing,
  focus,
  quote,
  score,
  onCreate,
  onSize,
  onPreview,
  onApprove,
  onSubmit,
  onCancel,
}: {
  config: ExecutionConfig | null;
  learning: LearningReport | null;
  orders: ExecutionOrder[];
  positions: ExecutionPosition[];
  plans: StrategyPlan[];
  approval: ExecutionApproval | null;
  sizing: SizingResult | null;
  focus: string;
  quote?: Quote;
  score?: number;
  onCreate: (event: FormEvent<HTMLFormElement>) => void;
  onSize: (event: FormEvent<HTMLFormElement>) => void;
  onPreview: (id: number) => void;
  onApprove: (id: number) => void;
  onSubmit: (id: number, confirmationText: string) => void;
  onCancel: (id: number) => void;
}) {
  const visibleOrders = config
    ? orders.filter((order) => order.mode === config.mode)
    : orders;
  const visiblePositions = config
    ? positions.filter((position) => position.mode === config.mode)
    : positions;
  const automaticEntries = Boolean(
    config?.automatic_trading &&
    (config.mode !== "live" || config.live_entries_enabled),
  );
  return <>
    <Title
      eyebrow={automaticEntries ? "AUTONOMOUS · DETERMINISTIC" : config?.automatic_trading ? "LIVE ENTRY SAFETY GATE" : "MANUAL EXECUTION"}
      stats={<div className="stats"><span><small>MODE</small><strong className={config?.mode === "live" ? "down" : "up"}>{config?.mode?.toUpperCase() ?? "—"}</strong></span><span><small>KILL SWITCH</small><strong className={config?.kill_switch ? "down" : "up"}>{config?.kill_switch ? "ACTIVE" : "READY"}</strong></span><span><small>AUTO TRADE</small><strong className={automaticEntries ? "up" : config?.automatic_trading ? "down" : ""}>{automaticEntries ? "ON" : config?.automatic_trading ? "BLOCKED" : "OFF"}</strong></span></div>}
    >Execution engine</Title>
    <div className={`execution-safety ${config?.mode === "live" ? "live" : ""}`}>
      <strong>{config?.mode === "live"
        ? automaticEntries ? "LIVE BROKER MODE" : "LIVE BROKER MODE · NEW ENTRIES BLOCKED"
        : "PAPER MODE — NO BROKER CALLS"}</strong>
      <span>{config?.automatic_trading && !automaticEntries
        ? "Market monitoring and broker synchronization remain active. Autonomous BUY entries are blocked pending shadow certification."
        : config?.automatic_trading
        ? `Continuous scan · Dynamic Top N · ${config.allowed_sessions.join(" + ")} · Session-routed auto execution · Stop / trailing exit`
        : "Risk validation → Preview → Human approval → Typed confirmation → Execution"}</span>
    </div>
    <div className={`readiness-strip ${learning?.certification.eligible ? "ready" : ""}`}>
      <span>
        <small>LIVE READINESS</small>
        <strong>{learning?.certification.decision.replaceAll("_", " ") ?? "LOADING"}</strong>
      </span>
      <span>
        <small>FORWARD SHADOW</small>
        <strong>{learning ? `${learning.strategy.valid_shadow_cycles} / 20 CYCLES` : "—"}</strong>
      </span>
      <span>
        <small>TRADING DAYS</small>
        <strong>{learning ? `${learning.strategy.shadow_trading_days} / 3` : "—"}</strong>
      </span>
      <span>
        <small>NET AFTER FEES</small>
        <strong className={(learning?.strategy.shadow_net_pnl ?? 0) >= 0 ? "up" : "down"}>
          {learning ? money(learning.strategy.shadow_net_pnl) : "—"}
        </strong>
      </span>
      <span>
        <small>PROFIT FACTOR</small>
        <strong>{learning ? learning.strategy.shadow_profit_factor.toFixed(2) : "—"}</strong>
      </span>
    </div>
    <section className="strategy-plans">
      <header><span>AUTONOMOUS STRATEGY</span><small>DYNAMIC TOP N · {config?.allowed_sessions.join(" + ") || "SUPPORTED SESSIONS"} · REALTIME WEBULL QUOTE + TICK</small></header>
      {plans.length === 0 && <p className="empty">Waiting for fresh continuously ranked candidates.</p>}
      {plans.map((plan) => <article key={`${plan.mode}:${plan.ticker}`}>
        <span><small>RANK</small><strong>#{plan.rank} · {plan.ticker}</strong></span>
        <span><small>STATE</small><strong>{plan.status.replaceAll("_", " ")}</strong></span>
        <span><small>STRATEGY</small><strong>{plan.strategy_version}</strong></span>
        <span><small>LAST / HIGH</small><strong>{money(plan.last_price, 3)} / {money(plan.session_high, 3)}</strong></span>
        <span><small>ENTRY / STOP</small><strong>{money(plan.entry_price || undefined, 3)} / {money(plan.stop_price || undefined, 3)}</strong></span>
        <span><small>TRAIL</small><strong>{money(plan.trailing_stop || undefined, 3)}</strong></span>
        <span><small>ORDER FLOW</small><strong>{plan.order_flow?.trade_ticks ?? 0} TICKS · {pct(plan.order_flow?.aggressive_buy_ratio)}</strong></span>
        <span><small>BOOK / UPTICK</small><strong>{pct(plan.order_flow?.average_book_pressure)} / {pct(plan.order_flow?.uptick_ratio)}</strong></span>
        <span><small>VELOCITY</small><strong className={(plan.order_flow?.price_velocity ?? 0) >= 0 ? "up" : "down"}>{pct(plan.order_flow?.price_velocity)}</strong></span>
        <p>{plan.last_reason ?? "Watching realtime price structure."}</p>
      </article>)}
    </section>
    {!config?.automatic_trading && <>
    <form className="sizing-command" onSubmit={onSize}>
      <label>SYMBOL<input name="ticker" defaultValue={focus} required /></label>
      <label>RISK $<input name="risk_amount" type="number" min=".01" step=".01" defaultValue="100" required /></label>
      <label>ENTRY<input name="entry_price" type="number" min=".0001" step=".0001" defaultValue={quote?.ask_price ?? quote?.bid_price} required /></label>
      <label>STOP<input name="stop_price" type="number" min=".0001" step=".0001" required /></label>
      <button>CALCULATE SIZE</button>
      <output>
        <small>RULE-BASED SIZE</small>
        <strong>{sizing ? `${compact.format(sizing.shares)} SH` : "—"}</strong>
        <span>{sizing ? `${money(sizing.estimated_cost)}${sizing.capped_by ? ` · ${sizing.capped_by.replaceAll("_", " ")}` : ""}` : "Risk ÷ stop distance"}</span>
      </output>
    </form>
    <form
      className="command execution-command"
      key={`${focus}:${quote?.ask_price ?? ""}`}
      onSubmit={onCreate}
    >
      <label>SYMBOL<input name="ticker" defaultValue={focus} required /></label>
      <label>SIDE<select name="side"><option>BUY</option><option>SELL</option></select></label>
      <label>SHARES<input name="quantity" type="number" min=".0001" step=".0001" required /></label>
      <label>LIMIT PRICE<input name="limit_price" type="number" min=".0001" step=".0001" defaultValue={quote?.ask_price ?? quote?.bid_price} required /></label>
      <label>TIME IN FORCE<select name="time_in_force"><option>DAY</option><option>GTC</option></select></label>
      <label>REASON<input name="reason" defaultValue={score != null ? `Deterministic score ${score.toFixed(1)}` : "Manual trade plan"} required /></label>
      <button>VALIDATE RISK</button>
    </form>
    </>}
    <section className="execution-orders">
      <header><span>ORDER LIFECYCLE</span><small>CURRENT MODE · EVERY TRANSITION IS AUDITED</small></header>
      {visibleOrders.length === 0 && <p className="empty">No execution orders in the current mode.</p>}
      {visibleOrders.map((order) => {
        const activeApproval = approval?.order.id === order.id ? approval : null;
        return <article key={order.id}>
          <div className="execution-order-head">
            <span><small>#{order.id} · {order.mode.toUpperCase()}</small><strong>{order.ticker}</strong></span>
            <span><small>ACTION</small><strong>{order.side} {compact.format(order.quantity)} SH</strong></span>
            <span><small>LIMIT / EST. COST</small><strong>{money(order.limit_price, 3)} · {money(order.estimated_cost)}</strong></span>
            <span><small>RISK</small><strong className={`risk-${order.risk.risk_level.toLowerCase()}`}>{order.risk.risk_level}</strong></span>
            <b className={`state state-${order.state.toLowerCase()}`}>{order.state.replaceAll("_", " ")}</b>
          </div>
          {order.risk.violations.length > 0 && <ul className="risk-violations">
            {order.risk.violations.map((violation) =>
              <li key={violation.code}><strong>{violation.code.replaceAll("_", " ")}</strong>{violation.message}</li>
            )}
          </ul>}
          <div className="execution-actions">
            {order.state === "CREATED" && <button onClick={() => onPreview(order.id)}>PREVIEW ORDER</button>}
            {order.state === "PREVIEWED" && <button onClick={() => onApprove(order.id)}>RECORD HUMAN APPROVAL</button>}
            {order.state === "APPROVED" && !activeApproval &&
              <span>Approval token is unavailable after refresh. Cancel and create a new order.</span>}
            {order.state === "APPROVED" && activeApproval && <ConfirmationForm
              approval={activeApproval}
              live={config?.mode === "live"}
              onSubmit={(text) => onSubmit(order.id, text)}
            />}
            {["CREATED", "PREVIEWED", "APPROVED", "SUBMITTED", "PARTIALLY_FILLED"].includes(order.state) &&
              <button className="cancel-order" onClick={() => onCancel(order.id)}>CANCEL</button>}
            {order.state === "FILLED" && <span>Fill recorded in position and trade journal.</span>}
          </div>
        </article>;
      })}
    </section>
    <section className="execution-positions">
      <header><span>EXECUTION POSITIONS</span><small>CURRENT MODE · PAPER AND LIVE STAY SEPARATE</small></header>
      {visiblePositions.length === 0 && <p className="empty">No execution positions in the current mode.</p>}
      {visiblePositions.map((position) => <div key={`${position.mode}:${position.ticker}`}>
        <strong>{position.ticker}</strong>
        <span>{position.mode.toUpperCase()}</span>
        <span>{compact.format(position.quantity)} SH</span>
        <span>AVG {money(position.average_cost, 3)}</span>
        <span>MARK {money(position.current_price, 3)}</span>
        <span className={position.unrealized_pnl >= 0 ? "up" : "down"}>U-PNL {money(position.unrealized_pnl)}</span>
        <span className={position.realized_pnl >= 0 ? "up" : "down"}>R-PNL {money(position.realized_pnl)}</span>
      </div>)}
    </section>
  </>;
}

function AutomaticPositions({
  items,
  brokerItems,
  plans,
  mode,
  now,
}: {
  items: ExecutionPosition[];
  brokerItems: BrokerPosition[];
  plans: StrategyPlan[];
  mode: "paper" | "live";
  now: number;
}) {
  if (mode === "live") {
    return <>
      <Title eyebrow="WEBULL TRADING API">Automatic positions</Title>
      <div className="automation-note">
        <span><small>POSITION SOURCE</small><strong>WEBULL ACCOUNT</strong></span>
        <p>Quantity, cost basis, and unrealized PnL come directly from the synchronized brokerage account. Manual position entry is disabled.</p>
      </div>
      <BrokerPositionTape items={brokerItems} now={now} />
      <section className="strategy-plans">
        <header><span>ACTIVE EXIT CONTROL</span><small>FIXED STOP · TRAILING STOP</small></header>
        {plans.filter((plan) => plan.mode === "live" && plan.status === "ENTERED").length === 0 &&
          <p className="empty">No live strategy position is currently under exit control.</p>}
        {plans.filter((plan) => plan.mode === "live" && plan.status === "ENTERED").map((plan) =>
          <article key={`${plan.mode}:${plan.ticker}`}>
            <span><small>SYMBOL</small><strong>{plan.ticker}</strong></span>
            <span><small>SIZE</small><strong>{compact.format(plan.quantity)} SH</strong></span>
            <span><small>ENTRY</small><strong>{money(plan.entry_price, 3)}</strong></span>
            <span><small>FIXED STOP</small><strong>{money(plan.stop_price, 3)}</strong></span>
            <span><small>TRAIL</small><strong>{money(plan.trailing_stop || undefined, 3)}</strong></span>
            <p>{plan.last_reason ?? "Monitoring realtime bid for exit conditions."}</p>
          </article>
        )}
      </section>
    </>;
  }
  return <>
    <Title eyebrow="PAPER FILL SYNCHRONIZED">Automatic positions</Title>
    <div className="automation-note">
      <span><small>POSITION SOURCE</small><strong>PAPER EXECUTION FILLS</strong></span>
      <p>Entries, exits, quantities, average cost, and PnL are synchronized automatically. No manual position entry is required.</p>
    </div>
    <section className="execution-positions">
      <header><span>OPEN POSITIONS</span><small>STOP AND TRAILING LEVELS FOLLOW THE STRATEGY PLAN</small></header>
      {items.length === 0 && <p className="empty">No open strategy positions.</p>}
      {items.map((position) => {
        const plan = plans.find((item) =>
          item.mode === position.mode && item.ticker === position.ticker
        );
        return <div key={`${position.mode}:${position.ticker}`}>
          <strong>{position.ticker}</strong>
          <span>{position.mode.toUpperCase()}</span>
          <span>{compact.format(position.quantity)} SH</span>
          <span>AVG {money(position.average_cost, 3)}</span>
          <span>MARK {money(position.current_price, 3)}</span>
          <span>STOP {money(plan?.stop_price, 3)}</span>
          <span>TRAIL {money(plan?.trailing_stop, 3)}</span>
          <span className={position.unrealized_pnl >= 0 ? "up" : "down"}>U-PNL {money(position.unrealized_pnl)}</span>
          <span className={position.realized_pnl >= 0 ? "up" : "down"}>R-PNL {money(position.realized_pnl)}</span>
        </div>;
      })}
    </section>
  </>;
}
function ConfirmationForm({
  approval,
  live,
  onSubmit,
}: {
  approval: ExecutionApproval;
  live: boolean;
  onSubmit: (confirmation: string) => void;
}) {
  const [value, setValue] = useState("");
  return <form className={`execution-confirm ${live ? "live" : ""}`} onSubmit={(event) => {
    event.preventDefault();
    onSubmit(value);
  }}>
    <label>
      <span>{live ? "TYPE EXACTLY TO SEND A REAL ORDER" : "TYPE EXACTLY TO FILL PAPER ORDER"}</span>
      <code>{approval.confirmation_text}</code>
      <input
        aria-label="Order confirmation phrase"
        value={value}
        onChange={(event) => setValue(event.target.value)}
        autoComplete="off"
        required
      />
    </label>
    <button disabled={value !== approval.confirmation_text}>
      {live ? "APPROVE & SEND TO WEBULL" : "APPROVE & PAPER FILL"}
    </button>
  </form>;
}
function BrokerPositionTape({ items, now }: { items: BrokerPosition[]; now: number }) {
  if (items.length === 0) return null;
  return <section className="broker-positions">
    <header><span>WEBULL SYNCED POSITIONS</span><small>READ ONLY · AUTOMATIC</small></header>
    {items.map((item) => <div key={`${item.account_id}:${item.position_id}`}>
      <strong>{item.ticker}</strong>
      <span>{compact.format(item.quantity)} SH</span>
      <span>AVG {money(item.average_price, 3)}</span>
      <span className={(item.unrealized_pnl ?? 0) >= 0 ? "up" : "down"}>{money(item.unrealized_pnl)}</span>
      <small>{age(item.synced_at, now)}</small>
    </div>)}
  </section>;
}
function Review({
  learning,
  dailyPnL,
  transactions,
  brokerOrders,
  now,
}: {
  learning: LearningReport | null;
  dailyPnL: DailyPnL[];
  transactions: ExecutionTransaction[];
  brokerOrders: BrokerOrder[];
  now: number;
}) {
  return <>
    <Title eyebrow="EVIDENCE BEFORE CAPITAL">Learning & trade review</Title>
    <LearningReportPanel report={learning} />
    <section className="daily-pnl">
      <header><span>DAILY PNL · ET</span><small>REALIZED AFTER FEES</small></header>
      {dailyPnL.length === 0 && <p className="empty">PnL appears after the first synchronized fill.</p>}
      {dailyPnL.map((day) => <article key={`${day.mode}:${day.trading_date}`}>
        <span><small>DATE</small><strong>{day.trading_date}</strong></span>
        <span><small>MODE</small><strong>{day.mode.toUpperCase()}</strong></span>
        <span><small>GROSS</small><strong>{money(day.gross_pnl)}</strong></span>
        <span><small>FEES</small><strong>{money(day.fees)}</strong></span>
        <span><small>NET</small><strong className={day.net_pnl >= 0 ? "up" : "down"}>{money(day.net_pnl)}</strong></span>
        <span><small>ENTRY / EXIT</small><strong>{day.entries} / {day.exits}</strong></span>
      </article>)}
    </section>
    <section className="transaction-ledger">
      <header><span>ENTRY / EXIT LEDGER</span><small>BROKER + SHADOW FILLS</small></header>
      <div className="transaction-row heading">
        <span>TIME ET</span><span>SYMBOL</span><span>ACTION</span><span>SIZE</span>
        <span>PRICE</span><span>FEE</span><span>REALIZED</span><span>STRATEGY / REASON</span>
      </div>
      {transactions.length === 0 && <p className="empty">No synchronized transactions yet.</p>}
      {transactions.map((item) => <div className="transaction-row" key={item.id}>
        <span>{new Intl.DateTimeFormat("en-US", {
          timeZone: "America/New_York", month: "short", day: "2-digit",
          hour: "2-digit", minute: "2-digit", second: "2-digit",
        }).format(new Date(item.executed_at))}</span>
        <strong>{item.ticker}</strong>
        <span className={item.side === "BUY" ? "up" : "down"}>{item.side === "BUY" ? "ENTRY" : "EXIT"}</span>
        <span>{compact.format(item.quantity)} SH</span>
        <span>{money(item.price, 4)}</span>
        <span>{money(item.fee)}</span>
        <span className={(item.realized_pnl ?? 0) >= 0 ? "up" : "down"}>{money(item.realized_pnl)}</span>
        <span><strong>{item.strategy} · {item.source}</strong><small>{item.reason ?? "Broker synchronized fill"}</small></span>
      </div>)}
    </section>
    <BrokerOrderTape items={brokerOrders} now={now} />
  </>;
}

function LearningReportPanel({ report }: { report: LearningReport | null }) {
  if (!report) {
    return <section className="learning-report">
      <header><span>LEARNING REPORT</span><small>LOADING EVIDENCE</small></header>
      <p className="empty">Waiting for model and strategy evidence.</p>
    </section>;
  }
  const modelCard = (
    label: string,
    model: LearningReport["champion"] | LearningReport["challenger"],
  ) => <article className="learning-model">
    <small>{label}</small>
    {model ? <>
      <strong>{model.name} · #{model.id}</strong>
      <span>{model.algorithm.toUpperCase()}</span>
      <p>TRAIN {model.trained_from.slice(0, 10)} → {model.trained_to.slice(0, 10)}</p>
      <p>LOGLOSS {model.validation_metrics?.log_loss?.toFixed?.(4) ?? "—"} · N {compact.format(model.validation_metrics?.sample_count ?? 0)}</p>
    </> : <strong>NO MODEL</strong>}
  </article>;
  const coverageTime = (value?: string) => value
    ? new Intl.DateTimeFormat("en-US", {
      timeZone: "America/New_York",
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: "h23",
    }).format(new Date(value))
    : "—";
  return <section className="learning-report">
    <header>
      <span>LEARNING REPORT · {report.coverage.trading_date?.slice(0, 10) || "NO DATE"}</span>
      <small className={report.certification.eligible ? "up" : "down"}>
        STRATEGY PROMOTION {report.certification.decision.replaceAll("_", " ")}
      </small>
    </header>
    <div className="learning-models">
      {modelCard("CHAMPION", report.champion)}
      {modelCard("LATEST CHALLENGER", report.challenger)}
      {modelCard("SPIKE EXPERIMENT", report.spike_model)}
      <article className="learning-decision">
        <small>REGISTRY DECISION</small>
        <strong>{report.last_decision?.decision.toUpperCase() ?? "NO DECISION"}</strong>
        <p>{report.last_decision?.reason ?? "No learning run recorded."}</p>
      </article>
    </div>
    {report.spike_evaluation && <>
      <div className="strategy-evidence">
        <span>
          <small>EVIDENCE</small>
          <strong className={report.spike_evaluation.evidence.point_in_time_causal ? "up" : "warn"}>
            {report.spike_evaluation.evidence.kind.replaceAll("_", " ")}
          </strong>
          <small>
            {report.spike_evaluation.evidence.point_in_time_causal
              ? "MODEL + RANKING AVAILABLE BEFORE SESSION"
              : "RESEARCH ONLY · NOT A DEPLOYED PREDICTION"}
          </small>
        </span>
        {report.spike_evaluation.thresholds.map((threshold) => {
          const top20 = threshold.top_k.find((metric) => metric.k === 20);
          const top100 = threshold.top_k.find((metric) => metric.k === 100);
          return <span key={threshold.threshold}>
            <small>SPIKE ≥{Math.round(threshold.threshold * 100)}%</small>
            <strong>{threshold.positives} ACTUAL</strong>
            <small>R@20 {Math.round((top20?.recall ?? 0) * 100)}% · R@100 {Math.round((top100?.recall ?? 0) * 100)}%</small>
            <small>
              SCAN EARLY {threshold.scanner_hits} · LATE {threshold.scanner_late_hits}
              {" "}· UNKNOWN {threshold.scanner_unknown_hits} · MISSED {threshold.scanner_misses}
            </small>
            <small>
              PICK EARLY {threshold.dynamic_hits} · LATE {threshold.dynamic_late_hits}
              {" "}· UNKNOWN {threshold.dynamic_unknown_hits} · MISSED {threshold.dynamic_misses}
            </small>
            {threshold.excluded_corporate_action > 0 && <small className="warn">
              EXCLUDED CORPORATE ACTION {threshold.excluded_corporate_action}
            </small>}
          </span>;
        })}
      </div>
      <div className="strategy-evidence">
        {report.spike_evaluation.patterns.map((pattern) => <span
          key={`${pattern.dimension}:${pattern.value}`}
        >
          <small>{pattern.dimension.replaceAll("_", " ")}</small>
          <strong>{pattern.value.replaceAll("_", " ")} · {pattern.count}</strong>
          <small>AVG MOVE {pct(pattern.average_return)} · REMAINING {pct(pattern.average_remaining_upside)}</small>
        </span>)}
      </div>
      <div className="strategy-evidence">
        {report.spike_evaluation.outcomes.slice(0, 8).map((outcome) => <span
          key={outcome.ticker}
        >
          <small>#{outcome.rank} · {outcome.origin_session.replaceAll("_", " ")}</small>
          <strong>{outcome.ticker} · {pct(outcome.return)}</strong>
          <small>
            OPEN {pct(outcome.open_return)} · {outcome.discovery_phase.replaceAll("_", " ")}
          </small>
          <small>{outcome.price_action_pattern.replaceAll("_", " ")}</small>
        </span>)}
      </div>
    </>}
    <div className="learning-coverage">
      <span><small>DAILY BARS</small><strong>{compact.format(report.coverage.daily_bars)}</strong></span>
      <span><small>FEATURES</small><strong>{compact.format(report.coverage.feature_snapshots)}</strong></span>
      <span><small>RANKINGS</small><strong>{compact.format(report.coverage.candidate_rankings)}</strong></span>
      <span><small>QUOTES</small><strong>{compact.format(report.coverage.market_quotes)}</strong><small>{compact.format(report.coverage.market_quote_tickers)} SYMBOLS</small></span>
      <span><small>TICKS</small><strong>{compact.format(report.coverage.market_ticks)}</strong><small>{compact.format(report.coverage.market_tick_tickers)} SYMBOLS</small></span>
      <span><small>SIGNALS</small><strong>{compact.format(report.coverage.scanner_signals)}</strong></span>
      <span><small>NEWS / SEC</small><strong>{report.coverage.news_items} / {report.coverage.sec_filings}</strong></span>
      <span><small>ORDERS / FILLS</small><strong>{report.coverage.execution_orders} / {report.coverage.execution_fills}</strong></span>
      <span><small>STRATEGY CYCLES</small><strong>{report.coverage.strategy_cycles}</strong></span>
      <span>
        <small>MARKET WINDOW ET</small>
        <strong>{coverageTime(report.coverage.first_market_event)} → {coverageTime(report.coverage.last_market_event)}</strong>
      </span>
    </div>
    <div className="strategy-evidence">
      <span>
        <small>FORWARD WINDOW</small>
        <strong>{report.strategy.shadow_trading_days} DAYS</strong>
        <small>
          {report.strategy.shadow_first_trading_date?.slice(0, 10) ?? "—"}
          {" "}→{" "}
          {report.strategy.shadow_last_trading_date?.slice(0, 10) ?? "—"}
        </small>
      </span>
      <span><small>VALID SHADOW</small><strong>{report.strategy.valid_shadow_cycles}</strong><small>{report.strategy.excluded_cycles} EXCLUDED</small></span>
      <span><small>NET AFTER FEES</small><strong className={report.strategy.shadow_net_pnl >= 0 ? "up" : "down"}>{money(report.strategy.shadow_net_pnl)}</strong><small>FEES {money(report.strategy.shadow_fees)}</small></span>
      <span><small>PROFIT FACTOR</small><strong className={report.strategy.shadow_profit_factor >= 1.15 ? "up" : "down"}>{report.strategy.shadow_profit_factor.toFixed(2)}</strong><small>GROSS +{money(report.strategy.shadow_gross_profit)} / -{money(report.strategy.shadow_gross_loss)}</small></span>
      <span><small>STRATEGY VERSION</small><strong title={report.strategy.shadow_strategy_version}>{report.strategy.shadow_strategy_version?.slice(-16) || "—"}</strong><small>LATEST 100 CLOSED CYCLES</small></span>
    </div>
    <div className="strategy-evidence monitors">
      <span><small>ACTUAL-FILL REPLAY</small><strong>{report.strategy.replay_matched_closed} / {report.strategy.replay_cycles}</strong><small>{report.strategy.replay_inconclusive} INCONCLUSIVE</small></span>
      <span><small>OBSERVED LIVE NET</small><strong className={report.strategy.actual_net_pnl >= 0 ? "up" : "down"}>{money(report.strategy.actual_net_pnl)}</strong><small>POST-LIVE MONITOR</small></span>
      <span><small>CHALLENGER Δ</small><strong className={report.strategy.challenger_net_pnl_delta > 0 ? "up" : "down"}>{money(report.strategy.challenger_net_pnl_delta)}</strong><small>POST-LIVE MONITOR</small></span>
    </div>
    <div className="certification-grid">
      {report.certification.checks.map((item) => <article
        className={`${item.passed ? "passed" : "failed"} ${item.required ? "gate" : "monitor"}`}
        key={item.code}
      >
        <i>{item.passed ? "✓" : "×"}</i>
        <span>
          <strong>{item.label}</strong>
          <small>{item.required ? "PROMOTION GATE" : "POST-LIVE MONITOR"} · {item.detail}</small>
        </span>
      </article>)}
    </div>
  </section>;
}
function BrokerOrderTape({ items, now }: { items: BrokerOrder[]; now: number }) {
  if (items.length === 0) return null;
  return <section className="broker-positions">
    <header><span>WEBULL ORDER / FILL HISTORY</span><small>READ ONLY · FEES INCLUDED</small></header>
    {items.map((item) => <div key={`${item.account_id}:${item.client_order_id}`}>
      <strong>{item.ticker}</strong>
      <span>{item.side} · {item.status}</span>
      <span>{compact.format(item.filled_quantity)} / {compact.format(item.total_quantity)} SH</span>
      <span>{money(item.filled_price, 3)} · FEE {money(item.commission + item.fees)}</span>
      <small>{age(item.filled_at ?? item.placed_at ?? item.synced_at, now)}</small>
    </div>)}
  </section>;
}
function OrderBook({ quote }: { quote?: Quote }) {
  const total = (quote?.bid_size ?? 0) + (quote?.ask_size ?? 0);
  const bidWidth = total ? ((quote?.bid_size ?? 0) / total) * 100 : 50;
  return <div className="book"><header><span>TOP OF BOOK</span><small>{quote?.source?.toUpperCase() ?? "NO DATA"}</small></header><div className="book-row ask"><span>ASK</span><strong>{money(quote?.ask_price)}</strong><span>{quote ? compact.format(quote.ask_size) : "—"}</span><i style={{ width: `${100 - bidWidth}%` }} /></div><div className="spread"><span>SPREAD</span><strong>{quote ? `$${(quote.ask_price - quote.bid_price).toFixed(2)}` : "—"}</strong><span>{quote?.bid_size ? `ASK ${(quote.ask_size / quote.bid_size).toFixed(1)}× BID` : "—"}</span></div><div className="book-row bid"><span>BID</span><strong>{money(quote?.bid_price)}</strong><span>{quote ? compact.format(quote.bid_size) : "—"}</span><i style={{ width: `${bidWidth}%` }} /></div></div>;
}
function EntryPlan({
  plan,
  selected,
  quoteFresh,
  sessionWaiting,
}: {
  plan?: StrategyPlan;
  selected: boolean;
  quoteFresh: boolean;
  sessionWaiting: boolean;
}) {
  const status = plan?.status ?? (selected ? "WATCH" : "OBSERVE");
  const labels: Record<string, string> = {
    OBSERVE: "NOT ACTIONABLE",
    WATCH: "WATCHING",
    PULLBACK: "WAITING PULLBACK",
    PENDING_ENTRY: "ORDER PENDING",
    ENTERED: "POSITION OPEN",
    PENDING_EXIT: "EXIT PENDING",
    CLOSED: "CLOSED",
    INVALIDATED: "INVALIDATED",
  };
  let reason = plan?.last_reason ??
    (selected
      ? "Ranked in Top N; waiting for the deterministic strategy state."
      : "Outside the current dynamic Top N.");
  if (sessionWaiting) {
    reason = "Market is closed; monitoring resumes at the next allowed session.";
  } else if (!quoteFresh) {
    reason = "Webull book is stale; no entry decision can be made.";
  }
  const active = ["PENDING_ENTRY", "ENTERED", "PENDING_EXIT"].includes(status);
  return <div className={`entry ${active ? "" : "blocked"}`}>
    <header><span>STRATEGY GATE</span><small>{status.replaceAll("_", " ")}</small></header>
    <div>
      <b>{active ? "✓" : "×"}</b>
      <p><strong>{labels[status] ?? status.replaceAll("_", " ")}</strong>{reason}</p>
    </div>
    {plan && <div>
      <b>↳</b>
      <p>
        <strong>Risk map</strong>
        Entry {money(plan.entry_price || undefined, 3)} ·
        stop {money(plan.stop_price || undefined, 3)} ·
        trail {money(plan.trailing_stop || undefined, 3)}
      </p>
    </div>}
    <footer>
      <span>{quoteFresh ? "FRESH WEBULL BOOK" : "STALE BOOK"}</span>
      <strong>{labels[status] ?? status.replaceAll("_", " ")}</strong>
      <span>NO MARKET CHASE</span>
    </footer>
  </div>;
}
