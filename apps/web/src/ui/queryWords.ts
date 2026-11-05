/**
 * The words of a filter query. Words are separated by whitespace outside double quotes, so `unit:"km / s"` is one word.
 * Quotes are kept in the returned words (a chip is written back the way it was typed); `unquote` strips them for matching.
 * An unterminated quote takes the rest of the query as part of its word.
 */
export function tokenize(q: string): string[] {
  const out: string[] = [];
  let cur = "";
  let quoted = false;
  for (const ch of q) {
    if (ch === '"') quoted = !quoted;
    if (!quoted && /\s/.test(ch)) {
      if (cur !== "") out.push(cur);
      cur = "";
    } else {
      cur += ch;
    }
  }
  if (cur !== "") out.push(cur);
  return out;
}

/** True when the text ends inside an open quote. */
export const inQuote = (q: string) => (q.match(/"/g)?.length ?? 0) % 2 === 1;

export const unquote = (w: string) => w.replace(/"/g, "");

/** A value as it is written in a query: quoted only when it has whitespace. */
export const quoteValue = (v: string) => (/\s/.test(v) ? `"${v}"` : v);
