import { bytes } from "../catalog/format";
import type { Transfer } from "../api/transfers";

/** A state in words. "Done" never stands alone: what was checked is a separate statement (see verificationText). */
export function stateText(t: Transfer): string {
  switch (t.state) {
    case "queued": return "Queued";
    case "waiting": return "Waiting to retry";
    case "running": return "Transferring";
    case "verifying": return "Checking the copy";
    case "conflict": return "Needs your decision";
    case "failed": return "Failed";
    case "cancelled": return "Cancelled";
    case "skipped": return "Skipped";
    default: return t.verification.status === "verified" ? "Done, verified" : t.verification.status === "unverified" ? "Arrived, NOT verified" : "Done";
  }
}

export const isLive = (t: Transfer) => t.state === "queued" || t.state === "waiting" || t.state === "running" || t.state === "verifying" || t.state === "conflict";
export const isRunning = (t: Transfer) => t.state === "running" || t.state === "verifying";

export function etaText(sec: number): string {
  if (sec < 0) return "";
  if (sec < 60) return "under a minute left";
  if (sec < 3600) return `about ${Math.round(sec / 60)} min left`;
  const h = Math.floor(sec / 3600);
  return `about ${h} h ${Math.round((sec % 3600) / 60)} min left`;
}

/** Bytes so far, of the total when known, with the rate and what rsync estimates is left. Unknown is never zero. */
export function progressText(t: Transfer): string {
  const parts = [t.totalBytes > 0 ? `${bytes(t.doneBytes)} of ${bytes(t.totalBytes)}` : `${bytes(t.doneBytes)} so far`];
  if (t.state === "running" && t.rateBps > 0) parts.push(`${bytes(t.rateBps)}/s`);
  if (t.state === "running" && t.etaSeconds >= 0 && t.rateBps > 0) parts.push(etaText(t.etaSeconds));
  return parts.join(" · ");
}

export function verificationText(t: Transfer): string | null {
  const v = t.verification;
  if (v.status === "verified") {
    return v.method === "sha256"
      ? "Verified: the size and SHA-256 are the same on both machines."
      : "Verified by content: every file was compared with rsync's checksum (not SHA-256).";
  }
  if (v.status === "unverified") return `Not verified: ${v.note}`;
  return null;
}

export const directionText = (t: Transfer) => (t.direction === "download" ? "Download" : "Upload");
