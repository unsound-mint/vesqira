import { useCallback, useEffect, useState } from "react";
import type { FormEvent } from "react";
import { ApiError } from "../api/http";
import type { Coord, View } from "../api/planes";
import { deleteSubset, editSubset, getSubset, getSubsetCoord, getSubsetView, materialiseSubset, repinSubset } from "../api/subsets";
import type { SubsetDetail, SubsetMaterialised, SubsetView } from "../api/subsets";
import { Button, Input, Pill } from "../ui";
import { useMasks } from "../viewer/useMasks";
import { MaskCarry, carryChoices } from "./MaskCarry";
import type { CarryState } from "./MaskCarry";
import { EM, bytes } from "./format";
import { SubsetForm } from "./SubsetForm";
import { describe, fieldsOf, summaryLine } from "./subsets";
import s from "./SubsetPage.module.css";

const when = (t: string) => new Date(t).toLocaleString();
const num = (v: number | null | undefined, digits = 6) => (v === null || v === undefined ? EM : String(Number(v.toPrecision(digits))));

/**
 * One virtual subset (ADR 0063): a window onto its source, never a file. It says what the window is, what it resolved to in the source's
 * own pixels and channels (read through the window from the source file, nothing written), a coordinate readout in the subset's own
 * pixels, and the history. A source that changed makes it stale: nothing is read until it is re-pinned to the current file. Writing it
 * as a file makes an ordinary dataset; asking again for the same version of the same bytes reuses that file.
 */
