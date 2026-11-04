import { useEffect, useState } from "react";
import { Button, Input } from "../ui";
import type { RegionShape } from "../api/planes";
import type { Viewer } from "./useViewer";
import { EM } from "../catalog/format";
import { nominalPixels } from "./region";
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
      {info ? <p className={s.note}>{info.mask_policy}{info.size ? ` ${info.size.note}.` : ""}</p> : null}
    </div>
  );
}
