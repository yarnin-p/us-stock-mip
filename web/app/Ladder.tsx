"use client";

/* The exit ladder, in one place.
 *
 * It was written into the order terminal, and when the bracket screen needed the same
 * thing I built a second set of rung cards that looked like it. That is the version of
 * this mistake that costs money rather than tidiness: the two drift, and the screen you
 * adjust a live position on stops matching the screen you planned it on -- a field
 * missing here, a label saying something slightly different there, while both claim to
 * be the same ladder.
 *
 * So there is one. The submit screen starts it from a preset; the bracket screen starts
 * it from the config the bracket is actually running. Everything after that -- the
 * markup, the ordering rules, the units -- is identical, because it is the same code.
 *
 * Units: percent on screen, fractions on the wire. 3 means 3%, and the conversion
 * happens at the edge in configFromLadder/ladderFromConfig rather than being spread
 * through the fields.
 */

import { sanitizeDecimal, sanitizeInteger } from "./inputs";

export type LadderValues = {
  breakEvenOn: boolean;
  breakEvenAfter: string;
  breakEvenFloor: string;
  profitLockOn: boolean;
  profitLockAfter: string;
  profitLockFloor: string;
  trailStopAfter: string;
  trailTargetAfter: string;
  trailStopDistance: string;
  trailTargetDistance: string;
  partialOn: boolean;
  partialAfter: string;
  partialFraction: string;
  partialMinShares: string;
};

/* Three shapes of the same ladder, from the handoff. They are a starting point, not a
 * recommendation: what separates them is how much of an open gain the trail is allowed
 * to give back before the floors take over.
 *
 * Every one of them satisfies the ordering the engine enforces -- floors below their
 * own activation, break-even before profit lock, profit lock before the trail, partial
 * above the trail -- so picking one can never produce the 400 that ladderErrorOf exists
 * to explain. That was checked by hand against all five rules when these were written;
 * there is no test framework in web/ yet to hold it, so editing a number here means
 * re-checking it against ladderErrorOf below. */
export const LADDER_PRESETS = {
  conservative: {
    label: "conservative",
    note: "banks capital early, gives the runner little room",
    breakEven: { after: "2", floor: "1" },
    profitLock: { after: "4", floor: "2.5" },
    trail: { stopAfter: "8", targetAfter: "16", stopDistance: "7", targetDistance: "12" },
    partial: { after: "20", fraction: "40", minShares: "10" },
  },
  balanced: {
    label: "balanced",
    note: "the middle setting",
    breakEven: { after: "3", floor: "1.5" },
    profitLock: { after: "6", floor: "3" },
    trail: { stopAfter: "10", targetAfter: "20", stopDistance: "10", targetDistance: "15" },
    partial: { after: "30", fraction: "25", minShares: "10" },
  },
  runner: {
    label: "runner",
    note: "accepts a deeper give-back to hold a long runner",
    breakEven: { after: "4", floor: "1" },
    profitLock: { after: "10", floor: "4" },
    trail: { stopAfter: "14", targetAfter: "28", stopDistance: "16", targetDistance: "22" },
    partial: { after: "45", fraction: "20", minShares: "10" },
  },
} as const;

export type PresetName = keyof typeof LADDER_PRESETS;

/** presetLadder turns a preset into a full set of values, every rung armed. */
export function presetLadder(name: PresetName): LadderValues {
  const shape = LADDER_PRESETS[name];
  return {
    breakEvenOn: true,
    breakEvenAfter: shape.breakEven.after,
    breakEvenFloor: shape.breakEven.floor,
    profitLockOn: true,
    profitLockAfter: shape.profitLock.after,
    profitLockFloor: shape.profitLock.floor,
    trailStopAfter: shape.trail.stopAfter,
    trailTargetAfter: shape.trail.targetAfter,
    trailStopDistance: shape.trail.stopDistance,
    trailTargetDistance: shape.trail.targetDistance,
    partialOn: true,
    partialAfter: shape.partial.after,
    partialFraction: shape.partial.fraction,
    partialMinShares: shape.partial.minShares,
  };
}

