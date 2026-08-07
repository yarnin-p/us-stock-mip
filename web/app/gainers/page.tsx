"use client";

import GainersBoard from "../GainersBoard";

// A route of its own because this is the screen that gets read the morning
// after, not during a setup: the history of what ran, and what those names had
// in common, deserves the whole width rather than a panel on the dashboard.
const API =
  process.env.NEXT_PUBLIC_API_URL?.replace(/\/$/, "") ?? "http://127.0.0.1:8080";

export default function GainersPage() {
  return (
    <main className="gainers-page">
      <GainersBoard api={API} />
    </main>
  );
}
