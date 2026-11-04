import { useEffect, useState } from "react";
import { Button, Input } from "../ui";
import type { RegionShape } from "../api/planes";
import type { Viewer } from "./useViewer";
import { EM } from "../catalog/format";
import { nominalPixels, sameRegion } from "./region";
import { ds9Url, toShape } from "../api/regions";
import type { SavedRegion } from "../api/regions";
import type { Tool } from "./region";
import s from "./RegionPanel.module.css";

const TOOLS: { id: Tool; label: string }[] = [
  { id: "pointer", label: "Pointer" },
  { id: "rect", label: "Rectangle" },
  { id: "circle", label: "Circle" },
];

const FIELDS = { rect: ["x0", "y0", "x1", "y1"], circle: ["cx", "cy", "r"] } as const;
const LABEL: Record<string, string> = { x0: "x from", y0: "y from", x1: "x to", y1: "y to", cx: "Centre x", cy: "Centre y", r: "Radius" };

/** Region tool, exact numeric entry (the keyboard route to a region) and what the region contains. */
export function RegionPanel({ v }: { v: Viewer }) {
  const { region, spectrum, view } = v;
  const [text, setText] = useState<Record<string, string>>({});
  const [problem, setProblem] = useState<string | null>(null);
  const [kind, setKind] = useState<"rect" | "circle">("rect");

  // keep the boxes in step with a region drawn on the image; FITS pixel numbers start at 1
  useEffect(() => {
    if (!region) return;
    setKind(region.shape);
    const next: Record<string, string> = {};
    for (const [k, val] of Object.entries(region)) if (k !== "shape") next[k] = String(k === "r" ? val : (val as number) + 1);
    setText(next);
    setProblem(null);
  }, [region]);

  function apply() {
    const fields = FIELDS[kind];
    const nums = fields.map((f) => Number(text[f]));
    if (nums.some((n) => !Number.isInteger(n)) || fields.some((f, i) => f === "r" ? nums[i]! < 0 : nums[i]! < 1)) {
      setProblem("Enter whole numbers: pixel numbers start at 1, the radius at 0.");
      return;
    }
    setProblem(null);
    const o: Record<string, number> = {};
    fields.forEach((f, i) => (o[f] = f === "r" ? nums[i]! : nums[i]! - 1));
    v.setRegion((kind === "rect"
      ? { shape: "rect", x0: Math.min(o.x0!, o.x1!), y0: Math.min(o.y0!, o.y1!), x1: Math.max(o.x0!, o.x1!), y1: Math.max(o.y0!, o.y1!) }
      : { shape: "circle", cx: o.cx!, cy: o.cy!, r: o.r! }) as RegionShape);
  }

  if (!view) return <p className={s.note}>{EM}</p>;
  const ch = v.plane ? spectrum?.count[v.plane.head.channel] : undefined;
  const info = spectrum?.head;

  return (
    <div className={s.wrap}>
      <div role="group" aria-label="Region tool" className={s.tools}>
        {TOOLS.map((t) => (
          <button key={t.id} type="button" className={s.tool} aria-pressed={v.tool === t.id} onClick={() => { v.setTool(t.id); if (t.id !== "pointer") setKind(t.id); }}>
            {t.label}
          </button>
        ))}
      </div>
      <p className={s.note}>{v.tool === "pointer" ? "Choose Rectangle or Circle, then drag on the image. A circle is drawn from its centre." : "Drag on the image. Esc cancels a drag."}</p>
      <form className={s.fields} onSubmit={(e) => { e.preventDefault(); apply(); }} aria-label="Region by pixel numbers">
        {FIELDS[kind].map((f) => (
          <Input key={f} label={LABEL[f]!} inputMode="numeric" value={text[f] ?? ""} onChange={(e) => setText({ ...text, [f]: e.target.value })} />
        ))}
        <div className={s.actions}>
          <Button type="submit">Set region</Button>
          <Button type="button" onClick={() => { v.setRegion(null); setText({}); }} disabled={!region}>Clear</Button>
        </div>
      </form>
      {problem ? <p role="alert" className={s.err}>{problem}</p> : null}
      {region ? (
        <dl className={s.facts}>
          <dt>Pixels</dt>
          <dd className="mono">{info ? info.region.pixels.toLocaleString("en-US") : v.interp && v.axial ? v.axial.pixels.toLocaleString("en-US") : nominalPixels(region).toLocaleString("en-US")}{info?.region.clipped ? ` of ${info.region.requested_pixels.toLocaleString("en-US")} (clipped at the image edge)` : ""}</dd>
          <dt>Area (reference pixel)</dt>
          <dd className="mono">{info ? (info.size ? `${info.size.area_arcmin2.toFixed(3)} arcmin²` : "unknown: no celestial WCS") : EM}</dd>
          <dt>Pixel scale</dt>
          <dd className="mono">{info?.size ? `${info.size.pixel_scale_arcsec[0].toFixed(3)} × ${info.size.pixel_scale_arcsec[1].toFixed(3)} arcsec` : EM}</dd>
          <dt>This channel</dt>
          <dd className="mono">{ch === undefined || !info ? EM : `${ch.toLocaleString("en-US")} of ${info.region.pixels.toLocaleString("en-US")} pixels have a value`}</dd>
        </dl>
      ) : null}
      <SavedList v={v} />
      {info ? <p className={s.note}>{info.mask_policy}{info.size ? ` ${info.size.note}.` : ""}</p> : null}
    </div>
  );
}

