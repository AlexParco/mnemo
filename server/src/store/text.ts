/** Text primitives shared by the store. Semantics mirror `card.py` exactly:
 * the card render is a byte-for-byte contract, so these helpers are ports, not
 * rewrites. Where Python and JS differ (code points vs UTF-16 units, splitlines
 * vs split) the Python behaviour wins. */

export const MAX_ITEM_LEN = 100;

/** Port of Python's `str.splitlines()` for the line breaks that occur in `.md`:
 * splits on \r\n, \n and \r, and drops the trailing empty element that a final
 * break would otherwise produce (`"a\n".splitlines() == ["a"]`). */
export function splitLines(s: string): string[] {
  const out = s.split(/\r\n|\n|\r/);
  if (out.length > 0 && out[out.length - 1] === "") out.pop();
  return out;
}

/** Port of `s.strip("'\"")`: removes any run of quotes from both ends. */
export function stripQuotes(s: string): string {
  let a = 0;
  let b = s.length;
  const isQuote = (c: string | undefined) => c === "'" || c === '"';
  while (a < b && isQuote(s[a])) a++;
  while (b > a && isQuote(s[b - 1])) b--;
  return s.slice(a, b);
}

/** Port of card.py's `truncate`. Counts and slices by code point, like Python's
 * `len`/slicing — `"🐛".length` is 2 in JS but 1 in Python, and the card's
 * 100-char budget is defined in Python terms. */
export function truncate(s: string, max = MAX_ITEM_LEN): string {
  const t = s.trim();
  const cps = Array.from(t);
  if (cps.length <= max) return t;
  return cps.slice(0, max - 1).join("") + "…";
}

/** Code-point ordering, matching Python's string comparison. Used where the
 * Python side relies on `sorted()` over file names. */
export function compareCodePoints(a: string, b: string): number {
  const ca = Array.from(a);
  const cb = Array.from(b);
  const n = Math.min(ca.length, cb.length);
  for (let i = 0; i < n; i++) {
    const x = ca[i]!.codePointAt(0)!;
    const y = cb[i]!.codePointAt(0)!;
    if (x !== y) return x < y ? -1 : 1;
  }
  return ca.length === cb.length ? 0 : ca.length < cb.length ? -1 : 1;
}
