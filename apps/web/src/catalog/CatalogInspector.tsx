import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { ApiError, catalogReport } from "../api/catalog";
import type { CatalogDataset, CatalogDeclarationRequest, CatalogReport } from "../api/catalog";
import { Button, Checkbox, Input, Section, Select } from "../ui";
import { bytes, EM } from "./format";
import s from "./CatalogInspector.module.css";

const FORMATS: [string, string][] = [
  ["degrees", "Decimal degrees, both columns"],
  ["hours_degrees", "Longitude in decimal hours, latitude in degrees"],
  ["sexagesimal_hours_degrees", "Longitude as h m s, latitude as d m s"],
  ["sexagesimal_degrees", "Both as d m s"],
];
const FRAMES: [string, string][] = [
  ["icrs", "ICRS"],
  ["fk5", "FK5 (needs an equinox, usually J2000)"],
  ["fk4", "FK4 (needs an equinox, usually B1950)"],
  ["galactic", "Galactic"],
];
const QUANTITIES: [string, string][] = [
  ["", "Not declared"],
  ["b_field", "Magnetic field orientation"],
  ["starlight_polarization", "Starlight polarization (along the field)"],
  ["dust_polarization", "Dust polarization (across the field)"],
  ["gradient", "Gradient (across the field)"],
  ["unspecified", "Something else"],
];

type Form = {
  lon: string; lat: string; format: string; frame: string; equinox: string;
  angleOn: boolean; angleColumn: string; angleUnit: string; quantity: string; sigma: string;
  values: string[];
};

function initial(d: CatalogDataset): Form {
  const c = d.catalogDeclaration;
  return {
    lon: c?.lon ?? "", lat: c?.lat ?? "", format: c?.format ?? "", frame: c?.frame ?? "", equinox: c?.equinox ?? "",
    angleOn: !!c?.angle, angleColumn: c?.angle?.column ?? "", angleUnit: c?.angle?.unit ?? "", quantity: c?.angle?.quantity ?? "", sigma: c?.angle?.sigmaColumn ?? "",
    values: c?.values ?? [],
  };
}

function toRequest(f: Form): CatalogDeclarationRequest {
  const needsEquinox = f.frame === "fk5" || f.frame === "fk4";
  return {
    lon: f.lon, lat: f.lat, format: f.format as CatalogDeclarationRequest["format"], frame: f.frame as CatalogDeclarationRequest["frame"],
    equinox: needsEquinox ? f.equinox.trim() : null,
    angle: f.angleOn ? { column: f.angleColumn, unit: f.angleUnit as "deg" | "rad", frame: "sky", quantity: (f.quantity || null) as NonNullable<CatalogDeclarationRequest["angle"]>["quantity"], sigmaColumn: f.sigma || null } : null,
    values: f.values,
  };
}

