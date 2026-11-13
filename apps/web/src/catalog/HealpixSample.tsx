import { useEffect, useRef, useState } from "react";
import { Button, Input, Section, Select } from "../ui";
import { QueueNote, useQueueReason } from "../ui/QueueNote";
import { newRequestId } from "../api/http";
import { isImage, type DatasetSummary } from "../api/catalog";
import type { HealpixDataset } from "../api/catalog";
import { getDerived, runHealpixSample } from "../api/analysis";
import type { Derived, HealpixSampleRequest, HealpixSampleResult } from "../api/analysis";
import s from "./AnalysisPanel.module.css";

/* Sampling a HEALPix polarization map onto a map's celestial grid (ADR 0067). The server states every refusal; this form only
   keeps the person from asking for what it can see will be refused, and says why. */

const CELESTIAL_AXIS = /^(RA|DEC|[A-Z]LON|[A-Z]LAT)(-|$)/i;

/** Why a dataset cannot receive a HEALPix map, or null when it can. Only what the header shows: the server decides again (a
    WCS correction, a missing projection) and its sentence is shown as it is. */
export function targetReason(d: DatasetSummary): string | null {
  if (!isImage(d)) return "it has no pixel grid";
  if (d.hostId !== 0) return "it lives on a host, and sampling runs on this computer only: download it with Transfers";
  const types = d.facts.ctypes;
  if (types.length < 2 || !types.every((t) => CELESTIAL_AXIS.test(t))) return "its first two axes are not a celestial pair, so it has no sky grid";
  return null;
}

/** Why the map itself cannot be sampled, or null. */
export function mapReason(d: HealpixDataset): string | null {
  if (d.hostId !== 0) return "this map lives on a host, and sampling runs on this computer only: download it with Transfers and register the copy";
  if (d.header.frame !== "galactic") return `this map is in the ${d.header.frame} system (COORDSYS = ${d.header.coordsys}); only Galactic maps are sampled in this version`;
  return null;
}

const PRODUCTS = [
  { id: "dust_polarization", label: "Dust polarization angle", help: "The angle of the dust's polarization, ψ = ½ atan2(U, Q), east of north in the target's celestial frame." },
  { id: "b_field", label: "Field orientation", help: "The magnetic-field orientation on the plane of the sky: the dust polarization angle plus 90°, since dust polarization is perpendicular to the field." },
] as const;
const METHODS = [
  { id: "nearest", label: "Nearest pixel" },
  { id: "bilinear", label: "Bilinear (four pixels)" },
  { id: "gaussian", label: "Gaussian smoothing" },
] as const;

