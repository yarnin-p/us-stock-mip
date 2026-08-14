import { BracketView } from "../../Bracket";

/* One bracket, full-bleed like the terminal. Reached from the plans list and from
 * Positions & Orders, which is what the breadcrumb says. */
export default async function BracketPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;
  return <BracketView id={Number(id)} />;
}
