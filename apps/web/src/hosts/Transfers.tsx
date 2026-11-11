import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { ArrowDownToLine, ArrowUpFromLine } from "lucide-react";
import { Button, Checkbox, Icon, Input, PathField, Pill, Select } from "../ui";
import { ApiError } from "../api/http";
import { cancelTransfer, listTransfers, queueTransfer, registerTransfer, removeTransfer, resolveTransfer, retryTransfer } from "../api/transfers";
import type { Transfer, TransferPolicy } from "../api/transfers";
import type { Host } from "../api/hosts";
import { bytes } from "../catalog/format";
import { directionText, isLive, isRunning, progressText, stateText, verificationText } from "./transferText";
import s from "./Transfers.module.css";

/** How often the list is read while something is moving, and when nothing is. */
export const ACTIVE_MS = 3000;
export const IDLE_MS = 20000;

const msg = (e: unknown, fallback: string) => (e instanceof Error ? e.message : fallback);

const TONE = (t: Transfer): "neutral" | "ok" | "warn" | "err" | "accent" => {
  if (t.state === "done") return t.verification.status === "verified" ? "ok" : "warn";
  if (t.state === "failed") return "err";
  if (t.state === "conflict" || t.state === "waiting" || t.state === "cancelled" || t.state === "skipped") return "warn";
  return "accent";
};

/**
 * The transfer queue between this computer and a host (ADR 0051): one transfer per host at a time, resumed after a lost
 * connection or a restart from the partial file, verified by hash on both ends. Nothing here overwrites a file unless Replace is
 * confirmed, and nothing is called verified because rsync finished.
 */
