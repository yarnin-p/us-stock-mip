/* What a field will accept, decided at the keystroke rather than at submit.
 *
 * Rejecting on submit is the usual shape and it is the worse one here: you find out
 * the ticker was wrong after writing the whole ticket, and a stray character in a
 * price field turns the risk figure into NaN silently while you keep typing. Filtering
 * as it is typed means the field can only ever hold something the rest of the screen
 * can compute from.
 *
 * The ticker charset is measured, not assumed: across the 20,750 US symbols in the
 * database the only characters that occur are A-Z, "." and "-", the longest is nine,
 * and not one contains a digit.
 */

const TICKER_CHARS = /[^A-Z.-]/g;
export const TICKER_MAX = 9;

export function sanitizeTicker(raw: string): string {
  return raw.toUpperCase().replace(TICKER_CHARS, "").slice(0, TICKER_MAX);
}

/* Decimals keep one point and drop everything else. A leading "." is allowed through
 * as "0." so typing ".5" behaves; a second point is ignored rather than truncating
 * what follows it, because truncating turns 1.2.3 into 1.2 and quietly changes the
 * number someone is halfway through correcting. */
export function sanitizeDecimal(raw: string): string {
  let seen = false;
  let out = "";
  for (const ch of raw) {
    if (ch >= "0" && ch <= "9") {
      out += ch;
      continue;
    }
    if (ch === "." && !seen) {
      seen = true;
      out += out === "" ? "0." : ".";
    }
  }
  return out;
}

/* Whole numbers only -- share counts, and the "skip under N shares" rung.
 *
 * Truncated at the point rather than stripped of it. Deleting the dot from a pasted
 * "10.5" gives 105: a different number, an order of magnitude out, and silently. */
export function sanitizeInteger(raw: string): string {
  return raw.split(".")[0].replace(/[^0-9]/g, "");
}
