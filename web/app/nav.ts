// The rail definition lives in a plain module because a server component needs
// the real array to validate a route. Exported from a "use client" file it
// arrives as a client reference, not data, and `.includes` is not a function on
// a reference — which is exactly how the dynamic route first broke.

export const NAV = [
  { key: "market", label: "Market Overview", icon: "M3 3v18h18|M7 14l3-4 3 3 5-7" },
  { key: "scanner", label: "Daily Scanner", icon: "M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14z|M20 20l-4-4" },
  { key: "gainers", label: "Gainers", icon: "M3 17l6-6 4 4 8-8|M15 7h6v6" },
  { key: "terminal", label: "Order Terminal", icon: "M3 5h18v14H3z|M7 10l2 2-2 2|M12 14h5" },
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

export type NavKey = (typeof NAV)[number]["key"];

export const NAV_KEYS: readonly string[] = NAV.map((item) => item.key);
