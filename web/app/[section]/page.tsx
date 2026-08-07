import { notFound } from "next/navigation";
import { NAV_KEYS } from "../nav";
import { TradeEdgeApp } from "../TradeEdge";

// One route per rail item so a refresh lands where the trader was. The
// alternative -- section in client state -- loses the page on every reload and
// makes a screen impossible to link to, which is most of what a URL is for.
export function generateStaticParams() {
  return NAV_KEYS.map((section) => ({ section }));
}

export default async function SectionPage({
  params,
}: {
  params: Promise<{ section: string }>;
}) {
  const { section } = await params;
  if (!NAV_KEYS.includes(section)) notFound();
  return <TradeEdgeApp section={section} />;
}
