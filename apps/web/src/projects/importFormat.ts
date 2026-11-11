import type { ImportRule, RuleRole } from "../api/projects";

/** What a built-in or rule role is called on screen. A role says what the file is, never what its values are. */
const ROLE_LABELS: Record<string, string> = {
  dataset: "Dataset (FITS)",
  orientation: "Orientation map",
  catalog: "Point catalog",
  ignore: "Ignored by your rule",
  run_record: "Run record",
  vgt_psi: "VGT output: psi",
  vgt_reliability: "VGT output: reliability",
  vgt_support: "VGT output: support",
  vgt_fit_rbar2: "VGT output: fit rbar2",
  run_output: "File inside a run",
  rohsa_fit: "ROHSA fit (raw output)",
  rohsa_product: "ROHSA product map",
  config: "Config",
  job_template: "Job template",
  archive: "Archive (never opened)",
  other_discipline: "Other format, no viewer",
  declared_data: "File in a folder you declared as data",
  other: "Not recognised",
};
export const roleLabel = (role: string): string => ROLE_LABELS[role] ?? role;

export const RULE_ROLES: { id: RuleRole; label: string }[] = [
  { id: "dataset", label: "Dataset (FITS image)" },
  { id: "orientation", label: "Orientation map" },
  { id: "catalog", label: "Point catalog" },
  { id: "ignore", label: "Ignore" },
];

export const QUANTITIES = ["b_field", "gradient", "dust_polarization", "starlight_polarization", "unspecified"];
export const COORD_FORMATS = ["degrees", "hours_degrees", "sexagesimal_hours_degrees", "sexagesimal_degrees"];
export const COORD_FRAMES = ["icrs", "fk5", "fk4", "galactic"];

/** A new rule: nothing declared. The angle unit and the coordinate frame are the researcher's to choose; there is no default. */
export const blankRule = (n: number): ImportRule => ({ name: `rule ${n}`, kind: "glob", pattern: "", role: "dataset" });

/** The names a new rule may take without clashing (names are what declarations are labelled with). */
export function freshName(rules: ImportRule[]): string {
  const have = new Set(rules.map((r) => r.name.toLowerCase()));
  for (let n = rules.length + 1; ; n++) if (!have.has(`rule ${n}`)) return `rule ${n}`;
}

/** Drops the declaration fields a role does not take, so switching a rule's role never leaves a stale one behind. */
export function forRole(r: ImportRule, role: RuleRole): ImportRule {
  const base = { name: r.name, kind: r.kind, pattern: r.pattern, role };
  switch (role) {
    case "dataset":
      return { ...base, unit: r.unit };
    case "orientation":
      return { ...base, angleUnit: r.angleUnit, angleFrame: r.angleFrame, quantity: r.quantity };
    case "catalog":
      return { ...base, lon: r.lon, lat: r.lat, coordFormat: r.coordFormat, coordFrame: r.coordFrame, equinox: r.equinox, angleColumn: r.angleColumn, angleUnit: r.angleUnit, quantity: r.quantity, sigmaColumn: r.sigmaColumn, values: r.values };
    default:
      return base;
  }
}

/** Empty strings and empty lists are "not declared": the wire format omits them. */
export function tidy(r: ImportRule): ImportRule {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(r)) {
    if (v === "" || v === undefined || (Array.isArray(v) && v.length === 0)) continue;
    out[k] = typeof v === "string" ? v.trim() : v;
  }
  return out as ImportRule;
}

export const sameRules = (a: ImportRule[], b: ImportRule[]): boolean => JSON.stringify(a.map(tidy)) === JSON.stringify(b.map(tidy));

/** The problems the editor can see without asking the server (the server checks everything again on save). */
export function localProblems(r: ImportRule): string[] {
  const bad: string[] = [];
  if (!r.pattern.trim()) bad.push("Give a pattern.");
  if (r.role === "orientation" && !r.angleUnit) bad.push("Choose the angle unit: Vesqira does not guess degrees or radians.");
  if (r.role === "catalog") {
    if (!r.lon || !r.lat) bad.push("Name the longitude and latitude columns.");
    if (!r.coordFormat) bad.push("Choose how the positions are written.");
    if (!r.coordFrame) bad.push("Choose the coordinate frame: the file does not say and there is no default.");
    if ((r.coordFrame === "fk4" || r.coordFrame === "fk5") && !r.equinox) bad.push("An fk4 or fk5 frame needs an equinox (B1950, J2000, ...).");
    if (r.angleColumn && !r.angleUnit) bad.push("Choose the angle unit of the angle column.");
  }
  return bad;
}
