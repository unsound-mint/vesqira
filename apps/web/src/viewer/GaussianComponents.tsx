import { useState } from "react";
import { Input, Select, Switch } from "../ui";
import type { Components } from "../api/rohsa";
import type { Spectrum } from "../api/planes";
import type { Viewer } from "./useViewer";
import { EM } from "../catalog/format";
import { segments } from "./SpectrumPlot";
import { PIPELINE_CNM_SIGMA_KMS, SIGN_NOTE, T_DOPPLER_NOTE, classify, parseCut, residual } from "./rohsa";
import type { Phase } from "./rohsa";
import { useComponents } from "./useComponents";
import s from "./GaussianComponents.module.css";

const W = 320;
const H = 176;
const HR = 72;
const M = { l: 8, r: 8, t: 8, b: 40 };
const num = (v: number) => Number(v.toPrecision(4)).toString();

function Legend({ showData }: { showData: boolean }) {
  const item = (cls: string, label: string) => (
    <li>
      <svg viewBox="0 0 28 8" width="28" height="8" aria-hidden="true"><line x1="1" x2="27" y1="4" y2="4" className={cls} /></svg>
      {label}
    </li>
  );
  return (
    <ul className={s.legend} aria-label="Legend">
      {showData ? item(s.data!, "data (this pixel)") : null}
      {item(s.model!, "model: sum of components")}
      {item(s.cnm!, "CNM component (dashed)")}
      {item(s.other!, "other component (dotted)")}
    </ul>
  );
}

function Plot({ comps, data, cut, phases, drawData }: { comps: Components; data: Spectrum | null; cut: number | null; phases: (Phase | null)[]; drawData: boolean }) {
  const xs = Array.from(comps.axis);
  const velocity = comps.head.axis.kind === "velocity";
  const dataY = drawData && data ? Array.from(data.mean) : [];
  const drawn = comps.curves.map((c, k) => (comps.head.components[k]!.valid ? c : null));
  const all = [...dataY.filter(Number.isFinite), ...comps.model, ...drawn.flatMap((c) => (c ? Array.from(c) : []))];
  const ylo = Math.min(0, ...all);
  const yhi = Math.max(...all);
  const xlo = Math.min(...xs);
  const xhi = Math.max(...xs);
  const px = (x: number) => M.l + (xhi === xlo ? 0.5 : (x - xlo) / (xhi - xlo)) * (W - M.l - M.r);
  const py = (y: number) => H - M.b - (yhi === ylo ? 0.5 : (y - ylo) / (yhi - ylo)) * (H - M.b - M.t);
  const line = (ys: ArrayLike<number>, cls: string, key: string) =>
    segments(xs.map(px), Array.from(ys, py)).map((l, i) => <polyline key={`${key}${i}`} className={cls} points={l.join(" ")} />);
  const unit = comps.head.bunit ?? "brightness unit unresolved";
  const xName = velocity ? `${comps.head.axis.name ?? "velocity"} (km/s)` : "channel number (no resolved velocity axis)";
  const res = drawData && data ? residual(data.mean, comps.model) : null;
  const rf = res ? Array.from(res).filter(Number.isFinite) : [];
  const rmax = rf.length ? Math.max(...rf.map(Math.abs)) || 1 : 1;
  const pr = (y: number) => HR / 2 - (y / rmax) * (HR / 2 - 6);
  return (
    <>
      <svg
        viewBox={`0 0 ${W} ${H}`}
        className={s.svg}
        role="img"
        aria-label={`Gaussian components at pixel ${comps.head.x + 1}, ${comps.head.y + 1}: ${comps.head.n_gauss} components and their sum${drawData ? ", with the pixel's own spectrum" : ""}.${cut !== null ? ` CNM is a width below ${cut} km/s.` : ""}`}
      >
        <line className={s.axisLine} x1={M.l} x2={W - M.r} y1={H - M.b} y2={H - M.b} />
        <line className={s.zero} x1={M.l} x2={W - M.r} y1={py(0)} y2={py(0)} />
        {dataY.length ? line(dataY, s.data!, "d") : null}
        {drawn.map((c, k) => (c ? line(c, phases[k] === "cnm" ? s.cnm! : s.other!, `g${k}`) : null))}
        {line(comps.model, s.model!, "m")}
        {drawn.map((c, k) => {
          const comp = comps.head.components[k]!;
          if (!c || !(comp.amp > 0)) return null;
          const at = velocity ? comp.centre : comp.mean_channel;
          return at === null ? null : <text key={`t${k}`} className={s.tag} x={px(at)} y={py(comp.amp) - 3} textAnchor="middle">{comp.k}</text>;
        })}
        <text className={s.tick} x={M.l} y={H - M.b + 18}>{num(xlo)}</text>
        <text className={s.tick} x={W - M.r} y={H - M.b + 18} textAnchor="end">{num(xhi)}</text>
        <text className={s.tick} x={W / 2} y={H - 4} textAnchor="middle">{xName}</text>
        <text className={s.tick} x={M.l + 2} y={M.t + 10}>{num(yhi)} {unit}</text>
        <text className={s.tick} x={M.l + 2} y={H - M.b - 4}>{num(ylo)}</text>
      </svg>
      {res ? (
        <svg viewBox={`0 0 ${W} ${HR}`} className={s.svg} role="img" aria-label={`Residual, data minus model, within ±${num(rmax)} ${unit}`}>
          <line className={s.zero} x1={M.l} x2={W - M.r} y1={pr(0)} y2={pr(0)} />
          {segments(xs.map(px), Array.from(res, pr)).map((l, i) => <polyline key={i} className={s.resid} points={l.join(" ")} />)}
          <text className={s.tick} x={M.l + 2} y={10}>{`+${num(rmax)}`}</text>
          <text className={s.tick} x={M.l + 2} y={HR - 4}>{`−${num(rmax)}`}</text>
          <text className={s.tick} x={W - M.r} y={10} textAnchor="end">residual</text>
        </svg>
      ) : null}
    </>
  );
}

