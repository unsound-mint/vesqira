import { useEffect, useState } from "react";
import { Button, Checkbox, Input, Select } from "../ui";
import { compassNote, usableCompass } from "../ui/Compass";
import type { Dataset } from "../api/catalog";
import { createBoard, listBoards } from "../api/boards";
import { addFigure } from "../undo/boardCommands";
import type { Board, Figure } from "../api/boards";
import { defaultFigureTitle, saveViewFigure } from "./saveFigure";
import type { Viewer } from "./useViewer";
import { EM } from "../catalog/format";
import { FigureRedraw } from "../boards/FigureRedraw";
import s from "./FigurePanel.module.css";

/** Saves what is on screen as a figure that keeps its source, parameters and provenance (pinned evidence). */
export function FigurePanel({ v, dataset, datasets }: { v: Viewer; dataset: Dataset; datasets?: Dataset[] }) {
  const [kind, setKind] = useState<"plane" | "spectrum">("plane");
  const [title, setTitle] = useState("");
  const [caption, setCaption] = useState("");
  // north and east arrows: on whenever the viewer has them; the choice is kept in the figure's scene, so a redraw reproduces it
  const [compass, setCompass] = useState(true);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [saved, setSaved] = useState<Figure | null>(null);
  const [boards, setBoards] = useState<Board[]>([]);
  const [boardId, setBoardId] = useState("");
  const [newBoard, setNewBoard] = useState("");
  const [note, setNote] = useState<string | null>(null);
  const { view, plane } = v;

  useEffect(() => {
    listBoards().then(setBoards).catch(() => setBoards([]));
  }, [saved]);
  useEffect(() => {
    setSaved(null);
    setNote(null);
    setTitle("");
    setCompass(true);
  }, [dataset.id]);

  if (!view || !plane) return <p className={s.note}>{EM}</p>;
  const canSpectrum = Boolean(v.region && v.spectrum && !v.interp);
  const defaultTitle = defaultFigureTitle(v, dataset);

  async function save() {
    setBusy(true);
    setErr(null);
    setNote(null);
    try {
      setSaved(await saveViewFigure(v, dataset, { title, caption, kind, datasets, compass }));
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not save the figure.");
    } finally {
      setBusy(false);
    }
  }

  async function addIt() {
    if (!saved) return;
    try {
      let id = Number(boardId);
      if (!id) {
        if (!newBoard.trim()) return setNote("Choose a board or name a new one.");
        id = (await createBoard(newBoard.trim())).id;
        setNewBoard("");
      }
      await addFigure(id, saved.id);
      setNote("Added to the board.");
      setBoards(await listBoards());
      setBoardId(String(id));
    } catch (e) {
      setNote(e instanceof Error ? e.message : "Could not add it to the board.");
    }
  }

  return (
    <div className={s.wrap}>
      {canSpectrum ? (
        <Select label="Figure of" value={kind} onChange={(e) => setKind(e.target.value as "plane" | "spectrum")}>
          <option value="plane">The image</option>
          <option value="spectrum">The region spectrum</option>
        </Select>
      ) : null}
      <Input label="Title" placeholder={defaultTitle} value={title} onChange={(e) => setTitle(e.target.value)} />
      <div className={s.field}>
        <label htmlFor="fig-caption" className={s.label}>Caption (optional)</label>
        <textarea id="fig-caption" className={s.area} rows={3} value={caption} maxLength={2000} onChange={(e) => setCaption(e.target.value)} />
      </div>
      {kind !== "spectrum" ? (
        usableCompass(view.compass) ? (
          <Checkbox label="North and east arrows" checked={compass} onChange={(e) => setCompass(e.target.checked)} />
        ) : (
          <p role="note" className={s.note}>{compassNote(view.compass)}</p>
        )
      ) : null}
      <Button variant="primary" onClick={save} disabled={busy || (kind === "spectrum" && !canSpectrum) || (kind !== "spectrum" && Boolean(v.plane?.head.preview))}>{busy ? "Saving…" : "Save as figure"}</Button>
      {kind !== "spectrum" && v.plane?.head.preview ? <p className={s.note}>This is a reduced preview. The figure can be saved when the full plane has loaded.</p> : null}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      <p className={s.note}>The figure keeps the picture as drawn, the facts it came from (file name, size, time, declared header values, channel, region, display settings) and the caveats. It never contains the file path.</p>
      {saved ? (
        <div className={s.saved} role="status">
          <p className={s.note}>Saved: “{saved.title}”.</p>
          <Select label="Add to board" value={boardId} onChange={(e) => setBoardId(e.target.value)}>
            <option value="">New board…</option>
            {boards.map((b) => <option key={b.id} value={b.id}>{b.title}</option>)}
          </Select>
          {!boardId ? <Input label="New board name" value={newBoard} onChange={(e) => setNewBoard(e.target.value)} /> : null}
          <Button onClick={addIt}>Add to board</Button>
          {note ? <p className={s.note}>{note}</p> : null}
          <FigureRedraw figure={saved} only="svg" />
        </div>
      ) : null}
    </div>
  );
}
