import type { DatasetLocation, NewerNotice } from "../api/catalog";

/**
 * The words for what is known about a file's bytes (ADR 0053). "Verified" is said only for equal full SHA-256 values;
 * a sampled fingerprint is never called a hash of the file, and a declaration is never called a check.
 */
export const LINK_TEXT: Record<DatasetLocation["link"], string> = {
  registered: "registered file",
  verified: "verified same bytes (equal full SHA-256)",
  probable: "probably the same (sampled fingerprint); not verified",
  declared: "declared the same by you; not checked",
  stale: "was verified against an earlier version of the registered file, which has changed since; not trusted now",
};

export const KIND_TEXT: Record<DatasetLocation["kind"], string> = {
  registered: "registered file",
  managed: "managed copy",
  downloaded: "downloaded copy",
  copy: "copy",
};

/** The first characters of a hash, enough to compare by eye; the full value is in the title. */
export const short = (v: string) => `${v.slice(0, 12)}…`;

const DIFFERS_TEXT: Record<string, string> = { size: "size", time: "modification time", fingerprint: "sampled fingerprint" };

/** "what differs: size, modification time". An empty list means nothing measured differs. */
export function differsText(differs: string[]): string {
  return differs.length === 0 ? "size, time and fingerprint are the same" : differs.map((d) => DIFFERS_TEXT[d] ?? d).join(", ");
}

/** One plain sentence per notice. Nothing was changed by it: the dataset or figure still points at what it did. */
export function noticeText(n: NewerNotice): string {
  const what = `what differs: ${differsText(n.differs)}`;
  switch (n.reason) {
    case "newer_file":
      return `A newer result exists: ${n.name} (${what}). Nothing was swapped; this still points at the older file.`;
    case "changed_in_place":
      return `The file at this path changed since it was registered (${what}).`;
    case "figure_data_changed":
      return `The data behind this figure is not what it was drawn from (${what}). The picture is unchanged.`;
  }
}
