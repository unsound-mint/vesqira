import { useState } from "react";
import { Button, Checkbox, LinkButton } from "../ui";
import { Modal, ModalActions } from "../ui/Modal";
import { ApiError, requestRaw } from "../api/http";
import { previewSupport, supportBundleUrl } from "../api/support";
import type { SupportPreview } from "../api/support";
import s from "./SupportDialog.module.css";

const size = (n: number) => (n < 1024 ? `${n} B` : n < 1048576 ? `${(n / 1024).toFixed(1)} KB` : `${(n / 1048576).toFixed(1)} MB`);

/** Owner-only: builds a support bundle, lists every file in it with its text, and saves it only when asked. */
export default function SupportDialog({ onClose }: { onClose: () => void }) {
  const [names, setNames] = useState(false);
  const [preview, setPreview] = useState<SupportPreview | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // A link would save the 404 page of an expired preview as the zip, so the file is fetched first and an expiry is said in words.
  async function save(id: string) {
    setErr(null);
    try {
      const blob = await requestRaw(supportBundleUrl(id)).then((r) => r.blob());
      const a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = "vesqira-support.zip";
      a.click();
      setTimeout(() => URL.revokeObjectURL(a.href), 1000);
    } catch (e) {
      setErr(e instanceof ApiError && e.status === 404 ? "This preview expired; preview again" : e instanceof Error ? e.message : "The bundle could not be saved.");
      setPreview(null);
    }
  }

  async function run() {
    setBusy(true);
    setErr(null);
    setPreview(null);
    try {
      setPreview(await previewSupport(names));
    } catch (e) {
      setErr(e instanceof Error ? e.message : "The preview could not be made.");
    }
    setBusy(false);
  }

  return (
    <Modal label="Create a support bundle" onClose={onClose} size="lg">
      <h2 className={s.h}>Create a support bundle</h2>
      <p className={s.note}>A zip of diagnostics you can send when asking for help. You see every file before anything is saved.</p>
      <Checkbox
        label="Include dataset names (off: dataset names and file names are removed)"
        checked={names}
        onChange={(e) => { setNames(e.target.checked); setPreview(null); setErr(null); }}
      />
      <div className={s.row}>
        <Button variant="primary" disabled={busy} onClick={() => void run()}>{busy ? "Preparing…" : "Preview files"}</Button>
      </div>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      {preview ? (
        <>
          <p className={s.note}>Nothing is saved yet. These {preview.files.length} files ({size(preview.bytes)}) are exactly what the zip will hold.</p>
          <ul className={s.files}>
            {preview.files.map((f) => (
              <li key={f.name}>
                <details>
                  <summary><span className="mono">{f.name}</span> <span className={s.size}>{size(f.bytes)}</span></summary>
                  <pre className={s.text}>{f.text}</pre>
                </details>
              </li>
            ))}
          </ul>
        </>
      ) : null}
      <ModalActions>
        <Button onClick={onClose}>Close</Button>
        {preview ? <LinkButton variant="primary" href={supportBundleUrl(preview.id)} download="vesqira-support.zip" onClick={(e) => { e.preventDefault(); void save(preview.id); }}>Save bundle (.zip)</LinkButton> : null}
      </ModalActions>
    </Modal>
  );
}

