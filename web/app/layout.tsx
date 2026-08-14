import type { Metadata } from "next";
import "./globals.css";
import "./tradeedge.css";
// After tradeedge.css so it can re-point that file's --te-* variables, and before
// terminal.css so the terminal's own rules still win over anything set here. Scoped
// to .tg: every other screen keeps the warm-white surface.
import "./theme-graphite.css";
import "./terminal.css";
import "./terminal-shape.css";

export const metadata: Metadata = {
  title: "TradeEdge — Momentum Suite",
  description: "A decision console for US momentum trading.",
  icons: { icon: "/favicon.svg", shortcut: "/favicon.svg" },
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
