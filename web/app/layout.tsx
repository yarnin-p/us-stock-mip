import type { Metadata } from "next";
import "./globals.css";
import "./tradeedge.css";

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
