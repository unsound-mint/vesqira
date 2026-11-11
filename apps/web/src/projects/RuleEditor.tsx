import { useId } from "react";
import { ArrowDown, ArrowUp, Trash2 } from "lucide-react";
import type { ImportPreview, ImportRule, RuleRole } from "../api/projects";
import { Button, Input, Select } from "../ui";
import { COORD_FORMATS, COORD_FRAMES, forRole, localProblems, QUANTITIES, RULE_ROLES } from "./importFormat";
import { num } from "./format";
import s from "./Projects.module.css";

type Report = ImportPreview["rules"][number];

const set = <K extends keyof ImportRule>(r: ImportRule, k: K, v: ImportRule[K]): ImportRule => ({ ...r, [k]: v });

/** One researcher rule: a pattern on the relative path, a role, and the declarations that role takes. Nothing has a default. */
export function RuleEditor({ rule, index, count, report, onChange, onMove, onRemove }: {
  rule: ImportRule;
  index: number;
  count: number;
  report: Report | undefined;
  onChange: (r: ImportRule) => void;
  onMove: (delta: -1 | 1) => void;
  onRemove: () => void;
}) {
  const id = useId();
  const problems = localProblems(rule);
  const text = (label: string, key: "unit" | "lon" | "lat" | "equinox" | "angleColumn" | "sigmaColumn", hint?: string) => (
    <Input label={label} hint={hint} value={rule[key] ?? ""} onChange={(e) => onChange(set(rule, key, e.target.value))} spellCheck={false} />
  );
  const choice = (label: string, key: "angleUnit" | "angleFrame" | "quantity" | "coordFormat" | "coordFrame", options: string[], blank: string) => (
    <Select label={label} value={rule[key] ?? ""} onChange={(e) => onChange(set(rule, key, e.target.value as never))}>
      <option value="">{blank}</option>
      {options.map((o) => <option key={o} value={o}>{o}</option>)}
    </Select>
  );
  const angle = (needUnit: boolean) => choice("Angle unit", "angleUnit", ["deg", "rad"], needUnit ? "Choose (required)" : "Choose");
  return (
    <li className={s.rule} aria-label={`Rule ${index + 1}: ${rule.name}`}>
      <div className={s.ruleHead}>
        <span className={s.ruleNo}>{index + 1}</span>
        <Input label="Name" value={rule.name} onChange={(e) => onChange(set(rule, "name", e.target.value))} spellCheck={false} />
        <div className={s.ruleBtns}>
          <Button icon={ArrowUp} aria-label={`Move ${rule.name} up`} disabled={index === 0} onClick={() => onMove(-1)}>Up</Button>
          <Button icon={ArrowDown} aria-label={`Move ${rule.name} down`} disabled={index === count - 1} onClick={() => onMove(1)}>Down</Button>
          <Button icon={Trash2} variant="danger" aria-label={`Remove ${rule.name}`} onClick={onRemove}>Remove</Button>
        </div>
      </div>
      <div className={s.ruleGrid}>
        <Select label="Pattern kind" value={rule.kind} onChange={(e) => onChange(set(rule, "kind", e.target.value as ImportRule["kind"]))}>
          <option value="glob">glob (a path with * and **)</option>
          <option value="regex">regex (Go syntax)</option>
        </Select>
        <Input label="Pattern on the path from the project folder" value={rule.pattern} onChange={(e) => onChange(set(rule, "pattern", e.target.value))} spellCheck={false}
          placeholder={rule.kind === "glob" ? "vda/*/vgt/psi_*.fits" : "^data/stars/.*\\.dat$"} />
        <Select label="Role" value={rule.role} onChange={(e) => onChange(forRole(rule, e.target.value as RuleRole))}>
          {RULE_ROLES.map((r) => <option key={r.id} value={r.id}>{r.label}</option>)}
        </Select>
      </div>
      {rule.role === "dataset" ? <div className={s.ruleGrid}>{text("Unit of the values (optional)", "unit", "declared by your rule; a file that states its own BUNIT keeps it")}</div> : null}
      {rule.role === "orientation" ? (
        <div className={s.ruleGrid}>
          {angle(true)}
          {choice("Angle frame", "angleFrame", ["pixel", "sky"], "Not declared")}
          {choice("What the angle describes", "quantity", QUANTITIES, "Not declared")}
        </div>
      ) : null}
      {rule.role === "catalog" ? (
        <>
          <div className={s.ruleGrid}>
            {text("Longitude column", "lon")}
            {text("Latitude column", "lat")}
            {choice("Positions are written as", "coordFormat", COORD_FORMATS, "Choose (required)")}
            {choice("Coordinate frame", "coordFrame", COORD_FRAMES, "Choose (required)")}
            {rule.coordFrame === "fk4" || rule.coordFrame === "fk5" ? text("Equinox", "equinox", "B1950, J2000, ...") : null}
          </div>
          <div className={s.ruleGrid}>
            {text("Angle column (optional)", "angleColumn")}
            {rule.angleColumn ? angle(true) : null}
            {rule.angleColumn ? choice("What the angle describes", "quantity", QUANTITIES, "Not declared") : null}
            {rule.angleColumn ? text("Uncertainty column (optional)", "sigmaColumn") : null}
            <Input label="Columns to show beside each source (comma separated, at most 8)" value={(rule.values ?? []).join(", ")}
              onChange={(e) => onChange(set(rule, "values", e.target.value.split(",").map((v) => v.trim()).filter(Boolean)))} spellCheck={false} />
          </div>
        </>
      ) : null}
      {problems.length > 0 ? <ul className={s.ruleProblems} aria-label="To fix before saving">{problems.map((p) => <li key={p}>{p}</li>)}</ul> : null}
      <div id={`${id}-live`} className={s.ruleLive} role="status">
        {report?.error ? <span className={s.warnText}>{report.error}</span> : report ? (
          <>
            <strong>{num(report.matches)}</strong> {report.matches === 1 ? "file matches" : "files match"}
            {report.matches > 0 ? <> · {num(report.wins)} {report.wins === 1 ? "is" : "are"} decided by this rule{report.shadowed > 0 ? `, ${num(report.shadowed)} by an earlier one` : ""}</> : null}
            {report.examples.length > 0 ? <ul className={s.examples} aria-label={`Files ${rule.name} matches`}>{report.examples.map((p) => <li key={p} className="mono">{p}</li>)}</ul> : null}
          </>
        ) : <span className={s.dim}>Previewing…</span>}
      </div>
    </li>
  );
}
