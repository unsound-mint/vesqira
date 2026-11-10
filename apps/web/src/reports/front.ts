/**
 * Front matter: an optional block of "key: value" lines between two "---" lines at the very top of a report body, as
 * Obsidian writes it. date (YYYY-MM-DD) and summary (one line under the title) mean something; other keys, such as
 * the properties a Notion import carries, are kept and never shown or published. Must match
 * services/api/internal/reports/front.go; both are tested against packages/contracts/fixtures/report-blocks.json.
 */
const MAX_LINES = 64;
const KEY_LINE = /^[A-Za-z][A-Za-z0-9_-]*:( .*)?$/;

export type Front = { lines: string[]; rest: string } | { lines: null; rest: string };

export function splitFront(body: string): Front {
  const b = body.replace(/\r\n/g, "\n");
  if (!b.startsWith("---\n")) return { lines: null, rest: b };
  const lines = b.split("\n");
  for (let i = 1; i < lines.length && i <= MAX_LINES; i++) {
    const l = (lines[i] as string).replace(/ +$/, "");
    if (l === "---") return { lines: lines.slice(1, i), rest: lines.slice(i + 1).join("\n") };
    if (l !== "" && !KEY_LINE.test(l)) return { lines: null, rest: b };
  }
  return { lines: null, rest: b };
}

export function frontValue(body: string, key: string): string {
  for (const l of splitFront(body).lines ?? []) {
    const i = l.indexOf(":");
    if (i > 0 && l.slice(0, i).toLowerCase() === key.toLowerCase()) return l.slice(i + 1).trim();
  }
  return "";
}

export function reportDate(body: string): string {
  const d = frontValue(body, "date");
  return /^\d{4}-\d{2}-\d{2}$/.test(d) && !Number.isNaN(Date.parse(d + "T00:00:00Z")) && new Date(d + "T00:00:00Z").toISOString().startsWith(d) ? d : "";
}

export function reportSummary(body: string): string {
  const s = frontValue(body, "summary");
  return [...s].length > 600 ? [...s].slice(0, 599).join("") + "…" : s;
}

/** The body with its front matter put back in front of rest (the blocks after an edit). */
export function withFront(body: string, rest: string): string {
  const f = splitFront(body);
  return f.lines ? `---\n${f.lines.join("\n")}\n---\n\n${rest}` : rest;
}

/** Sets (or, with an empty value, removes) one key, keeping the others and their order. */
export function setFrontValue(body: string, key: string, value: string): string {
  const f = splitFront(body);
  const v = value.replace(/\s+/g, " ").trim();
  const lines = (f.lines ?? []).filter((l) => l.trim() !== "");
  const i = lines.findIndex((l) => l.slice(0, l.indexOf(":")).toLowerCase() === key);
  if (i >= 0) {
    if (v) lines[i] = `${key}: ${v}`;
    else lines.splice(i, 1);
  } else if (v) lines.push(`${key}: ${v}`);
  const rest = f.rest.replace(/^\n+/, "");
  return lines.length ? `---\n${lines.join("\n")}\n---\n\n${rest}` : rest;
}

/** A YYYY-MM-DD date written out, as the rest of the reading surface writes dates; "" for none. */
export function longDate(d: string | undefined): string {
  if (!d) return "";
  return new Date(d + "T00:00:00Z").toLocaleDateString("en-GB", { day: "numeric", month: "long", year: "numeric", timeZone: "UTC" });
}

/** A short date for a list: "10 Nov". */
export function shortDay(d: string): string {
  return new Date(d + "T00:00:00Z").toLocaleDateString("en-GB", { day: "numeric", month: "short", timeZone: "UTC" });
}

/** A title without the "Report 10.11.2026 —" that a list showing the date already says. */
export function listTitle(title: string): string {
  const t = title.replace(/^(?:report|meeting|notes?)?\s*\d{1,2}[./]\d{1,2}[./]\d{2,4}\s*[—–:-]\s*/i, "");
  return t.length >= 3 ? t[0]!.toUpperCase() + t.slice(1) : title;
}
