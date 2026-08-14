/* The vocabulary of a bracket's life, in one place.
 *
 * It was spread as string literals across four screens, and every one of them had to
 * be found and changed by hand when the words changed. That is the shape that lets a
 * screen keep asking about a state the server stopped sending -- the comparison does
 * not fail, it simply never matches, and a row quietly stops appearing.
 *
 * The words mirror internal/bracket/bracket.go exactly, and the two predicates mirror
 * State.Holding and State.Live there. Keep them in step: the server decides, and a
 * screen that disagrees is a screen that lies.
 */
export type BracketState =
  | "DRAFT"
  | "REFUSED"
  | "WORKING"
  | "PROTECTED"
  | "UNPROTECTED"
  | "STOPPED"
  | "TARGET_HIT"
  | "CANCELLED";

/** Holding answers the first question: is there stock in the account right now. */
export function holding(state: string): boolean {
  return state === "PROTECTED" || state === "UNPROTECTED";
}

/** Live answers whether this bracket still occupies its ticker -- a plan that could
 *  be sent, a buy at the venue, or stock held. */
export function live(state: string): boolean {
  return (
    state === "DRAFT" ||
    state === "WORKING" ||
    state === "PROTECTED" ||
    state === "UNPROTECTED"
  );
}

/* What the operator reads, and how loud it is.
 *
 * label is deliberately not the constant: DRAFT and TARGET_HIT are how a database
 * spells things. tone drives the colour, and `alarm` exists for exactly one state --
 * stock held with nothing behind it is the only condition on this screen that should
 * be able to interrupt someone. */
export const STATE_LABEL: Record<
  string,
  { label: string; tone: "quiet" | "working" | "good" | "alarm" | "done"; hint: string }
> = {
  DRAFT: {
    label: "Draft",
    tone: "quiet",
    hint: "written, nothing sent",
  },
  REFUSED: {
    label: "Refused",
    tone: "alarm",
    hint: "the risk gate turned this down",
  },
  WORKING: {
    label: "Buying",
    tone: "working",
    hint: "a buy is live at the broker",
  },
  PROTECTED: {
    label: "Protected",
    tone: "good",
    hint: "stop and target are resting at the broker",
  },
  UNPROTECTED: {
    label: "UNPROTECTED",
    tone: "alarm",
    hint: "these shares are held with nothing behind them",
  },
  STOPPED: {
    label: "Stopped",
    tone: "done",
    hint: "the stop filled",
  },
  TARGET_HIT: {
    label: "Target hit",
    tone: "done",
    hint: "the target filled",
  },
  CANCELLED: {
    label: "Cancelled",
    tone: "done",
    hint: "abandoned",
  },
};

/** describe never returns undefined: an unknown state is a server this build does not
 *  understand, and saying so is better than rendering a blank cell. */
export function describe(state: string) {
  return (
    STATE_LABEL[state] ?? {
      label: state || "—",
      tone: "quiet" as const,
      hint: "this build does not know this state",
    }
  );
}