export function SubsetPage({ id, embedded = false, onChanged, onOpenDataset, onClose }: {
  id: number;
  /** In the inspector beside the viewer that reads it: one calm line instead of the title, and no coordinate probe (the viewer has one). */
  embedded?: boolean;
  onChanged: () => void;
  onOpenDataset: (id: number) => void;
  onClose: () => void;
}) {
  const [detail, setDetail] = useState<SubsetDetail | null>(null);
  const [view, setView] = useState<View | null>(null);
  const [viewErr, setViewErr] = useState<string | null>(null);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const [reason, setReason] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [made, setMade] = useState<SubsetMaterialised | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [probe, setProbe] = useState({ x: "1", y: "1", channel: "1" });
  const [coord, setCoord] = useState<Coord | null>(null);
  const [carry, setCarry] = useState<CarryState>({});
  const { masks: sourceMasks } = useMasks(detail?.subset.sourceId ?? null);

  const load = useCallback(async () => {
    setLoadErr(null);
    try {
      const d = await getSubset(id);
      setDetail(d);
      setView(null);
      setViewErr(null);
      if (!d.subset.stale) {
        try {
          setView(await getSubsetView(id));
        } catch (e) {
          setViewErr(e instanceof Error ? e.message : "The subset could not be read from its source.");
        }
      }
    } catch (e) {
      setLoadErr(e instanceof Error ? e.message : "The subset could not be loaded.");
    }
  }, [id]);
  useEffect(() => {
    setMade(null);
    setCoord(null);
    setEditing(false);
    setConfirmDelete(false);
    setCarry({});
    void load();
  }, [load]);

  if (loadErr) return <div className={s.page}><p role="alert" className={s.err}>{loadErr}</p><Button onClick={onClose}>Close</Button></div>;
  if (!detail) return <div className={s.page}><p className={s.note}>Loading the subset…</p></div>;
  const sub: SubsetView = detail.subset;
  const win = view?.window;

  async function run(f: () => Promise<void>) {
    setBusy(true);
    setErr(null);
    try {
      await f();
    } catch (e) {
      setErr(e instanceof ApiError || e instanceof Error ? e.message : "That did not work.");
    }
    setBusy(false);
  }
  const repin = (e: FormEvent) => {
    e.preventDefault();
    void run(async () => {
      await repinSubset(sub.id, reason.trim(), sub.version);
      setReason("");
      onChanged();
      await load();
    });
  };
  const write = () => run(async () => {
    const chosen = carryChoices(sourceMasks ?? [], carry, false).filter((c) => c.carry); // none is carried unless chosen
    const r = chosen.length ? await materialiseSubset(sub.id, sub.version, chosen) : await materialiseSubset(sub.id, sub.version);
    setMade(r);
    onChanged();
    await load();
  });
  const remove = () => run(async () => {
    await deleteSubset(sub.id);
    onChanged();
    onClose();
  });
  const read = (e: FormEvent) => {
    e.preventDefault();
    const [x, y, c] = [probe.x, probe.y, probe.channel].map((t) => Number(t) - 1);
    if (![x, y, c].every((v) => v !== undefined && Number.isInteger(v) && v >= 0)) { setErr("The pixel and the channel are whole numbers, counting from 1."); return; }
    void run(async () => setCoord(await getSubsetCoord(sub.id, x!, y!, c!)));
  };

  return (
    <div className={embedded ? s.embedded : s.page}>
      {embedded ? (
        <p className={s.line} data-testid="subset-line">{summaryLine(sub.sourceName, sub.version, sub.definition)}{sub.stale ? " · stale" : ""}</p>
      ) : (
        <header className={s.head}>
          <h2 className={s.title}>{sub.name}</h2>
          <Pill tone="accent">virtual subset</Pill>
          {sub.stale ? <Pill tone="warn">stale</Pill> : null}
        </header>
      )}
      <p className={s.note}>A window onto <button type="button" className={s.link} onClick={() => onOpenDataset(sub.sourceId)}>{sub.sourceName}</button>. It is not a file: nothing is copied until you write it as one.</p>

      {sub.stale ? (
        <form className={s.alert} role="alert" onSubmit={repin}>
          <p style={{ margin: 0 }}>{sub.staleReason}</p>
          <Input label="Why this window still fits the changed file" value={reason} onChange={(e) => setReason(e.target.value)} maxLength={500} autoComplete="off" />
          <div><Button type="submit" variant="primary" disabled={busy || !reason.trim()}>Re-pin to the current file</Button></div>
        </form>
      ) : null}

      <dl className={s.facts}>
        <dt>Window</dt>
        <dd>{describe(sub.definition)}</dd>
        <dt>Reason</dt>
        <dd>{sub.reason}</dd>
        <dt>Version</dt>
        <dd>{sub.version} ({sub.kind}), {when(sub.editedAt)}</dd>
        <dt>Pinned to</dt>
        <dd className="mono">{sub.pinned.identity ? `${sub.pinned.identity.slice(0, 22)}…` : "no recorded content identity"}, {bytes(sub.pinned.size)}, {when(sub.pinned.modifiedAt)}</dd>
      </dl>

      {viewErr ? <p role="alert" className={s.err}>{viewErr}</p> : null}
      {view && win ? (
        <section aria-label="What the window resolved to">
          <h3 className={s.h3}>What it resolved to</h3>
          <dl className={s.facts}>
            <dt>Size</dt>
            <dd>{win.width} by {win.height} pixels, {win.channels} channel{win.channels === 1 ? "" : "s"}</dd>
            <dt>Source pixels</dt>
            <dd className="mono">x {(win.x0 ?? 0) + 1} to {(win.x1 ?? 0) + 1}, y {(win.y0 ?? 0) + 1} to {(win.y1 ?? 0) + 1}{(win.step ?? 1) > 1 ? `, every ${win.step}` : ""} of {win.source?.width} by {win.source?.height}</dd>
            <dt>Source channels</dt>
            <dd className="mono">{(win.c0 ?? 0) + 1} to {(win.c1 ?? 0) + 1} of {win.source?.channels}</dd>
            {win.world ? <><dt>Spectral rule</dt><dd>{win.channel_rule}</dd></> : null}
          </dl>
          {view.spectral && view.spectral.resolved === false ? <p className={s.note}>The spectral axis is not fully declared in the header, so no values are shown for it.</p> : null}
          {embedded ? null : <form className={s.probe} onSubmit={read} aria-label="Coordinates of a pixel of the subset">
            <Input label="x" value={probe.x} onChange={(e) => setProbe({ ...probe, x: e.target.value })} inputMode="numeric" />
            <Input label="y" value={probe.y} onChange={(e) => setProbe({ ...probe, y: e.target.value })} inputMode="numeric" />
            <Input label="channel" value={probe.channel} onChange={(e) => setProbe({ ...probe, channel: e.target.value })} inputMode="numeric" />
            <Button type="submit" disabled={busy}>Read coordinates</Button>
          </form>}
          {coord && !embedded ? (
            <p className={s.readout} role="status">
              Pixel ({coord.x + 1}, {coord.y + 1}), channel {coord.channel + 1} of the subset: {coord.sky ? `${num(coord.sky.lon)}°, ${num(coord.sky.lat)}° on the sky` : "no sky position (the header has no complete celestial WCS)"}
              {coord.spectral ? `; spectral ${num(coord.spectral.value)} ${view.spectral?.unit ?? ""}` : ""}.
            </p>
          ) : null}
          <p className={s.note}>The coordinates are the source file's own, read through the window: pixel 1 of the subset is the source pixel {(win.x0 ?? 0) + 1}.{embedded ? " Masks, saved regions, overlays, channel maps and analyses are the file's: write the subset as a file to use them." : ""}</p>
        </section>
      ) : null}

      {sourceMasks && sourceMasks.length > 0 && sourceMasks.some((m) => m.hostId !== 0) ? (
        <p className={s.note} data-testid="no-carry-on-host">The file is written on the source's host, where carrying masks is not built yet: the source's masks stay with it. Make them again on the new file there.</p>
      ) : sourceMasks && sourceMasks.length > 0 ? <MaskCarry masks={sourceMasks} state={carry} onChange={setCarry} op="subset" required={false} /> : null}
      <div className={s.actions}>
        <Button variant="primary" disabled={busy || sub.stale} onClick={() => void write()} title={sub.stale ? "Re-pin the subset first" : undefined}>
          {sub.materialised && !Object.values(carry).includes("carry") ? "Use the file already written" : "Write as a file"}
        </Button>
        <Button disabled={busy} onClick={() => setEditing(!editing)} aria-expanded={editing}>Edit the window</Button>
        {confirmDelete ? (
          <Button disabled={busy} onClick={() => void remove()}>Delete the subset (files written from it stay)</Button>
        ) : (
          <Button disabled={busy} onClick={() => setConfirmDelete(true)}>Delete</Button>
        )}
      </div>
      {made ? (
        <p role="status" className={s.note}>
          {made.reused ? "This version of the subset had already been written from the same file, and that file is unchanged: " : "Written as "}
          <button type="button" className={s.link} onClick={() => onOpenDataset(made.dataset.id)}>{made.dataset.name}</button>. It is an ordinary dataset; a script reads it unmodified.
        </p>
      ) : null}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}

      {editing ? (
        <SubsetForm datasetId={sub.sourceId} initial={fieldsOf(sub.name, sub.definition)} expectVersion={sub.version} submitLabel="Save as a new version"
          save={(input) => editSubset(sub.id, input)} onSaved={() => { setEditing(false); onChanged(); void load(); }} onCancel={() => setEditing(false)} />
      ) : null}

      <section aria-label="History">
        <h3 className={s.h3}>History</h3>
        <ol className={s.history}>
          {[...detail.versions].reverse().map((v) => {
            const files = detail.materialised.filter((m) => m.version === v.version);
            return (
              <li key={v.version}>
                <strong>Version {v.version}</strong> ({v.kind}), {when(v.at)}: {v.name}. {describe(v.definition)}. <span className={s.note}>{v.reason}</span>
                {files.map((m) => <span key={m.datasetId}> <button type="button" className={s.link} onClick={() => onOpenDataset(m.datasetId)}>written file {m.datasetId}</button></span>)}
              </li>
            );
          })}
        </ol>
      </section>
    </div>
  );
}