/** The facts, the declaration form and the parse report of a point catalog (ADR 0043). Sections of the inspector. */
export function CatalogInspector({ d, problem, onDeclare, onReread }: {
  d: CatalogDataset;
  problem?: string;
  onDeclare: (id: number, body: CatalogDeclarationRequest | null) => Promise<string | null>;
  onReread?: () => void;
}) {
  const declaredAt = d.catalogDeclaration?.declaredAt ?? "none";
  const [rep, setRep] = useState<CatalogReport | null>(null);
  const [repErr, setRepErr] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    setRepErr(null);
    catalogReport(d.id)
      .then((r) => live && setRep(r))
      .catch((e: unknown) => {
        if (!live) return;
        setRep(null);
        setRepErr(e instanceof ApiError ? e.message : "Could not read the catalog.");
      });
    return () => {
      live = false;
    };
  }, [d.id, d.modifiedAt, declaredAt]);

  return (
    <>
      <Section title="Catalog">
        <div className={s.wrap}>
          {problem ? (
            <div role="status" className={s.note}>
              <p style={{ margin: 0 }}>{problem}</p>
              {onReread ? <Button type="button" onClick={onReread}>Read it again</Button> : null}
            </div>
          ) : null}
          <dl className={s.facts}>
            <dt>File</dt>
            <dd className="mono" title={d.path}>{d.name}</dd>
            <dt>Size</dt>
            <dd className="mono">{bytes(d.sizeBytes)}</dd>
            <dt>Format</dt>
            <dd className="mono">{d.header.format}</dd>
            <dt>Rows</dt>
            <dd className="mono">{d.header.rows.toLocaleString("en-US")}</dd>
            <dt>Columns</dt>
            <dd className="mono">{d.header.columns.length}</dd>
          </dl>
          <div className={s.scroller} role="region" aria-label="Columns table" tabIndex={0}>
            <table className={s.cols}>
              <caption>Columns as read from the file</caption>
              <thead>
                <tr><th scope="col">Name</th><th scope="col">Type</th><th scope="col">Unit</th></tr>
              </thead>
              <tbody>
                {d.header.columns.map((c) => (
                  <tr key={c.name}>
                    <th scope="row">{c.name}</th>
                    <td>{c.scalar ? c.dtype : "array"}</td>
                    <td>{c.unit ? c.unit : <span className={s.unres}>{EM} not declared</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className={s.note}>A plain table has no frame, no units and no meaning of its own. Units appear only when the file declares them (ECSV, VOTable, FITS). Vesqira does not guess them from the names.</p>
          {repErr ? <p role="alert" className={s.err}>{repErr}</p> : null}
          {rep ? <ReadReport rep={rep} /> : null}
        </div>
      </Section>
      <Section title="Columns and sky positions">
        {rep ? <DeclarationForm key={`${d.id}-${declaredAt}-${d.modifiedAt}`} d={d} rep={rep} onDeclare={onDeclare} /> : repErr ? null : <p className={s.note}>Reading the catalog…</p>}
      </Section>
    </>
  );
}

function ReadReport({ rep }: { rep: CatalogReport }) {
  const r = rep.read;
  return (
    <div className={s.report}>
      {r.headerless ? <p className={s.note}>This file has no header row, so its columns are named col1, col2, … in file order.</p> : null}
      {r.skippedTotal > 0 ? (
        <div role="status">
          <p className={s.warn}>{r.skippedTotal === 1 ? "1 line is not a row and was left out:" : `${r.skippedTotal} lines are not rows and were left out:`}</p>
          <ul className={s.list}>
            {r.skipped.map((x) => <li key={x.line}>line {x.line} {x.reason}</li>)}
            {r.skippedTotal > r.skipped.length ? <li>and {r.skippedTotal - r.skipped.length} more</li> : null}
          </ul>
        </div>
      ) : null}
      {r.notes.filter((n) => !n.startsWith("the file has no header")).map((n) => <p key={n} className={s.note}>{n}</p>)}
    </div>
  );
}

function DeclarationForm({ d, rep, onDeclare }: {
  d: CatalogDataset;
  rep: CatalogReport;
  onDeclare: (id: number, body: CatalogDeclarationRequest | null) => Promise<string | null>;
}) {
  const [f, setF] = useState<Form>(() => initial(d));
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [filled, setFilled] = useState(false);
  const decl = d.catalogDeclaration;
  const cols = d.header.columns.filter((c) => c.scalar);
  const set = <K extends keyof Form>(k: K, v: Form[K]) => setF((cur) => ({ ...cur, [k]: v }));
  const p = rep.proposal;
  const proposed = p.lon !== null || p.lat !== null || p.angle !== null;

  function applyProposal() {
    setF((cur) => ({
      ...cur,
      lon: p.lon ?? cur.lon, lat: p.lat ?? cur.lat, format: p.format ?? cur.format, frame: p.frame ?? cur.frame, equinox: p.equinox ?? cur.equinox,
      ...(p.angle ? { angleOn: true, angleColumn: p.angle.column, angleUnit: p.angle.unit ?? cur.angleUnit, sigma: p.sigma ?? cur.sigma } : {}),
    }));
    setFilled(true);
  }
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const r = await onDeclare(d.id, toRequest(f));
    setBusy(false);
    setErr(r);
    if (r === null) setFilled(false);
  }
  async function withdraw() {
    setBusy(true);
    setErr(await onDeclare(d.id, null));
    setBusy(false);
  }
  const colOptions = (blank: string) => (
    <>
      <option value="">{blank}</option>
      {cols.map((c) => <option key={c.name} value={c.name}>{c.name}</option>)}
    </>
  );

  return (
    <form className={s.wrap} onSubmit={(e) => void submit(e)}>
      {decl ? (
        <p className={s.note}>Declared by you, {new Date(decl.declaredAt).toLocaleDateString()}. It is kept apart from the file, which is not changed.</p>
      ) : (
        <p className={s.note}>Nothing is declared yet, so this catalog cannot be placed on the sky. Say which columns are the position and which frame they use. Vesqira does not assume it.</p>
      )}
      {decl?.stale ? <p role="status" className={s.warn}>The file has changed since you declared this, so the declaration is not being used. Check it, then confirm it again.</p> : null}
      {!decl && proposed ? (
        <div className={s.proposal} role="group" aria-label="Proposal from the column names">
          <p className={s.label}>{p.basis}</p>
          <ul className={s.list}>
            {p.lon && p.lat ? <li>positions: {p.lon}, {p.lat}</li> : null}
            {p.format ? <li>written as {FORMATS.find(([v]) => v === p.format)?.[1].toLowerCase()} ({p.format_basis})</li> : null}
            {p.frame ? <li>frame {p.frame}{p.equinox ? ` ${p.equinox}` : ""} ({p.frame_basis})</li> : null}
            {p.angle ? <li>angle: {p.angle.column}{p.angle.unit ? ` in ${p.angle.unit} (${p.angle.unit_basis})` : ""}{p.sigma ? `, uncertainty ${p.sigma}` : ""}</li> : null}
            {p.notes.map((n) => <li key={n}>{n}</li>)}
          </ul>
          <Button type="button" onClick={applyProposal}>Fill the form from this proposal</Button>
          <p className={s.note}>The proposal is only a suggestion from the names. Nothing is saved until you declare it.</p>
        </div>
      ) : null}
      {filled ? <p role="status" className={s.note}>Filled from the proposal. Check each field, then declare.</p> : null}

      <Select label="Longitude column" value={f.lon} onChange={(e) => set("lon", e.target.value)}>{colOptions("Choose a column")}</Select>
      <Select label="Latitude column" value={f.lat} onChange={(e) => set("lat", e.target.value)}>{colOptions("Choose a column")}</Select>
      <Select label="How positions are written" value={f.format} onChange={(e) => set("format", e.target.value)}>
        <option value="">Choose</option>
        {FORMATS.map(([v, t]) => <option key={v} value={v}>{t}</option>)}
      </Select>
      <Select label="Celestial frame" value={f.frame} onChange={(e) => set("frame", e.target.value)}>
        <option value="">Choose</option>
        {FRAMES.map(([v, t]) => <option key={v} value={v}>{t}</option>)}
      </Select>
      {f.frame === "fk5" || f.frame === "fk4" ? (
        <Input label="Equinox" value={f.equinox} onChange={(e) => set("equinox", e.target.value)} placeholder={f.frame === "fk5" ? "J2000" : "B1950"} spellCheck={false} autoComplete="off" maxLength={12} />
      ) : null}
      <p className={s.note}>Hours are for right ascension only. A Galactic longitude is always in degrees.</p>

      <Checkbox label="This catalog has an angle column" checked={f.angleOn} onChange={(e) => set("angleOn", e.target.checked)} />
      {f.angleOn ? (
        <div className={s.group}>
          <Select label="Angle column" value={f.angleColumn} onChange={(e) => set("angleColumn", e.target.value)}>{colOptions("Choose a column")}</Select>
          <Select label="Angle unit" value={f.angleUnit} onChange={(e) => set("angleUnit", e.target.value)}>
            <option value="">Choose</option>
            <option value="deg">Degrees</option>
            <option value="rad">Radians</option>
          </Select>
          <Select label="What the angle is" value={f.quantity} onChange={(e) => set("quantity", e.target.value)}>
            {QUANTITIES.map(([v, t]) => <option key={v} value={v}>{t}</option>)}
          </Select>
          <Select label="Angle uncertainty column" value={f.sigma} onChange={(e) => set("sigma", e.target.value)}>{colOptions("None")}</Select>
          <p className={s.note}>The angle is a position angle on the sky, east of north, in the frame chosen above. It is an orientation: 0° and 180° are the same.</p>
        </div>
      ) : null}

      <fieldset className={s.values}>
        <legend className={s.label}>Columns to show next to each source (up to 8)</legend>
        {cols.filter((c) => ![f.lon, f.lat, f.angleOn ? f.angleColumn : "", f.angleOn ? f.sigma : ""].includes(c.name)).map((c) => (
          <Checkbox key={c.name} label={c.name} checked={f.values.includes(c.name)}
            disabled={!f.values.includes(c.name) && f.values.length >= 8}
            onChange={(e) => set("values", e.target.checked ? [...f.values, c.name] : f.values.filter((x) => x !== c.name))} />
        ))}
      </fieldset>

      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      <div className={s.two}>
        <Button type="submit" variant="primary" disabled={busy}>{decl?.stale ? "It still applies" : decl ? "Save changes" : "Declare"}</Button>
        {decl ? <Button type="button" disabled={busy} onClick={() => void withdraw()}>Withdraw</Button> : null}
      </div>
      <ParseReport rep={rep} decl={d.catalogDeclaration !== null} />
    </form>
  );
}

function ParseReport({ rep, decl }: { rep: CatalogReport; decl: boolean }) {
  if (!decl) return null;
  if (rep.parseError) return <p role="status" className={s.warn}>{rep.parseError}</p>;
  const p = rep.parse;
  if (!p) return null;
  return (
    <div className={s.report} role="status" aria-label="How the declaration reads the rows">
      <p className={p.unreadable > 0 ? s.warn : s.ok}>{p.placed.toLocaleString("en-US")} of {p.rows.toLocaleString("en-US")} rows have a position.</p>
      {p.unreadable > 0 ? (
        <>
          <p className={s.warn}>{p.unreadable === 1 ? "1 row could not be read:" : `${p.unreadable} rows could not be read:`}</p>
          <ul className={s.list}>
            {p.samples.map((x) => <li key={`${x.row}-${x.column}`}>{x.line !== null ? `line ${x.line}` : `row ${x.row}`} {x.column} &apos;{x.value}&apos; {x.reason}</li>)}
            {p.unreadable > p.samples.length ? <li>and {p.unreadable - p.samples.length} more</li> : null}
          </ul>
        </>
      ) : null}
      {p.missing > 0 ? <p className={s.note}>{p.missing === 1 ? "1 row has" : `${p.missing} rows have`} no longitude or latitude (an empty cell). It is left off the sky, not placed at 0.</p> : null}
      {p.angle && (p.angle.missing > 0 || p.angle.invalid > 0) ? (
        <p className={s.note}>
          Angle column {p.angle.column}: {p.angle.missing} missing{p.angle.invalid > 0 ? `, ${p.angle.invalid} unreadable` : ""}.
          {p.angle.samples.slice(0, 3).map((x) => ` ${x.line !== null ? `Line ${x.line}` : `Row ${x.row}`} '${x.value}' ${x.reason}.`)}
        </p>
      ) : null}
      {p.sigma && (p.sigma.missing > 0 || p.sigma.invalid > 0) ? (
        <p className={s.note}>Uncertainty column {p.sigma.column}: {p.sigma.missing} missing{p.sigma.invalid > 0 ? `, ${p.sigma.invalid} unreadable` : ""}.</p>
      ) : null}
      <p className={s.note}>Declared by you, not verified. Reading a table without error is not a check that the frame or the unit is the right one.</p>
    </div>
  );
}

