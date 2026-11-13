import { useState } from "react";
import { Basis, Input, ScrollTable, Select, Switch } from "../ui";
import type { Components } from "../api/rohsa";
import type { Spectrum } from "../api/planes";
import type { Viewer } from "./useViewer";
import { EM } from "../catalog/format";
import { segments } from "./SpectrumPlot";
import { CNM_SIGMA_DEFAULT_KMS, CNM_TD_MAX_K, DOPPLER_K_PER_KMS2, SIGN_NOTE, T_DOPPLER_NOTE, classify, cutSigma, cutText as cutTextOf, residual, tdOfSigma } from "./rohsa";
import type { CutBasis, Phase } from "./rohsa";
import { GaussianReview } from "./GaussianReview";
import { useComponents } from "./useComponents";
import s from "./GaussianComponents.module.css";

const W = 320;
const H = 176;
const HR = 72;
// the left margin holds the value labels and the unit, so no text sits on a curve; the top one holds the component numbers
const M = { l: 76, r: 10, t: 18, b: 40 };
const num = (v: number) => Number(v.toPrecision(4)).toString();
const numOrEm = (v: number | null) => (v === null ? EM : num(v));

/** The record's centre and width facts as one sentence, written from what the record says (never from the tool's name). */
function recordText(r: Components["head"]["record"]): string {
  const c = r.centre;
  const w = r.width;
  const centre = c?.unit === "channel"
    ? `channel numbers counted from ${c.base ?? "an undeclared base"}`
    : c?.unit ? `${c.unit}${c.frame ? `, frame ${c.frame}` : ", frame not declared"}` : "a unit that is not declared";
  const width = `${w?.kind ?? "a convention that is not declared"}${w?.unit ? ` in ${w.unit}` : ", unit not declared"}`;
  return `Centre: ${centre}. Width: ${width}${w?.to_sigma ? ` (${w.to_sigma})` : ""}.`;
}

