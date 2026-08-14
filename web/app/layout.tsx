import type { Metadata } from "next";
import "./globals.css";
import "./tradeedge.css";
// Before terminal.css, so the terminal's own rules win over a token default rather
// than the other way round. The token layer is scoped to .tg and paints nothing on
// its own -- every other screen is untouched by its presence here.
import "./theme-graphite.css";
import "./terminal.css";

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