/* Which pill is lit is read back from the fields rather than remembered from the
 * click. Remembering it means a highlighted "balanced" can sit over numbers that were
 * edited afterwards -- a label making a claim about the ladder that the ladder no
 * longer supports. Derived, it cannot drift: change one number and no pill is lit,
 * change it back and the pill returns. */
export function activePresetOf(values: LadderValues): PresetName | null {
  const names = Object.keys(LADDER_PRESETS) as PresetName[];
  return (
    names.find((name) => {
      const shape = presetLadder(name);
      return (
        shape.breakEvenAfter === values.breakEvenAfter &&
        shape.breakEvenFloor === values.breakEvenFloor &&
        shape.profitLockAfter === values.profitLockAfter &&
        shape.profitLockFloor === values.profitLockFloor &&
        shape.trailStopAfter === values.trailStopAfter &&
        shape.trailTargetAfter === values.trailTargetAfter &&
        shape.trailStopDistance === values.trailStopDistance &&
        shape.trailTargetDistance === values.trailTargetDistance &&
        shape.partialAfter === values.partialAfter &&
        shape.partialFraction === values.partialFraction &&
        shape.partialMinShares === values.partialMinShares &&
        values.breakEvenOn &&
        values.profitLockOn &&
        values.partialOn
      );
    }) ?? null
  );
}

/* The order of the rungs is the whole design, and getting it wrong is a 400 from the
 * server with no clue attached. Checking it here turns that into a sentence that says
 * which two numbers are in the wrong order. The server is still the authority -- this
 * only saves a round trip to be told so. */
export function ladderErrorOf(values: LadderValues): string {
  const trail = Number(values.trailStopAfter);
  const be = Number(values.breakEvenAfter);
  const lock = Number(values.profitLockAfter);
  if (values.breakEvenOn && !(Number(values.breakEvenFloor) < be)) {
    return "Break-even: the floor must sit below the gain that arms it, or it is a target rather than a floor.";
  }
  if (values.profitLockOn && !(Number(values.profitLockFloor) < lock)) {
    return "Profit lock: the floor must sit below the gain that arms it.";
  }
  if (values.breakEvenOn && values.profitLockOn && !(be < lock)) {
    return "Break-even must arm before profit lock — the ladder only climbs.";
  }
  if (values.profitLockOn && !(lock < trail)) {
    return "Profit lock must arm before the trail.";
  }
  if (values.partialOn && !(Number(values.partialAfter) > trail)) {
    return "Partial take-profit must arm above the trail — selling before the trail engages cuts into the runner the trail exists to hold.";
  }
  return "";
}

/** BracketConfig is the ladder as the server holds it: fractions, not percents. */
export type BracketConfig = Record<string, number>;

const asPercent = (value: unknown): string => {
  const number = Number(value);
  if (!Number.isFinite(number) || number === 0) return "0";
  // Rounded to four places because 0.015 * 100 is 1.4999999999999998, and a field
  // that opens showing that is a field nobody trusts.
  return String(Math.round(number * 100 * 10000) / 10000);
};

/* ladderFromConfig reads what a bracket is actually running.
 *
 * A rung is off when its activation is zero, which is how the server says "not armed"
 * -- the fields keep the preset's numbers underneath so switching it back on does not
 * present a row of zeros to fill in. */