function Legend({ showData }: { showData: boolean }) {
  const item = (cls: string, label: string) => (
    <li>
      <svg viewBox="0 0 20 8" width="20" height="8" aria-hidden="true"><line x1="1" x2="19" y1="4" y2="4" className={cls} /></svg>
      {label}
    </li>
  );
  return (
    <ul className={s.legend} aria-label="Legend">
      {showData ? item(s.data!, "data") : null}
      {item(s.model!, "sum")}
      {item(s.cnm!, "CNM")}
      {item(s.other!, "other")}
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
        {line(comps.model, s.model!, "m")}
        {drawn.map((c, k) => (c ? line(c, phases[k] === "cnm" ? s.cnm! : s.other!, `g${k}`) : null))}
        {drawn.map((c, k) => {
          const comp = comps.head.components[k]!;
          if (!c || comp.amp === null || !(comp.amp > 0)) return null;
          const at = velocity ? comp.centre : comp.mean_channel;
          return at === null ? null : <text key={`t${k}`} className={s.tag} x={px(at)} y={Math.max(11, py(comp.amp) - 4)} textAnchor="middle">{comp.k}</text>;
        })}
        <text className={s.tick} x={M.l} y={H - M.b + 18}>{num(xlo)}</text>
        <text className={s.tick} x={W - M.r} y={H - M.b + 18} textAnchor="end">{num(xhi)}</text>
        <text className={s.tick} x={W / 2} y={H - 4} textAnchor="middle">{xName}</text>
        <text className={s.tick} x={M.l - 5} y={py(yhi) + 4} textAnchor="end">{num(yhi)}</text>
        <text className={s.tick} x={M.l - 5} y={py(ylo) + 4} textAnchor="end">{num(ylo)}</text>
        <text className={s.tick} transform={`translate(11 ${(M.t + H - M.b) / 2}) rotate(-90)`} textAnchor="middle">{unit}</text>
      </svg>
      {res ? (
        <svg viewBox={`0 0 ${W} ${HR}`} className={s.svg} role="img" aria-label={`Residual, data minus model, within ±${num(rmax)} ${unit}`}>
          <line className={s.zero} x1={M.l} x2={W - M.r} y1={pr(0)} y2={pr(0)} />
          {segments(xs.map(px), Array.from(res, pr)).map((l, i) => <polyline key={i} className={s.resid} points={l.join(" ")} />)}
          <text className={s.tick} x={M.l - 5} y={14} textAnchor="end">{`+${num(rmax)}`}</text>
          <text className={s.tick} x={M.l - 5} y={HR - 6} textAnchor="end">{`−${num(rmax)}`}</text>
          <text className={s.tick} transform={`translate(11 ${HR / 2}) rotate(-90)`} textAnchor="middle">residual</text>
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
  const [cutBasis, setCutBasis] = useState<CutBasis>("sigma");
  const [cutText, setCutText] = useState(String(CNM_SIGMA_DEFAULT_KMS));
  if (st.runs.length === 0) return null;
  const run = st.run!;
  const cut = cutSigma(cutText, cutBasis);       // always the width in km/s: classification and the class maps use it
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
  // the record may not say enough to place the components on the spectrum: then the table lists the source's own values and nothing is computed
  const drawn = comps ? comps.head.drawn : true;
  const rec = comps?.head.record;

  return (
    <>
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
        <p className={s.note}>Draws the {run.nGauss} fitted Gaussians of <span className="mono">{run.name}</span> over the spectrum of one pixel, from the run&rsquo;s own output files.</p>
      ) : null}
      {st.enabled && st.usable ? (
        <>
          {st.indexError ? <p role="alert" className={s.err}>{st.indexError}</p> : null}
          {idx?.state === "building" || (!idx && !st.indexError) ? (
            <p role="status" className={s.note}>{idx?.where === "host" ? "The host is reading the .dat once to find each pixel" : "Reading the .dat once to find each pixel"}{idx ? `: ${pct}%` : "…"} {idx?.where === "host" ? "The index is kept in the account's cache folder on the host and reused; the .dat does not leave it." : "The index is kept and reused."}</p>
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
              {drawn ? (
                <>
                  <Plot comps={comps} data={st.data} cut={cut} phases={phases} drawData={st.data !== null && unitProblem === null} />
                  <Legend showData={st.data !== null && unitProblem === null} />
                  {unitProblem ? <p className={s.note}>{unitProblem}</p> : <p className={s.note}>data: this pixel&rsquo;s spectrum · sum: the sum of the components · CNM (dashed) and other (dotted): single components. {SIGN_NOTE}.</p>}
                </>
              ) : (
                <p role="status" className={s.note}>Not drawn: {comps.head.not_drawn_reason ?? "the record does not say enough to place the components on the spectrum"}. The table lists the values as the source states them; nothing is computed from the facts that are missing.</p>
              )}
              {drawn ? (
                <>
                  <Select label="Set the CNM cut as" value={cutBasis} onChange={(e) => {
                    const next = e.target.value as CutBasis;
                    if (cut !== null) setCutText(cutTextOf(cut, next));
                    setCutBasis(next);
                  }}>
                    <option value="sigma">Width σ (km/s)</option>
                    <option value="td">Doppler temperature T_D (K)</option>
                  </Select>
                  <Input
                    label={cutBasis === "sigma" ? "CNM width cut σ (km/s)" : "CNM Doppler temperature cut T_D (K)"}
                    inputMode="decimal"
                    value={cutText}
                    onChange={(e) => setCutText(e.target.value)}
                    aria-invalid={cut === null}
                  />
                  {cut === null ? <p className={s.err}>Enter a positive number.</p> : (
                    <Basis summary={`Your cut: a component is CNM when σ < ${Number(cut.toPrecision(4))} km/s (T_D < ${Number(tdOfSigma(cut).toPrecision(4))} K), otherwise "others".`}>
                      <p>T_D = {DOPPLER_K_PER_KMS2} K (km/s)⁻² × σ² is the Doppler temperature of a Gaussian of width σ for atomic hydrogen. A common upper bound on the kinetic temperature of cold neutral gas is {CNM_TD_MAX_K} K, which is σ &lt; {CNM_SIGMA_DEFAULT_KMS} km/s: that is the starting value. It is a convention, not a result; change it freely. Two groups only: nothing here separates the others further.</p>
                    </Basis>
                  )}
                </>
              ) : null}
              {!velocity ? <p className={s.note}>{comps.head.axis.note} Widths and the CNM split need a velocity axis, so every component is shown unclassified.</p> : null}
              {comps.head.axis.unit_status === "inferred" ? <p className={s.note}>The crop has no CUNIT for its spectral axis: {comps.head.axis.source_unit} is the FITS default for {comps.head.axis.name ?? "this axis"}, assumed here.</p> : null}
              <ScrollTable label="Fitted components" className={s.scroll}>
                <table>
                  <thead>
                    <tr>
                      <th scope="col">#</th>
                      <th scope="col">Amplitude ({amp})</th>
                      {drawn ? (
                        <>
                          <th scope="col">{velocity ? "Centre (km/s)" : "Centre (channel)"}</th>
                          <th scope="col">{velocity ? "σ (km/s)" : "σ (channels)"}</th>
                          {velocity ? <th scope="col">FWHM (km/s)</th> : null}
                          {velocity ? <th scope="col">T_D (K)</th> : null}
                          <th scope="col">Group</th>
                        </>
                      ) : (
                        <>
                          <th scope="col">Centre as stated ({rec?.centre?.unit ?? "unit not declared"})</th>
                          <th scope="col">Width as stated ({rec?.width?.kind ?? "convention not declared"}, {rec?.width?.unit ?? "unit not declared"})</th>
                        </>
                      )}
                    </tr>
                  </thead>
                  <tbody>
                    {comps.head.components.map((c, k) => !c.present ? (
                      <tr key={c.k}>
                        <th scope="row">{c.k}</th>
                        <td colSpan={drawn ? (velocity ? 6 : 4) : 3}>not fitted at this pixel</td>
                      </tr>
                    ) : !drawn ? (
                      <tr key={c.k}>
                        <th scope="row">{c.k}</th>
                        <td className="mono">{numOrEm(c.amp)}</td>
                        <td className="mono">{numOrEm(c.centre_raw)}</td>
                        <td className="mono">{numOrEm(c.width_raw)}</td>
                      </tr>
                    ) : (
                      <tr key={c.k}>
                        <th scope="row">{c.k}</th>
                        <td className="mono">{numOrEm(c.amp)}</td>
                        <td className="mono">{velocity ? numOrEm(c.centre) : numOrEm(c.mean_channel)}</td>
                        <td className="mono">{velocity ? numOrEm(c.sigma) : numOrEm(c.sigma_channel)}</td>
                        {velocity ? <td className="mono">{numOrEm(c.fwhm)}</td> : null}
                        {velocity ? <td className="mono">{numOrEm(c.t_doppler_k)}</td> : null}
                        <td>{!c.valid ? "invalid σ, not drawn" : phases[k] === "cnm" ? "CNM" : phases[k] === "other" ? "others" : EM}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </ScrollTable>
              <Basis summary={drawn ? "T_D is an upper limit on the kinetic temperature (thermal broadening only). An amplitude of 0 is a fitted value, not a missing one." : "How the source's values are read"}>
                {drawn ? <p>{T_DOPPLER_NOTE}. FWHM = 2√(2 ln 2) σ.</p> : null}
                <p>{rec ? recordText(rec) : ""} {rec?.centre?.unit === "channel" ? "They are placed through the crop’s spectral axis." : "A centre in a velocity unit is placed on the crop’s axis only when its frame is the cube’s."}</p>
                {rec?.notes?.map((n) => <p key={n}>{n}</p>)}
              </Basis>
            </>
          ) : null}
        </>
      ) : null}
    </div>
    {st.usable ? <GaussianReview st={st} v={v} cut={cut} /> : null}
    </>
  );
}