/**
 * The Gaussian components of a ROHSA fit over the spectrum at one pixel: each component, their sum, and the residual, with
 * a table of the fitted values and a width cut that marks CNM against the rest. Two phases only; the cut is the person's.
 */
export function GaussianComponents({ v }: { v: Viewer }) {
  const st = useComponents(v);
  const [cutText, setCutText] = useState(String(PIPELINE_CNM_SIGMA_KMS));
  if (st.runs.length === 0) return null;
  const run = st.run!;
  const cut = parseCut(cutText);
  const comps = st.comps;
  const velocity = comps?.head.axis.kind === "velocity";
  const phases: (Phase | null)[] = comps ? comps.head.components.map((c) => (velocity && c.valid && c.sigma !== null && cut !== null ? classify(c.sigma, cut) : null)) : [];
  const dataUnit = st.data?.head.bunit ?? null;
  const unitProblem = comps && st.data && (dataUnit === null || run.cropBunit === null || dataUnit.trim() !== run.cropBunit.trim())
    ? `The data's brightness unit (${dataUnit ?? "unresolved"}) is not confirmed equal to the unit of the fit's amplitudes (${run.cropBunit ?? "unresolved"}), so the data and residual are not drawn.`
    : null;
  const idx = st.index;
  const pct = idx && idx.size_bytes > 0 ? Math.floor((100 * idx.done_bytes) / idx.size_bytes) : 0;
  const amp = comps?.head.bunit ?? "unit unresolved";

  return (
    <div className={s.wrap} role="group" aria-label="Gaussian components">
      <Switch label="Gaussian components" checked={st.enabled && st.usable} disabled={!st.usable} onChange={(e) => st.setEnabled(e.target.checked)} />
      {st.runs.length > 1 ? (
        <Select label="Decomposition run" value={String(st.chosen ?? "")} onChange={(e) => st.setChosen(Number(e.target.value))}>
          {st.runs.map((r) => <option key={r.runId} value={r.runId}>{r.name}{r.usable ? "" : " (not for this dataset)"}</option>)}
        </Select>
      ) : null}
      {!st.usable ? (
        <p className={s.note}>{run.refusal ?? "These components cannot be shown over this dataset."}</p>
      ) : !st.enabled ? (
        <p className={s.note}>Draws the {run.nGauss} fitted Gaussians of <span className="mono">{run.name}</span> over the spectrum of one pixel, from the raw ROHSA output.</p>
      ) : null}
      {st.enabled && st.usable ? (
        <>
          {st.indexError ? <p role="alert" className={s.err}>{st.indexError}</p> : null}
          {idx?.state === "building" || (!idx && !st.indexError) ? (
            <p role="status" className={s.note}>Reading the .dat once to find each pixel{idx ? `: ${pct}%` : "…"} The index is kept and reused.</p>
          ) : null}
          {idx?.state === "refused" ? (
            <div role="alert" className={s.err}>
              <p>The .dat cannot be used: {idx.reason}</p>
              {idx.examples.length > 0 ? <ul>{idx.examples.slice(0, 3).map((e) => <li key={e.line} className="mono">line {e.line}: {e.problem}</li>)}</ul> : null}
            </div>
          ) : null}
          {idx?.state === "ready" && !st.pixel ? <p className={s.note}>Point at a pixel, or click one to pin it.</p> : null}
          {st.error ? <p role="alert" className={s.err}>{st.error}</p> : null}
          {idx?.state === "ready" && st.pixel && !comps && !st.error ? <p role="status" className={s.note}>{st.loading ? "Reading the components…" : EM}</p> : null}
          {comps ? (
            <>
              <p className={s.pixel}>Pixel <span className="mono">{comps.head.x + 1}, {comps.head.y + 1}</span> <span className={s.note}>(FITS numbering, from 1)</span></p>
              <Plot comps={comps} data={st.data} cut={cut} phases={phases} drawData={st.data !== null && unitProblem === null} />
              <Legend showData={st.data !== null && unitProblem === null} />
              {unitProblem ? <p className={s.note}>{unitProblem}</p> : <p className={s.note}>{SIGN_NOTE}.</p>}
              <Input
                label="CNM width cut σ (km/s)"
                inputMode="decimal"
                value={cutText}
                onChange={(e) => setCutText(e.target.value)}
                aria-invalid={cut === null}
              />
              <p className={cut === null ? s.err : s.note}>
                {cut === null
                  ? "Enter a positive number."
                  : `Your cut: a component is CNM when σ < ${cut} km/s, otherwise "others". The starting value ${PIPELINE_CNM_SIGMA_KMS} km/s is the pipeline's scan-script default (T_D < 500 K); change it freely. Two groups only: nothing here separates the others further.`}
              </p>
              {!velocity ? <p className={s.note}>{comps.head.axis.note} Widths and the CNM split need a velocity axis, so every component is shown unclassified.</p> : null}
              {comps.head.axis.unit_status === "inferred" ? <p className={s.note}>The crop has no CUNIT for its spectral axis: {comps.head.axis.source_unit} is the FITS default for {comps.head.axis.name ?? "this axis"}, assumed here.</p> : null}
              <div className={s.scroll} role="region" aria-label="Fitted components" tabIndex={0}>
                <table>
                  <thead>
                    <tr>
                      <th scope="col">#</th>
                      <th scope="col">Amplitude ({amp})</th>
                      <th scope="col">{velocity ? "Centre (km/s)" : "Centre (channel)"}</th>
                      <th scope="col">{velocity ? "σ (km/s)" : "σ (channels)"}</th>
                      {velocity ? <th scope="col">FWHM (km/s)</th> : null}
                      {velocity ? <th scope="col">T_D (K)</th> : null}
                      <th scope="col">Group</th>
                    </tr>
                  </thead>
                  <tbody>
                    {comps.head.components.map((c, k) => (
                      <tr key={c.k}>
                        <th scope="row">{c.k}</th>
                        <td className="mono">{num(c.amp)}</td>
                        <td className="mono">{velocity ? (c.centre === null ? EM : num(c.centre)) : num(c.mean_channel)}</td>
                        <td className="mono">{velocity ? (c.sigma === null ? EM : num(c.sigma)) : num(c.sigma_channel)}</td>
                        {velocity ? <td className="mono">{c.fwhm === null ? EM : num(c.fwhm)}</td> : null}
                        {velocity ? <td className="mono">{c.t_doppler_k === null ? EM : num(c.t_doppler_k)}</td> : null}
                        <td>{!c.valid ? "invalid σ, not drawn" : phases[k] === "cnm" ? "CNM" : phases[k] === "other" ? "others" : EM}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <p className={s.note}>{T_DOPPLER_NOTE}. FWHM = 2√(2 ln 2) σ. Centre and σ come from the file's channel numbers (the file counts channels from 1) through the crop's spectral axis. An amplitude of 0 is a fitted value, not a missing one.</p>
            </>
          ) : null}
        </>
      ) : null}
    </div>
  );
}
