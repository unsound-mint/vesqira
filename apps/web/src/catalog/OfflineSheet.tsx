import { useEffect, useMemo, useRef, useState } from "react";
import { cancelPrepare, getPrepare, planOffline, startPrepare } from "../api/offline";
import type { OfflineItem, OfflinePlan, OfflinePrepareState, OfflineRequest, OfflineStatus } from "../api/offline";
import { getView } from "../api/planes";
import { Button, Input, Pill, Select } from "../ui";
import { Modal, ModalActions } from "../ui/Modal";
import { bytes } from "./format";
import s from "./OfflineSheet.module.css";

/** Said in words, never by colour alone. */
export const STATUS_WORDS: Record<OfflineStatus, string> = { offline: "Works offline", partial: "Works partially", remote: "Needs the remote" };
const TONE: Record<OfflineStatus, "ok" | "warn" | "neutral"> = { offline: "ok", partial: "warn", remote: "neutral" };

const MIB = 1 << 20;
export const PLAN_DELAY_MS = 350;
export const POLL_MS = 1000;

/** The three-way list of a preparation: each item, its status in words, and why. */
export function OfflineItems({ items, label }: { items: OfflineItem[]; label: string }) {
  if (items.length === 0) return null;
  return (
    <ul className={s.items} aria-label={label}>
      {items.map((it) => (
        <li key={`${it.kind}:${it.id}`} className={s.item}>
          <div className={s.itemHead}>
            <Pill tone={TONE[it.status]}>{STATUS_WORDS[it.status]}</Pill>
            <span className={s.itemName}>{it.name}</span>
            <span className={s.kind}>{it.kind}</span>
          </div>
          <p className={s.reason}>{it.reason}</p>
        </li>
      ))}
    </ul>
  );
}

const wholeNumber = (t: string): number | null => (/^\d{1,5}$/.test(t.trim()) ? Number(t) : null);

/**
 * "Prepare for offline" (ADR 0059): choose a channel range, the detail and a byte budget, see what each item would give
 * (works offline, works partially, needs the remote) and the estimate against the budget, then copy. Only reduced or single planes,
 * headers, region spectra and board pictures are kept; a cube is never copied, and the sheet says so.
 */