export function Transfers({ hosts, onRegistered }: { hosts: Host[]; onRegistered?: () => Promise<void> }) {
  const [items, setItems] = useState<Transfer[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [rowErr, setRowErr] = useState<Record<number, string>>({});
  const [confirm, setConfirm] = useState<number | null>(null);
  const [formOpen, setFormOpen] = useState(false);
  const [absent, setAbsent] = useState(false); // a server started without the queue answers 404: nothing to show
  const alive = useRef(true);

  const load = useCallback(async () => {
    try {
      const l = await listTransfers();
      if (alive.current) {
        setItems(l);
        setErr(null);
      }
    } catch (e) {
      if (!alive.current) return;
      if (e instanceof ApiError && e.status === 404) setAbsent(true);
      else setErr(msg(e, "Could not read the transfer list."));
    }
  }, []);

  const anyLive = (items ?? []).some(isLive);
  useEffect(() => {
    alive.current = true;
    void load();
    const t = setInterval(() => void load(), anyLive ? ACTIVE_MS : IDLE_MS);
    return () => {
      alive.current = false;
      clearInterval(t);
    };
  }, [load, anyLive]);

  async function act(id: number, f: () => Promise<unknown>) {
    try {
      await f();
      setRowErr((r) => { const { [id]: _gone, ...rest } = r; return rest; });
    } catch (e) {
      setRowErr((r) => ({ ...r, [id]: msg(e, "That did not work.") }));
    }
    setConfirm(null);
    await load();
  }

  if (absent) return null;
  const hostName = (id: number) => hosts.find((h) => h.id === id)?.name ?? `host ${id}`;

  return (
    <section className={s.root} aria-label="Transfers">
      <div className={s.head}>
        <h3 className={s.h}>Transfers</h3>
        <Button type="button" aria-expanded={formOpen} onClick={() => setFormOpen((o) => !o)}>{formOpen ? "Hide the form" : "New transfer…"}</Button>
      </div>
      {formOpen ? <NewTransfer hosts={hosts} onQueued={async () => { setFormOpen(false); await load(); }} /> : null}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      {items !== null && items.length === 0 ? <p className={s.note}>No transfers yet. A transfer copies a file or folder between this computer and a host, and carries on by itself after the connection drops or the app restarts.</p> : null}
      <ul className={s.list} aria-label="Transfer queue">
        {(items ?? []).map((t) => {
          const total = t.totalBytes;
          const vtext = verificationText(t);
          return (
            <li key={t.id} className={s.item} aria-label={`${directionText(t)} ${t.finalName || t.name}`}>
              <div className={s.row}>
                <Icon icon={t.direction === "download" ? ArrowDownToLine : ArrowUpFromLine} />
                <strong className={s.name}>{t.finalName || t.name}</strong>
                <span className={s.where}>{t.direction === "download" ? "from" : "to"} {hostName(t.hostId)}</span>
                <Pill tone={TONE(t)}>{stateText(t)}</Pill>
              </div>
              {isRunning(t) || (t.state === "waiting" && t.doneBytes > 0) ? (
                <>
                  {total > 0 ? <progress className={s.bar} max={total} value={Math.min(t.doneBytes, total)} aria-label={`${t.name}: ${Math.round((t.doneBytes / total) * 100)} %`} /> : null}
                  <p className={s.note}>{progressText(t)}</p>
                </>
              ) : null}
              {t.state === "queued" && t.bwlimitKBps > 0 ? <p className={s.note}>Limited to {bytes(t.bwlimitKBps * 1024)}/s.</p> : null}
              {t.detail ? <p className={s.note}>{t.detail}</p> : null}
              {t.warning ? <p className={s.warn} role="note">{t.warning}</p> : null}
              {vtext ? <p className={t.verification.status === "verified" ? s.note : s.warn}>{vtext}</p> : null}
              <details className={s.more}>
                <summary>Paths{t.verification.status === "verified" && t.verification.method === "sha256" ? " and SHA-256" : ""}</summary>
                <p className={s.path}>From <span className="mono">{t.sourcePath}</span></p>
                <p className={s.path}>To <span className="mono">{t.destPath}</span></p>
                {t.verification.status === "verified" && t.verification.method === "sha256" ? <p className={s.hash}>SHA-256 <span className="mono">{t.verification.destSha256}</span></p> : null}
              </details>
              {rowErr[t.id] ? <p role="alert" className={s.err}>{rowErr[t.id]}</p> : null}
              <div className={s.actions}>
                {t.state === "conflict" ? (
                  confirm === t.id ? (
                    <>
                      <span className={s.note}>Replace overwrites <span className="mono">{t.destPath}</span> and cannot be undone.</span>
                      <Button type="button" onClick={() => void act(t.id, () => resolveTransfer(t.id, "replace"))}>Replace it</Button>
                      <Button type="button" onClick={() => setConfirm(null)}>Keep it</Button>
                    </>
                  ) : (
                    <>
                      <Button type="button" onClick={() => void act(t.id, () => resolveTransfer(t.id, "skip"))}>Skip</Button>
                      <Button type="button" onClick={() => void act(t.id, () => resolveTransfer(t.id, "keep_both"))}>Keep both (new name)</Button>
                      <Button type="button" onClick={() => setConfirm(t.id)}>Replace…</Button>
                    </>
                  )
                ) : null}
                {isLive(t) ? <Button type="button" onClick={() => void act(t.id, () => cancelTransfer(t.id))}>Cancel</Button> : null}
                {isRunning(t) && t.direction === "download" && t.kind === "file" ? (
                  <Button type="button" onClick={() => void act(t.id, () => cancelTransfer(t.id, true))}>Cancel and delete the partial file</Button>
                ) : null}
                {t.state === "failed" || t.state === "cancelled" ? <Button type="button" onClick={() => void act(t.id, () => retryTransfer(t.id))}>Try again</Button> : null}
                {t.state === "done" && t.verification.status === "unverified" ? (
                  <Button type="button" onClick={() => void act(t.id, () => retryTransfer(t.id))}>Copy again and compare contents</Button>
                ) : null}
                {t.state === "done" && t.direction === "download" && t.kind === "file" && t.verification.status === "verified" ? (
                  t.datasetId != null ? (
                    <span className={s.note}>Registered as a dataset.</span>
                  ) : (
                    <Button type="button" onClick={() => void act(t.id, async () => { await registerTransfer(t.id); await onRegistered?.(); })}>Register as a dataset</Button>
                  )
                ) : null}
                {!isLive(t) ? <Button type="button" onClick={() => void act(t.id, () => removeTransfer(t.id))}>Remove from the list</Button> : null}
              </div>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

const POLICIES: { value: TransferPolicy; label: string }[] = [
  { value: "ask", label: "Ask me if a different file is there" },
  { value: "skip", label: "Skip if a file with that name is there" },
  { value: "keep_both", label: "Keep both (add a number to the name)" },
  { value: "replace", label: "Replace what is there" },
];

function NewTransfer({ hosts, onQueued }: { hosts: Host[]; onQueued: () => Promise<void> }) {
  const [direction, setDirection] = useState<"download" | "upload">("download");
  const [hostId, setHostId] = useState<number>(hosts[0]?.id ?? 0);
  const [source, setSource] = useState("");
  const [dest, setDest] = useState("");
  const [policy, setPolicy] = useState<TransferPolicy>("ask");
  const [limit, setLimit] = useState("");
  const [compress, setCompress] = useState(true);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const kb = limit.trim() === "" ? 0 : Number(limit);
  const badLimit = !Number.isInteger(kb) || kb < 0 || kb > 10_000_000;

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await queueTransfer({ direction, hostId, sourcePath: source.trim(), destDir: dest.trim(), policy, bwlimitKBps: kb, compress });
      setErr(null);
      setSource("");
      await onQueued();
    } catch (x) {
      setErr(msg(x, "Could not queue the transfer."));
    }
    setBusy(false);
  }

  if (hosts.length === 0) return <p className={s.note}>Add a host first.</p>;
  const down = direction === "download";
  return (
    <form className={s.form} onSubmit={submit} aria-label="New transfer">
      <Select label="Direction" value={direction} onChange={(e) => { setDirection(e.target.value as "download" | "upload"); setSource(""); setDest(""); }}>
        <option value="download">Download: from the host to this computer</option>
        <option value="upload">Upload: from this computer to the host</option>
      </Select>
      <Select label="Host" value={hostId} onChange={(e) => setHostId(Number(e.target.value))}>
        {hosts.map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}
      </Select>
      <PathField label={down ? "File or folder on the host" : "File or folder on this computer"} kind="any" hostId={down ? hostId : undefined} value={source} onChange={setSource} required />
      <PathField label={down ? "Into this folder on this computer" : "Into this folder on the host"} kind="dir" hostId={down ? undefined : hostId} value={dest} onChange={setDest} required />
      <Select label="If the name is already there" value={policy} onChange={(e) => setPolicy(e.target.value as TransferPolicy)}>
        {POLICIES.map((p) => <option key={p.value} value={p.value}>{p.label}</option>)}
      </Select>
      <Input label="Speed limit in KiB/s (empty: none)" inputMode="numeric" value={limit} onChange={(e) => setLimit(e.target.value)} placeholder="for example 2000 on a hotspot" />
      <Checkbox label="Compress while transferring (little gain for FITS cubes, costs CPU)" checked={compress} onChange={(e) => setCompress(e.target.checked)} />
      <p className={s.note}>
        Uses rsync over your own ssh. If the connection drops it is retried with growing waits and resumes from the partial file. The copy is checked by hash on both machines afterwards{down ? "" : "; an upload writes on the host, so it needs the destination to be inside that host's source folders"}.
      </p>
      {replaceNote(policy)}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      <Button type="submit" disabled={busy || !source.trim() || !dest.trim() || badLimit}>Queue transfer</Button>
    </form>
  );
}

function replaceNote(policy: TransferPolicy) {
  return policy === "replace" ? <p className={s.warn} role="note">Replace overwrites a file or folder with the same name without asking again. Choose Ask if you are not sure.</p> : null;
}
