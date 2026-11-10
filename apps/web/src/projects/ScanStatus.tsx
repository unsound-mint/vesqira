import { Button, Pill } from "../ui";
import type { ScanProgress } from "../api/projects";
import { num } from "./format";
import s from "./Projects.module.css";

/** What the scan is doing, in words. Nothing here claims more than the counters say. */
export function scanText(scan: ScanProgress): string | null {
  switch (scan.state) {
    case "scanning":
      return `reading folder · ${num(scan.entries)} entries`;
    case "registering":
      return `reading headers · ${num(scan.registered + scan.failed)} of ${num(scan.toRegister)}`;
    case "canceled":
      return "scan canceled · what was found so far is shown";
    case "error":
      return `scan failed${scan.error ? `: ${scan.error}` : ""}`;
    default:
      return null;
  }
}

export function ScanStatus({ scan, onCancel }: { scan: ScanProgress; onCancel?: () => void }) {
  const text = scanText(scan);
  if (!text) return null;
  const running = scan.state === "scanning" || scan.state === "registering";
  return (
    <div className={s.scan} role="status">
      <Pill tone={scan.state === "error" ? "err" : scan.state === "canceled" ? "warn" : "accent"}>{text}</Pill>
      {running && onCancel ? <Button onClick={onCancel}>Cancel</Button> : null}
      {scan.state === "registering" && scan.failed > 0 ? <span className={s.dim}>{num(scan.failed)} could not be read</span> : null}
    </div>
  );
}
