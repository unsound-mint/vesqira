import type { Controls } from "../api/runs";
import { Pill } from "../ui";
import { controlLines } from "./controlsText";
import s from "./RunPage.module.css";

/** The controls stated for a result: each block with who said so. A block that was not stated is not shown, and nothing is shown as "none". */
export function ControlsView({ controls, only }: { controls: Controls | null | undefined; only?: "smoothing" | "noise" | "construction" }) {
  const lines = controlLines(controls).filter((l) => !only || l.key === only);
  if (lines.length === 0) return null;
  return (
    <ul className={s.known} aria-label="Controls">
      {lines.map((l) => (
        <li key={l.key}>
          <Pill tone={l.basis === "verified" ? "ok" : "neutral"}>{l.basis}</Pill> <strong>{l.label}.</strong> {l.text} <span className={s.note}>({l.stated})</span>
        </li>
      ))}
    </ul>
  );
}
