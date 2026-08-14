import { RiskView } from "../Risk";

/* Named for what it governs rather than for the fact that it has fields on it.
 * "Settings" describes the widget; risk and controls describe what the page decides,
 * and the hub card has always called it that. */
export default function RiskPage() {
  return <RiskView />;
}