export function ladderFromConfig(config: BracketConfig | null | undefined): LadderValues {
  const base = presetLadder("balanced");
  if (!config) return base;
  const on = (key: string) => Number(config[key]) > 0;
  const value = (key: string, fallback: string) =>
    Number(config[key]) > 0 ? asPercent(config[key]) : fallback;
  return {
    breakEvenOn: on("BreakEvenAfter"),
    breakEvenAfter: value("BreakEvenAfter", base.breakEvenAfter),
    breakEvenFloor: value("BreakEvenFloor", base.breakEvenFloor),
    profitLockOn: on("ProfitLockAfter"),
    profitLockAfter: value("ProfitLockAfter", base.profitLockAfter),
    profitLockFloor: value("ProfitLockFloor", base.profitLockFloor),
    trailStopAfter: value("TrailStopAfter", base.trailStopAfter),
    trailTargetAfter: value("TrailTargetAfter", base.trailTargetAfter),
    trailStopDistance: value("TrailStopDistance", base.trailStopDistance),
    trailTargetDistance: value("TrailTargetDistance", base.trailTargetDistance),
    partialOn: on("PartialTPAfter"),
    partialAfter: value("PartialTPAfter", base.partialAfter),
    partialFraction: value("PartialTPFraction", base.partialFraction),
    // A share count, not a percentage: it goes over the wire as itself.
    partialMinShares:
      Number(config.PartialTPMinShares) > 0
        ? String(Math.round(Number(config.PartialTPMinShares)))
        : base.partialMinShares,
  };
}

/* configFromLadder writes it back, merged over what the bracket already has so any
 * field this screen does not show -- the stop-loss percent, the minimum step, the fee
 * -- survives an edit rather than being silently zeroed.
 *
 * A rung that is off sends zero for its activation, which is how the server is told it
 * is not armed. Sending the floor without the activation is refused, and rightly. */
export function configFromLadder(
  values: LadderValues, existing: BracketConfig | null | undefined,
): BracketConfig {
  const pct = (input: string) => Number(input) / 100;
  return {
    ...(existing ?? {}),
    BreakEvenAfter: values.breakEvenOn ? pct(values.breakEvenAfter) : 0,
    BreakEvenFloor: values.breakEvenOn ? pct(values.breakEvenFloor) : 0,
    ProfitLockAfter: values.profitLockOn ? pct(values.profitLockAfter) : 0,
    ProfitLockFloor: values.profitLockOn ? pct(values.profitLockFloor) : 0,
    TrailStopAfter: pct(values.trailStopAfter),
    TrailTargetAfter: pct(values.trailTargetAfter),
    TrailStopDistance: pct(values.trailStopDistance),
    TrailTargetDistance: pct(values.trailTargetDistance),
    PartialTPAfter: values.partialOn ? pct(values.partialAfter) : 0,
    PartialTPFraction: values.partialOn ? pct(values.partialFraction) : 0,
    PartialTPMinShares: values.partialOn ? Number(values.partialMinShares) || 0 : 0,
  };
}

/* The four rung cards. Markup lifted from the handoff and shared rather than copied,
 * so the bracket screen cannot drift from the terminal it was planned on. */
