import { SettingsView } from "../Settings";

/* Full-bleed like the terminal and the hub: a static segment beats the [section]
 * route, so /settings lands here rather than inside the rail shell. */
export default function SettingsPage() {
  return <SettingsView />;
}