export function HealpixSamplePanel({ dataset, datasets, onResult }: { dataset: HealpixDataset; datasets: DatasetSummary[]; onResult: (r: HealpixSampleResult) => void }) {
  const h = dataset.header;
  const [target, setTarget] = useState("");
  const [product, setProduct] = useState<HealpixSampleRequest["product"]>("b_field");
  const [method, setMethod] = useState<NonNullable<HealpixSampleRequest["method"]>>("nearest");
  const [fwhm, setFwhm] = useState("");
  const [snr, setSnr] = useState("");
  const [busy, setBusy] = useState(false);
  const [rid, setRid] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const queued = useQueueReason(busy, rid);
  const abort = useRef<AbortController | null>(null);
  useEffect(() => () => abort.current?.abort(), []);

  const candidates = datasets.filter((d) => d.id !== dataset.id && !d.archived && isImage(d)).map((d) => ({ d, why: targetReason(d) }));
  const refused = candidates.filter((c) => c.why !== null);
  const why = mapReason(dataset);
  const num = (t: string) => (t.trim() === "" ? null : Number(t));
  const fwhmN = num(fwhm);
  const snrN = num(snr);
  const fwhmBad = method === "gaussian" && (fwhmN === null || !Number.isFinite(fwhmN) || fwhmN <= 0 || fwhmN > 600);
  const snrBad = snrN !== null && (!Number.isFinite(snrN) || snrN < 0);
  const product1 = PRODUCTS.find((p) => p.id === product)!;

  async function go() {
    if (!target) return;
    setBusy(true);
    const id = newRequestId();
    setRid(id);
    setErr(null);
    const ctl = new AbortController();
    abort.current = ctl;
    const req: HealpixSampleRequest = { datasetId: dataset.id, targetId: Number(target), product, method, ...(method === "gaussian" ? { fwhmArcmin: fwhmN } : {}), ...(snrN !== null ? { snrMin: snrN } : {}) };
    try {
      onResult(await runHealpixSample(req, ctl.signal, id));
    } catch (e) {
      setErr(ctl.signal.aborted ? "Cancelled: the computation was stopped and nothing was kept." : e instanceof Error ? e.message : "The map was not sampled.");
    }
    abort.current = null;
    setBusy(false);
  }

  return (
    <div className={s.wrap}>
      <dl className={s.facts} aria-label="What this HEALPix map is">
        <dt>NSIDE</dt><dd className="mono">{h.nside} ({h.npix.toLocaleString("en-US")} pixels, {h.pixel_resolution_arcmin.toFixed(1)}′ across)</dd>
        <dt>Ordering</dt><dd className="mono">{h.ordering}</dd>
        <dt>Coordinate system</dt><dd><span className="mono">{h.coordsys}</span> ({h.frame})</dd>
        <dt>Convention</dt><dd><span className="mono">{h.polcconv}</span>{h.polcconv === "COSMO" ? ": U has the opposite sign to IAU, flipped when sampled" : ": IAU, used as it is"}</dd>
        <dt>QU covariance</dt><dd>{h.has_qu_cov ? "present" : "absent: the Q–U covariance is taken as 0 in σ_P"}</dd>
      </dl>
      <p className={s.note}>All of this is what the file's header states. It has no pixel grid to view: it is sampled onto a map's celestial grid as Stokes Q and U, and the angle is formed afterwards.</p>
      {why ? <p className={s.note}>{why}.</p> : (
        <>
          <Select label="Sample onto" value={target} onChange={(e) => setTarget(e.target.value)}>
            <option value="">Choose a map with a celestial grid…</option>
            {candidates.map(({ d, why: w }) => <option key={d.id} value={d.id} disabled={w !== null}>{d.name}{w !== null ? " (unavailable)" : ""}</option>)}
          </Select>
          {candidates.length === 0 ? <p className={s.note}>No map is registered yet. Register the map whose grid should receive this one.</p> : null}
          {refused.length ? (
            <ul className={s.note} aria-label="Datasets that cannot receive this map">
              {refused.map(({ d, why: w }) => <li key={d.id}>{d.name}: {w}.</li>)}
            </ul>
          ) : null}
          <Select label="Product" value={product} onChange={(e) => setProduct(e.target.value as typeof product)}>
            {PRODUCTS.map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
          </Select>
          <p className={s.note}>{product1.help}</p>
          <Select label="Method" value={method} onChange={(e) => setMethod(e.target.value as typeof method)}>
            {METHODS.map((m) => <option key={m.id} value={m.id}>{m.label}</option>)}
          </Select>
          {method === "gaussian" ? <Input label="Gaussian FWHM (arcmin)" value={fwhm} onChange={(e) => setFwhm(e.target.value)} inputMode="decimal" hint={`at least ${h.pixel_resolution_arcmin.toFixed(1)}′ (the pixel size), at most 600′`} /> : null}
          <Input label="Blank the angle where P/σ_P is below (optional)" value={snr} onChange={(e) => setSnr(e.target.value)} inputMode="decimal" placeholder="no cut" />
          <p className={s.note}>P is not debiased. Correlated noise between HEALPix pixels (beam, scanning, smoothing) is not modelled, so σ_P understates the uncertainty where pixels are correlated. P and σ_P are kept everywhere; only the angle is blanked.</p>
          {fwhmBad ? <p className={s.err}>The Gaussian needs a FWHM above 0 and at most 600 arcmin.</p> : null}
          {snrBad ? <p className={s.err}>The P/σ_P cut must be a number, 0 or more.</p> : null}
          <Button variant="primary" disabled={busy || !target || fwhmBad || snrBad} onClick={go}>{busy ? "Sampling…" : "Sample onto the map"}</Button>
          {busy ? <Button onClick={() => abort.current?.abort()}>Cancel</Button> : null}
          <QueueNote reason={queued} />
        </>
      )}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
    </div>
  );
}

/** The inspector section of a map made by a HEALPix sampling: how it was derived and links to its P and σ_P maps, read from the map's
    own derived record, so it is there whenever the map is opened. Renders nothing for any other dataset. */
export function HealpixSampleDone({ dataset, datasets, onOpen }: { dataset: DatasetSummary; datasets: DatasetSummary[]; onOpen: (d: DatasetSummary) => void }) {
  const [rec, setRec] = useState<Derived | null>(null);
  useEffect(() => {
    setRec(null);
    getDerived(dataset.id).then((d) => setRec(d.op === "healpix_sample" ? d : null)).catch(() => setRec(null));
  }, [dataset.id]);
  if (!rec) return null;
  const r = rec.summary as Partial<HealpixSampleResult["summary"]> & { companionOf?: number };
  if (r.companionOf !== undefined) return null; // a companion's own record: the angle map holds the story
  const ids = rec.params.companions ?? {};
  const link = (key: "p" | "sigmaP", label: string) => {
    const d = ids[key] === undefined ? undefined : datasets.find((x) => x.id === ids[key]);
    return d ? <Button onClick={() => onOpen(d)}>{label}</Button> : <p className={s.note}>{label.replace("Open ", "")}: {ids[key] === undefined ? "not recorded for this map" : "no longer registered"}.</p>;
  };
  return (
    <Section title="Sampled from a HEALPix map">
      <div className={s.wrap} role="region" aria-label="Sampled from a HEALPix map">
        <p className={s.note}>{r.product === "b_field" ? "Field orientation" : "Dust polarization angle"} from a HEALPix map. Companion maps on the same grid:</p>
        <div className={s.two}>{link("p", "Open P")}{link("sigmaP", "Open σ_P")}</div>
        <p className={s.note}>{r.derivation?.sampling} {r.derivation?.noise}</p>
        <p className={s.note}>{r.policy}</p>
      </div>
    </Section>
  );
}

