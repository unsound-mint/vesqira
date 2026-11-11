import { useEffect, useState } from "react";
import { Modal } from "../ui/Modal";
import { Button, Pill } from "../ui";
import { fromEditorText, lineEnding, openTextFile, saveTextFile, toEditorText } from "../api/editor";
import type { TextFile } from "../api/editor";
import { bytes } from "../catalog/format";
import { CodeSurface, languageFor } from "./CodeSurface";
import s from "./Editor.module.css";

type Opened = { file: TextFile; text: string; saved: string; eol: "\n" | "\r\n"; mixed: boolean };

const open = (file: TextFile): Opened => {
  const { eol, mixed } = lineEnding(file.content);
  const text = toEditorText(file.content);
  return { file, text, saved: text, eol, mixed };
};

/**
 * Edit one text file on this computer (ADR 0029). A save names the file as it was opened; when it changed on disk the
 * server writes nothing and answers with what is there now, and this screen shows both versions and asks which to keep.
 * Loaded on demand (it brings Monaco).
 */
export default function EditorScreen({ path, onClose }: { path: string; onClose: () => void }) {
  const [doc, setDoc] = useState<Opened | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [conflict, setConflict] = useState<{ current: TextFile; message: string } | null>(null);
  const [confirmOverwrite, setConfirmOverwrite] = useState(false);
  const [confirmClose, setConfirmClose] = useState(false);
  const name = path.slice(path.lastIndexOf("/") + 1);

  useEffect(() => {
    let live = true;
    openTextFile(path).then(
      (f) => live && setDoc(open(f)),
      (e: unknown) => live && setError(e instanceof Error ? e.message : "Could not open the file."),
    );
    return () => {
      live = false;
    };
  }, [path]);

  const dirty = doc !== null && doc.text !== doc.saved;

  async function save(base: TextFile) {
    if (!doc) return;
    setSaving(true);
    setError(null);
    try {
      const r = await saveTextFile(doc.file.path, fromEditorText(doc.text, doc.eol), base);
      if (r.status === "saved") {
        setDoc({ ...doc, file: r.file, saved: doc.text });
        setConflict(null);
      } else {
        setConflict({ current: r.current, message: r.message });
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not save the file.");
    } finally {
      setSaving(false);
      setConfirmOverwrite(false);
    }
  }

  function close() {
    if (dirty && !confirmClose) setConfirmClose(true);
    else onClose();
  }

  return (
    <Modal label={`Edit ${name}`} fullscreen onClose={close} className={s.dialog}>
      <div className={s.screen}>
        <header className={s.bar}>
          <div className={s.title}>
            <p className={s.name}>{name}</p>
            <p className={s.where} title={doc?.file.path ?? path}>{doc?.file.path ?? path}</p>
          </div>
          {doc ? <span className={s.meta}>{bytes(doc.file.size)}</span> : null}
          {dirty ? <Pill tone="warn">unsaved</Pill> : doc ? <Pill tone="neutral">saved</Pill> : null}
          <Button variant="primary" disabled={!doc || !dirty || saving || conflict !== null} onClick={() => doc && void save(doc.file)}>{saving ? "Saving…" : "Save"}</Button>
          <Button onClick={close}>Close</Button>
        </header>
        {confirmClose ? (
          <div role="alert" className={s.notice}>
            <p>This file has changes that are not saved.</p>
            <Button onClick={onClose}>Discard changes</Button>
            <Button onClick={() => setConfirmClose(false)}>Keep editing</Button>
          </div>
        ) : null}
        {error ? <p role="alert" className={s.error}>{error}</p> : null}
        {doc?.mixed ? <p role="status" className={s.notice}>This file mixes line endings. Saving will give every line the {doc.eol === "\r\n" ? "Windows (CRLF)" : "Unix (LF)"} ending most of them have.</p> : null}
        {conflict && doc ? (
          <section className={s.conflict} aria-label="Changed on disk">
            <h2 className={s.conflictTitle}>Changed on disk</h2>
            <p>{conflict.message}. Nothing was written. Keep your version (it replaces the one on disk) or take the one on disk (your edits are dropped).</p>
            <div className={s.versions}>
              <div>
                <h3 className={s.versionTitle}>Your version</h3>
                <textarea className={s.readonly} readOnly aria-label="Your version" value={doc.text} spellCheck={false} />
              </div>
              <div>
                <h3 className={s.versionTitle}>On disk now · {bytes(conflict.current.size)}</h3>
                <textarea className={s.readonly} readOnly aria-label="On disk now" value={toEditorText(conflict.current.content)} spellCheck={false} />
              </div>
            </div>
            <div className={s.actions}>
              <Button onClick={() => { setDoc(open(conflict.current)); setConflict(null); setConfirmOverwrite(false); }}>Take theirs</Button>
              {confirmOverwrite ? (
                <>
                  <Button variant="primary" disabled={saving} onClick={() => void save(conflict.current)}>Overwrite the file on disk</Button>
                  <Button onClick={() => setConfirmOverwrite(false)}>Cancel</Button>
                </>
              ) : (
                <Button onClick={() => setConfirmOverwrite(true)}>Keep mine…</Button>
              )}
              <Button variant="ghost" onClick={() => { setConflict(null); setConfirmOverwrite(false); }}>Back to editing</Button>
            </div>
          </section>
        ) : doc ? (
          <div className={s.body}>
            <CodeSurface value={doc.text} language={languageFor(name)} label={`Contents of ${name}`} onChange={(text) => setDoc((d) => (d ? { ...d, text } : d))} />
          </div>
        ) : error ? null : (
          <p role="status" className={s.loading}>Opening…</p>
        )}
      </div>
    </Modal>
  );
}