export function LadderRungs({
  values, onChange,
}: {
  values: LadderValues;
  onChange: (next: LadderValues) => void;
}) {
  const set = (patch: Partial<LadderValues>) => onChange({ ...values, ...patch });

  /* Percentages take a decimal point; a share count does not. "Skip under 10.5 shares"
   * is not a rule anyone can act on, and the server rounds it anyway. */
  const field = (
    label: string,
    value: string,
    key: keyof LadderValues,
    whole = false,
  ) => (
    <label className="tg-rungfield">
      <span>{label}</span>
      <input
        value={value}
        inputMode={whole ? "numeric" : "decimal"}
        onChange={(event) =>
          set({
            [key]: whole
              ? sanitizeInteger(event.target.value)
              : sanitizeDecimal(event.target.value),
          } as Partial<LadderValues>)
        }
      />
    </label>
  );

  return (
    <div className="tg-rungs">
      <div className={`tg-rung${values.breakEvenOn ? "" : " off"}`}>
        <div className="tg-runghead">
          <button type="button" className="tg-rungno" aria-pressed={values.breakEvenOn}
            aria-label="toggle rung 1"
            onClick={() => set({ breakEvenOn: !values.breakEvenOn })}>1</button>
          <span className="tg-rungname">Break-even</span>
        </div>
        <p className="tg-rungwhat">Stops a winner from turning into a loss.</p>
        <div className="tg-rungfields">
          {field("Arm at gain %", values.breakEvenAfter, "breakEvenAfter")}
          {field("Lift SL to gain %", values.breakEvenFloor, "breakEvenFloor")}
        </div>
        <p className="tg-rungnote">
          If it stalls here it exits at this floor — a small gain, never red.
        </p>
      </div>

      <div className={`tg-rung${values.profitLockOn ? "" : " off"}`}>
        <div className="tg-runghead">
          <button type="button" className="tg-rungno" aria-pressed={values.profitLockOn}
            aria-label="toggle rung 2"
            onClick={() => set({ profitLockOn: !values.profitLockOn })}>2</button>
          <span className="tg-rungname">Profit lock</span>
        </div>
        <p className="tg-rungwhat">Banks a real slice instead of giving it all back.</p>
        <div className="tg-rungfields">
          {field("Arm at gain %", values.profitLockAfter, "profitLockAfter")}
          {field("Lift SL to gain %", values.profitLockFloor, "profitLockFloor")}
        </div>
        <p className="tg-rungnote">
          This floor sits above rung one; however far price retraces, it holds.
        </p>
      </div>

      <div className="tg-rung">
        <div className="tg-runghead">
          <span className="tg-rungno static">3</span>
          <span className="tg-rungname">Trail</span>
        </div>
        <p className="tg-rungwhat">Lets a runner run, following at a fixed distance.</p>
        <div className="tg-rungfields">
          {field("SL trails from gain %", values.trailStopAfter, "trailStopAfter")}
          {field("TP widens from gain %", values.trailTargetAfter, "trailTargetAfter")}
          {field("SL below high %", values.trailStopDistance, "trailStopDistance")}
          {field("TP above high %", values.trailTargetDistance, "trailTargetDistance")}
        </div>
        <p className="tg-rungnote">
          Rungs one and two measure from entry; the trail measures from the high.
        </p>
      </div>

      <div className={`tg-rung${values.partialOn ? "" : " off"}`}>
        <div className="tg-runghead">
          <button type="button" className="tg-rungno" aria-pressed={values.partialOn}
            aria-label="toggle rung 4"
            onClick={() => set({ partialOn: !values.partialOn })}>4</button>
          <span className="tg-rungname">Partial take-profit</span>
        </div>
        <p className="tg-rungwhat">Takes cash off the table without closing the runner.</p>
        <div className="tg-rungfields">
          {field("Arm at gain %", values.partialAfter, "partialAfter")}
          {field("Sell % of position", values.partialFraction, "partialFraction")}
          {field("Skip under N shares", values.partialMinShares, "partialMinShares", true)}
        </div>
        <p className="tg-rungnote">
          Sent as a limit at the arm price, never market — it will not walk down its
          own book.
        </p>
      </div>
    </div>
  );
}

/* The whole ladder section: heading, preset pills, the four rungs, and whatever the
 * ordering rules have to say about them. */
export function LadderPlane({
  values, onChange, title, subtitle, error,
}: {
  values: LadderValues;
  onChange: (next: LadderValues) => void;
  title: React.ReactNode;
  subtitle: string;
  error?: string;
}) {
  const activePreset = activePresetOf(values);
  const problem = error ?? ladderErrorOf(values);
  return (
    <section className="tg-plane tg-ladderplane">
      <div className="tg-planehead">
        <div>
          <h2>{title}</h2>
          <p className="tg-laddersub">{subtitle}</p>
        </div>
        <div className="tg-presetgroup">
          {(Object.keys(LADDER_PRESETS) as PresetName[]).map((name) => (
            <button
              key={name} type="button"
              className={`tg-presetpill${activePreset === name ? " on" : ""}`}
              aria-pressed={activePreset === name}
              onClick={() => onChange(presetLadder(name))}
            >
              {name}
            </button>
          ))}
        </div>
      </div>

      <LadderRungs values={values} onChange={onChange} />
      {problem && <p className="tg-err">{problem}</p>}
    </section>
  );
}
