"use client";

import TicketPanel from "../TicketPanel";

// A route of its own so the ticket is one address away with nothing competing
// for the screen. During a fast setup, scrolling past charts to reach it is
// exactly the delay this was built to remove.
const API =
  process.env.NEXT_PUBLIC_API_URL?.replace(/\/$/, "") ?? "http://127.0.0.1:8080";

export default function TicketPage() {
  return (
    <main className="ticket-page">
      <TicketPanel api={API} />
    </main>
  );
}
