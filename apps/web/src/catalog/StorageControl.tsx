import { useState } from "react";
import { copyDataset } from "../api/catalog";
import type { Dataset } from "../api/catalog";
import { Button } from "../ui";
import { bytes } from "./format";
import s from "./AnalysisPanel.module.css";

/** Reference (the default) versus a managed copy. A copy is byte for byte, read-only and identified by its hash. */
export function StorageControl({ dataset, onCopied }: { dataset: Dataset; onCopied: (d: Dataset) => void }) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [sha, setSha] = useState<string | null>(null);
  if (dataset.hostId !== 0) return <p className={s.note}>This file stays on its host and is read there. It is a reference, never copied here.</p>;
  async function go() {
    setBusy(true);
    try {
      const r = await copyDataset(dataset.id);
      setSha(r.sha256);
      onCopied(r.dataset);
      setErr(null);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not copy the file.");
    }
    setBusy(false);
  }
  return (
    <div className={s.wrap}>
      <p className={s.note}>Registered as a <strong>reference</strong> to the original, which is the source of truth. A path does not prove the bytes stay the same; if the file changes, Vesqira says so and asks you to register it again.</p>
      <Button type="button" disabled={busy} onClick={go}>Make a managed copy ({bytes(dataset.sizeBytes)})</Button>
      <p className={s.note}>The copy is byte for byte, read-only, and identified by its SHA-256. The original is not touched. It uses that much disk space.</p>
      {sha ? <p className={s.note} role="status">Copied. SHA-256 <span className="mono">{sha.slice(0, 16)}…</span></p> : null}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
    </div>
  );
}
