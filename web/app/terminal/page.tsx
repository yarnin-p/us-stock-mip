import { TerminalView } from "../Terminal";

/* The terminal is its own page, not a section inside the rail.
 *
 * The handoff is explicit about this: the screen is full-bleed and the way back is
 * the "All apps" pill in its own portal bar, which returns to the hub. Rendering it
 * inside the rail was my addition, and it was wrong -- it put a second, competing
 * navigation next to the one the design already provides.
 *
 * A static segment beats the [section] route, so /terminal lands here rather than in
 * the shell. */
export default function TerminalPage() {
  return <TerminalView />;
}