export default function OfflineSheet({ title, datasetId, boardId, onClose, onChanged }: { title: string; datasetId?: number; boardId?: number; onClose: () => void; onChanged: () => void }) {
  const [from, setFrom] = useState("0");
  const [to, setTo] = useState("9");
  const [detail, setDetail] = useState<"preview" | "full">("preview");
  const [budget, setBudget] = useState("256");
  const [plan, setPlan] = useState<OfflinePlan | null>(null);
  const [planErr, setPlanErr] = useState<string | null>(null);
  const [planning, setPlanning] = useState(false);
  const [run, setRun] = useState<OfflinePrepareState | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const channelsSet = useRef(false);

  // A single cube: start with the middle twenty channels, from the layout the host reports (nothing happens if it cannot).
  useEffect(() => {
    if (datasetId === undefined || channelsSet.current) return;
    const ac = new AbortController();
    getView(datasetId, ac.signal)
      .then((v) => {
        if (channelsSet.current || !v.channels) return;
        const mid = Math.floor(v.channels / 2);
        setFrom(String(Math.max(0, mid - 10)));
        setTo(String(Math.min(v.channels - 1, mid + 9)));
      })
      .catch(() => undefined);
    return () => ac.abort();
  }, [datasetId]);

  const request = useMemo((): OfflineRequest | null => {
    const f = wholeNumber(from), t = wholeNumber(to), b = wholeNumber(budget);
    if (f === null || t === null || b === null || t < f) return null;
    return { ...(boardId !== undefined ? { boardId } : { datasetIds: datasetId === undefined ? [] : [datasetId] }), channels: { from: f, to: t }, detail, budgetBytes: b * MIB };
  }, [from, to, budget, detail, boardId, datasetId]);

  const running = run?.state === "running";
  useEffect(() => {
    if (!request || running || run?.state === "done") return;
    setPlanning(true);
    const ac = new AbortController();
    const t = setTimeout(() => {
      planOffline(request, ac.signal)
        .then((p) => { setPlan(p); setPlanErr(null); })
        .catch((e: unknown) => { if (!ac.signal.aborted) { setPlan(null); setPlanErr(e instanceof Error ? e.message : "Could not work out what this would keep."); } })
        .finally(() => { if (!ac.signal.aborted) setPlanning(false); });
    }, PLAN_DELAY_MS);
    return () => { clearTimeout(t); ac.abort(); };
  }, [request, running, run?.state]);

  const changed = useRef(onChanged);
  changed.current = onChanged;
  // A preparation that is still running (started before this sheet opened) is picked up.
  useEffect(() => {
    const ac = new AbortController();
    getPrepare(ac.signal).then((st) => { if (st.state === "running") setRun(st); }).catch(() => undefined);
    return () => ac.abort();
  }, []);
  // Progress is followed once a second until it ends.
  useEffect(() => {
    if (!running) return;
    let live = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = () => {
      getPrepare().then((st) => {
        if (!live) return;
        setRun(st);
        if (st.state === "running") timer = setTimeout(poll, POLL_MS);
        else changed.current();
      }).catch(() => { if (live) timer = setTimeout(poll, POLL_MS * 3); });
    };
    timer = setTimeout(poll, POLL_MS);
    return () => { live = false; clearTimeout(timer); };
  }, [running]);

  async function prepare() {
    if (!request) return;
    setErr(null);
    try {
      setRun(await startPrepare(request));
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not start.");
    }
  }

  const free = plan ? Math.max(0, plan.storeCapBytes - plan.storeUsedBytes) : 0;
  const canStart = !!request && !!plan && plan.fits && plan.estimateBytes > 0 && !planning && !running;
  const shown = run && run.state !== "idle" ? run : null;
  const finished = shown !== null && shown.state !== "running";

  return (
    <Modal label={`Prepare ${title} for offline`} onClose={onClose} size="lg">
      <h2 className={s.title}>Prepare “{title}” for offline</h2>
      <p className={s.lede}>
        Do this while the host answers. Vesqira keeps only what you choose here: the view and header, single planes, the spectra of your saved regions and
        board pictures. It never copies the cube, and a copy is used only when the host cannot be reached, always marked as a copy.
      </p>
      {shown === null ? (
        <>
          <div className={s.form}>
            <Input label="First channel" inputMode="numeric" value={from} onChange={(e) => { channelsSet.current = true; setFrom(e.target.value); }} aria-invalid={wholeNumber(from) === null} />
            <Input label="Last channel" inputMode="numeric" value={to} onChange={(e) => { channelsSet.current = true; setTo(e.target.value); }} aria-invalid={wholeNumber(to) === null || (wholeNumber(from) !== null && wholeNumber(to)! < wholeNumber(from)!)} />
            <Select label="Plane detail" value={detail} onChange={(e) => setDetail(e.target.value as "preview" | "full")}>
              <option value="preview">Preview planes (reduced, as the viewer shows them first)</option>
              <option value="full">Full-resolution planes too</option>
            </Select>
            <Input label="Byte budget (MiB)" inputMode="numeric" value={budget} onChange={(e) => setBudget(e.target.value)} aria-invalid={wholeNumber(budget) === null} />
          </div>
          {!request ? <p className={s.note} role="status">Give whole numbers for the channels (first not after last) and the budget.</p> : null}
          <div className={s.estimate} role="status" aria-live="polite">
            {planning && !plan ? "Working out what this keeps…" : null}
            {plan ? (
              <>
                <strong>About {bytes(plan.estimateBytes)}</strong> of your {bytes(plan.budgetBytes)} budget{planning ? " (updating…)" : ""}. The offline store has {bytes(free)} free of {bytes(plan.storeCapBytes)}.
                {" "}{plan.fits ? "It fits." : "It does not fit."}
                {plan.notes.map((n) => <span key={n} className={s.warn}> {n}</span>)}
              </>
            ) : null}
          </div>
          {planErr ? <p role="alert" className={s.err}>{planErr}</p> : null}
          {plan ? <OfflineItems items={plan.items} label="What each item would give" /> : null}
        </>
      ) : (
        <div className={s.run}>
          <p role="status" aria-live="polite" className={s.progress}>
            {shown.state === "running" ? `Preparing ${Math.min(shown.done + 1, shown.total)} of ${shown.total}${shown.current ? ` · ${shown.current}` : ""} · ${bytes(shown.bytes)} kept so far` : null}
            {shown.state === "done" ? `Done. ${bytes(shown.bytes)} kept. These copies answer only when the host cannot be reached, and say so.` : null}
            {shown.state === "cancelled" ? `Stopped. ${bytes(shown.bytes)} kept; what was kept stays.` : null}
            {shown.state === "failed" ? `Stopped: ${shown.error}` : null}
          </p>
          <OfflineItems items={shown.items} label="What each item now gives" />
        </div>
      )}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      <ModalActions>
        {running ? <Button type="button" onClick={() => void cancelPrepare().then(setRun).catch(() => undefined)}>Stop preparing</Button> : null}
        {finished ? <Button type="button" onClick={() => { setRun(null); setPlan(null); }}>Prepare more</Button> : null}
        <Button type="button" onClick={onClose}>{finished ? "Close" : "Cancel"}</Button>
        {shown === null ? <Button type="button" variant="primary" disabled={!canStart} onClick={() => void prepare()}>Prepare</Button> : null}
      </ModalActions>
    </Modal>
  );
}