const describe = (r: SavedRegion) =>
  r.shape.shape === "rect"
    ? `rectangle x ${r.shape.x0 + 1}–${r.shape.x1 + 1}, y ${r.shape.y0 + 1}–${r.shape.y1 + 1}`
    : `circle at ${r.shape.cx + 1}, ${r.shape.cy + 1}, radius ${r.shape.r}`;

/** Named regions kept with the dataset: save the current one, pick one to work with, rename, replace its shape, copy, delete. */
function SavedList({ v }: { v: Viewer }) {
  const sr = v.savedRegions;
  const [name, setName] = useState("");
  const [editing, setEditing] = useState<{ id: number; text: string } | null>(null);
  return (
    <section className={s.saved} aria-label="Saved regions">
      <h3 className={s.h}>Saved regions</h3>
      <form className={s.row} onSubmit={(e) => { e.preventDefault(); if (v.region && name.trim()) { void sr.save(name.trim(), v.region); setName(""); } }}>
        <Input label="Save the current region as" value={name} onChange={(e) => setName(e.target.value)} maxLength={80} />
        <Button type="submit" disabled={!v.region || !name.trim()}>Save</Button>
      </form>
      {sr.saved.length === 0 ? <p className={s.note}>{v.region ? "Give the region a name to keep it with this dataset." : "No saved regions yet. Draw one, then save it."}</p> : null}
      <ul className={s.list}>
        {sr.saved.map((r) => {
          const selected = sr.selectedId === r.id;
          const changed = selected && v.region !== null && !sameRegion(v.region, toShape(r));
          return (
            <li key={r.id} className={selected ? s.selected : undefined}>
              {editing?.id === r.id ? (
                <form className={s.row} onSubmit={(e) => { e.preventDefault(); if (editing.text.trim()) void sr.rename(r.id, editing.text.trim()); setEditing(null); }}>
                  <Input label={`New name for ${r.name}`} value={editing.text} onChange={(e) => setEditing({ id: r.id, text: e.target.value })} maxLength={80} autoFocus />
                  <Button type="submit">Rename</Button>
                  <Button type="button" onClick={() => setEditing(null)}>Cancel</Button>
                </form>
              ) : (
                <>
                  <label className={s.pick}>
                    <input type="checkbox" checked={!sr.hidden.has(r.id)} onChange={() => sr.toggle(r.id)} aria-label={`Show ${r.name} on the image`} />
                    <button type="button" className={s.name} aria-pressed={selected} onClick={() => sr.select(selected ? null : r)}>{r.name}</button>
                  </label>
                  <span className={s.note}>{describe(r)}</span>
                  <div className={s.actions}>
                    <Button type="button" onClick={() => setEditing({ id: r.id, text: r.name })}>Rename</Button>
                    <Button type="button" disabled={!changed} onClick={() => void sr.reshape(r.id, v.region!)}>Use current shape</Button>
                    <Button type="button" onClick={() => void sr.duplicate(r)}>Duplicate</Button>
                    <a className={s.link} href={ds9Url(r.datasetId, r.id)} download>DS9</a>
                    <Button type="button" onClick={() => void sr.remove(r)}>Delete</Button>
                  </div>
                </>
              )}
            </li>
          );
        })}
      </ul>
      {sr.undo ? (
        <p className={s.note} role="status">
          Deleted “{sr.undo.name}”. <button type="button" className={s.name} onClick={() => void sr.undoDelete()}>Undo</button>
        </p>
      ) : null}
      {sr.error ? <p role="alert" className={s.err}>{sr.error}</p> : null}
      {sr.saved.length > 0 && v.datasetId !== null ? <a className={s.link} href={ds9Url(v.datasetId)} download>Export all as a DS9 region file (.reg)</a> : null}
      <p className={s.note}>DS9 files use the dataset’s own pixel grid (image coordinates, counted from 1), so they open in DS9 on the same file without any sky transformation.</p>
    </section>
  );
}
