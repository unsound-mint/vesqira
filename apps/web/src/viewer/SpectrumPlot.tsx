import type { MouseEvent } from "react";
import type { Spectrum } from "../api/planes";
import type { Viewer } from "./useViewer";
import { EM } from "../catalog/format";
import { unitLabel } from "./format";
import { QueueNote, useQueueReason } from "../ui/QueueNote";
import s from "./SpectrumPlot.module.css";

const W = 320;
const H = 168;
const M = { l: 64, r: 10, t: 10, b: 40 }; // the left margin holds the value labels, so no text sits on the curve

/** Display units for the axis: m/s as km/s, Hz as MHz. Conversion for reading only. */
export function axisDisplay(unit: string | null): { factor: number; label: string } {
  if (unit === "m/s") return { factor: 1e-3, label: "km/s" };
  if (unit === "Hz") return { factor: 1e-6, label: "MHz" };
  return { factor: 1, label: unit ?? "" };
}

const num = (v: number) => Number(v.toPrecision(5)).toString();

/** Polyline segments broken wherever the mean is unknown (NaN), so a gap is never drawn as zero. */
export function segments(x: number[], y: number[]): string[][] {
  const out: string[][] = [];
  let cur: string[] = [];
  for (let i = 0; i < x.length; i++) {
    if (Number.isFinite(y[i]!) && Number.isFinite(x[i]!)) cur.push(`${x[i]!.toFixed(2)},${y[i]!.toFixed(2)}`);
    else if (cur.length) (out.push(cur), (cur = []));
  }
  if (cur.length) out.push(cur);
  return out;
}

export function SpectrumPlot({ v }: { v: Viewer }) {
  const sp: Spectrum | null = v.spectrum;
  const queued = useQueueReason(v.spectrumLoading && !sp, v.spectrumRequestId);
  if (v.interp) return <p className={s.note}>A spectrum averages values over channels, which is not defined for angles of an axial map. See the orientation statistics.</p>;
  if (!v.region) return <p className={s.note}>Draw a region (Region section) to see its mean spectrum.</p>;
  if (v.spectrumError) return <p role="alert" className={s.err}>{v.spectrumError}</p>;
  if (!sp) return <><p className={s.note}>{v.spectrumLoading ? "Computing the spectrum…" : EM}</p><QueueNote reason={queued} className={s.note} /></>;

  const { factor, label } = axisDisplay(sp.head.axis.unit);
  const n = sp.head.channels;
  const xs = Array.from(sp.axis, (a) => a * factor);
  const ys = Array.from(sp.mean);
  const finite = ys.filter(Number.isFinite);
  if (finite.length === 0) {
    return <p className={s.note}>No pixel in this region has a value in any channel, so there is no spectrum to draw.</p>;
  }
  const xlo = Math.min(...xs);
  const xhi = Math.max(...xs);
  const ylo = Math.min(...finite);
  const yhi = Math.max(...finite);
  const px = (x: number) => M.l + (xhi === xlo ? 0.5 : (x - xlo) / (xhi - xlo)) * (W - M.l - M.r);
  const py = (y: number) => H - M.b - (yhi === ylo ? 0.5 : (y - ylo) / (yhi - ylo)) * (H - M.b - M.t);
  const lines = segments(xs.map(px), ys.map(py));
  const peak = ys.reduce((b, y, i) => (Number.isFinite(y) && (b < 0 || y > ys[b]!) ? i : b), -1);
  const cur = v.plane?.head.channel ?? v.channel;
  const flagged = v.view?.flags.declared ? v.view.flags.channels : [];
  const xName = sp.head.axis.kind === "spectral" ? `${sp.head.axis.name ?? "spectral"} (${label})` : "channel number (spectral axis unresolved)";
  const unit = unitLabel(sp.head.bunit, sp.head.bunit_status);
  const missing = ys.filter((y) => !Number.isFinite(y)).length;

  function pick(e: MouseEvent<SVGSVGElement>) {
    const r = e.currentTarget.getBoundingClientRect();
    const x = ((e.clientX - r.left) / r.width) * W;
    let best = 0;
    for (let i = 1; i < n; i++) if (Math.abs(px(xs[i]!) - x) < Math.abs(px(xs[best]!) - x)) best = i;
    v.setChannel(best);
  }

  return (
    <div className={s.wrap}>
      <svg
        viewBox={`0 0 ${W} ${H}`}
        className={s.svg}
        role="img"
        aria-label={`Mean spectrum of the region over ${n} channels. Peak ${num(ys[peak]!)}${sp.head.bunit ? ` ${unitLabel(sp.head.bunit, sp.head.bunit_status)}` : " (brightness unit unresolved)"} at channel ${peak + 1}. ${missing} channels have no value.`}
        onClick={pick}
      >
        {flagged.map((c) => <line key={c} className={s.flag} x1={px(xs[c]!)} x2={px(xs[c]!)} y1={H - M.b} y2={H - M.b + 6} />)}
        <line className={s.axis} x1={M.l} x2={W - M.r} y1={H - M.b} y2={H - M.b} />
        {lines.map((l, i) => (l.length === 1 ? <circle key={i} className={s.dot} cx={Number(l[0]!.split(",")[0])} cy={Number(l[0]!.split(",")[1])} r={2} /> : <polyline key={i} className={s.line} points={l.join(" ")} />))}
        <line className={s.cursor} x1={px(xs[cur]!)} x2={px(xs[cur]!)} y1={M.t} y2={H - M.b} />
        <text className={s.tick} x={M.l} y={H - M.b + 18}>{num(xlo)}</text>
        <text className={s.tick} x={W - M.r} y={H - M.b + 18} textAnchor="end">{num(xhi)}</text>
        <text className={s.tick} x={W / 2} y={H - 4} textAnchor="middle">{xName}</text>
        <text className={s.tick} x={M.l - 5} y={py(yhi) + 4} textAnchor="end">{num(yhi)}</text>
        <text className={s.tick} x={M.l - 5} y={py(ylo) + 4} textAnchor="end">{num(ylo)}</text>
        <text className={s.tick} transform={`translate(11 ${(M.t + H - M.b) / 2}) rotate(-90)`} textAnchor="middle">{sp.head.bunit ?? "unit unresolved"}</text>
      </svg>
      <p className={s.note}>
        Mean over the pixels with a value, in {unit}. {missing > 0 ? `${missing} of ${n} channels have no pixel with a value and are left as gaps. ` : ""}Click the plot to go to that channel.
      </p>
      <details className={s.table}>
        <summary>Spectrum values as a table</summary>
        <div className={s.scroll} role="region" aria-label="Spectrum table" tabIndex={0}>
          <table>
            <thead>
              <tr><th scope="col">Channel</th><th scope="col">{xName}</th><th scope="col">Mean ({unit})</th><th scope="col">Pixels with a value</th></tr>
            </thead>
            <tbody>
              {ys.map((y, i) => (
                <tr key={i}>
                  <th scope="row">{i + 1}</th>
                  <td className="mono">{num(xs[i]!)}</td>
                  <td className="mono">{Number.isFinite(y) ? num(y) : EM}</td>
                  <td className="mono">{sp.count[i]!.toLocaleString("en-US")}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </details>
    </div>
  );
}
