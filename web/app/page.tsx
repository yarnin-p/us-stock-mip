import { HubView } from "./Hub";

/* The hub is the front door.
 *
 * It used to be the old dashboard, which answered a question nobody opens the app to
 * ask — the first thing you want is where to go and what happened while you were
 * away, and that is what the hub is for. The dashboard section is gone from the rail
 * with it rather than left as a second, worse version of this page.
 */
export default function Home() {
  return <HubView />;
}
