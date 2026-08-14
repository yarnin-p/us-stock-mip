"use client";

/* What the engine is doing, as it does it.
 *
 * Polling was the first answer and it was the wrong one: a three-second timer is
 * three seconds of not knowing, and the moments this screen exists for -- a stop
 * ratcheting up, a fill arriving, a stop being refused -- are exactly the moments
 * where that gap is felt. The server already runs an event stream for cache
 * invalidation; this rides it and reads the news the bracket engine puts on it.
 *
 * One connection for the whole tab. Every hook here shares it, because a browser
 * allows a handful of connections per origin and a screen with four panels open
 * would otherwise spend them all on the same stream.
 */

import { useEffect, useRef, useState } from "react";

const API = process.env.NEXT_PUBLIC_API_BASE ?? "http://localhost:8080";

export type BracketEvent = {
  scope: string;
  /** The rung or the act: BREAK_EVEN, ENTRY_SENT, STOP_FIRED. */
  operation?: string;
  /** The bracket's state after it, as the server spells it. */
  subject?: string;
  ticker?: string;
  id?: number;
  detail?: string;
  price?: number;
  level?: number;
  /** false when the venue refused what this describes. */
  applied?: boolean;
  occurred_at: string;
};

type Listener = (event: BracketEvent) => void;

let stream: EventSource | null = null;
const listeners = new Set<Listener>();

function ensureStream() {
  if (stream || typeof window === "undefined") return;
  stream = new EventSource(`${API}/events`);
  stream.addEventListener("change", (message) => {
    try {
      const event = JSON.parse((message as MessageEvent).data) as BracketEvent;
      // Only what the bracket engine raised. The same stream carries cache
      // invalidations for half the console, and a panel that reacted to all of it
      // would redraw on every scan.
      if (event.scope !== "bracket") return;
      listeners.forEach((listener) => listener(event));
    } catch {
      // A malformed frame is not worth taking the stream down for; the next one
      // will be fine, and the screen's own refetch is the backstop.
    }
  });
  stream.addEventListener("error", () => {
    // EventSource reconnects on its own. Closing here would turn a blip into a
    // permanently dead stream, which is worse than a gap.
  });
}

/* useBracketEvents calls back on every bracket event, newest first in the list it
 * also returns.
 *
 * The callback is held in a ref so a caller can pass an inline function without
 * resubscribing on every render -- the common shape, and the one that silently
 * reopens the connection sixty times a second if it is done wrong.
 */
export function useBracketEvents(
  onEvent?: (event: BracketEvent) => void,
  keep = 40,
): BracketEvent[] {
  const [events, setEvents] = useState<BracketEvent[]>([]);
  const handler = useRef(onEvent);
  handler.current = onEvent;

  useEffect(() => {
    ensureStream();
    const listener: Listener = (event) => {
      setEvents((current) => [event, ...current].slice(0, keep));
      handler.current?.(event);
    };
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  }, [keep]);

  return events;
}

/* TRIGGER_COPY turns the domain's word into the operator's.
 *
 * The words are the server's -- BREAK_EVEN, ENTRY_SENT -- because inventing a second
 * vocabulary on the way to the screen is how a log and a dashboard come to describe
 * the same event differently. What changes here is only the reading.
 */
export const TRIGGER_COPY: Record<string, string> = {
  ENTRY_SENT: "Buy sent",
  ENTRY_REFUSED: "Buy refused",
  ENTRY_FILLED: "Filled — protection placed",
  ENTRY_CANCELLED: "Buy ended without filling",
  ENTRY_TOPPED_UP: "More filled — protection resized",
  ENTRY_EXPOSED: "NO STOP AT THE BROKER",
  INITIAL: "Protection placed",
  BREAK_EVEN: "Stop lifted to break-even",
  PROFIT_LOCK: "Stop lifted to lock profit",
  TRAIL_STOP: "Stop trailed up",
  TRAIL_TARGET: "Target widened",
  PARTIAL_TP: "Partial taken",
  STOP_FIRED: "Stop fired",
  STOP_HANDOVER: "Stop changed hands",
  FILLED: "Position closed",
  MANUAL: "Changed by hand",
};

export function readTrigger(operation?: string): string {
  if (!operation) return "Changed";
  return TRIGGER_COPY[operation] ?? operation.replace(/_/g, " ").toLowerCase();
}
