import { useEffect, useRef, useState } from "react";
import { FileImage, Redo2 } from "lucide-react";
import { Button } from "../ui";
import { getRedrawCheck } from "../api/boards";
import type { Figure, RedrawCheck } from "../api/boards";
import { downloadName, redrawFigure, samePixels, savedPicture } from "../viewer/redraw";
import type { Redrawn } from "../viewer/redraw";
import s from "./FigureRedraw.module.css";

/** Triggers a browser download of a blob. */
export function saveBlobAs(blob: Blob, name: string): void {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
}

const MESSAGES = {
  same: "Drawn again from unchanged data: the picture has the same pixels as the saved one.",
  sameUnproven: "The picture has the same pixels as the saved one, although the data was not proven unchanged: a change, if there is one, is not in what this picture shows.",
  sameDeclared: "The picture has the same pixels as the saved one, drawn under the declarations in force now. A revision saved from it records those declarations, not the ones the original was drawn under.",
  differs: "The picture drawn now is not the same as the saved one. The saved picture is kept; look at both before you save this as a new revision.",
};

/**
 * "Draw again" and "Export SVG" for a stored figure (ADR 0042, section 5). It first asks the server what the figure's files look
 * like now and, if anything changed or cannot be proven unchanged, says so and waits for the owner. A redrawing replaces
 * nothing: it becomes a new, linked figure only on "Save as a new revision".
 */
export function FigureRedraw({ figure, onSaved, addTo, only }: {
  figure: Figure;
  onSaved?: (f: Figure) => void;
  /** Puts a saved revision on the board the figure is on (the board card passes it). */
  addTo?: (figureId: number) => Promise<void>;
  /** "svg": only the export, for a figure that was just saved (drawing it again would show nothing new). */
  only?: "svg";
}) {
  const schema = (figure.scene as { schema?: string } | undefined)?.schema;
  const drawable = schema === "scene/1" || schema === "channel-grid/1";
  const [busy, setBusy] = useState<null | "checking" | "drawing" | "saving">(null);
  const [check, setCheck] = useState<RedrawCheck | null>(null);
  const [intent, setIntent] = useState<"draw" | "svg">("draw");
  const [err, setErr] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [out, setOut] = useState<{ r: Redrawn; url: string; same: boolean; proven: boolean; declared: boolean } | null>(null);
  const [saved, setSaved] = useState<Figure | null>(null);
  const [onBoard, setOnBoard] = useState(false);
  const live = useRef(true);
  useEffect(() => () => { live.current = false; }, []);
  useEffect(() => () => { if (out) URL.revokeObjectURL(out.url); }, [out]);

  function reset() {
    setCheck(null);
    setErr(null);
    setNote(null);
    setOut(null);
    setSaved(null);
    setOnBoard(false);
  }

  async function draw(refresh: boolean, why: "draw" | "svg") {
    // draw(true) is only called after the owner agreed to data that is not proven unchanged, or to a changed declaration
    const proven = !check || check.sources.every((x) => x.status === "unchanged");
    const declared = !!check?.sources.some((x) => x.declaration === "changed" || x.declaration === "removed");
    setBusy("drawing");
    setErr(null);
    setCheck(null);
    try {
      const r = await redrawFigure(figure, { refresh });
      if (why === "svg") {
        if (!r.svg) throw new Error("A channel-map grid is panels of raster only, so it has no vector layers to export.");
        saveBlobAs(new Blob([r.svg()], { type: "image/svg+xml" }), downloadName(figure.title, "svg"));
        setNote(r.notes.length ? `SVG exported. ${r.notes.join(" ")}` : "SVG exported.");
        return;
      }
      const same = await samePixels(r.blob, await savedPicture(figure)).catch(() => false);
      if (live.current) setOut({ r, url: URL.createObjectURL(r.blob), same, proven, declared });
    } catch (e) {
      setErr(e instanceof Error ? e.message : "The figure could not be drawn again.");
    } finally {
      if (live.current) setBusy(null);
    }
  }

  async function start(why: "draw" | "svg") {
    reset();
    setIntent(why);
    setBusy("checking");
    try {
      const c = await getRedrawCheck(figure.id);
      if (c.state === "none") {
        setNote(c.prompt ?? "This figure cannot be drawn again.");
        setBusy(null);
        return;
      }
      if (c.state !== "ready") {
        setCheck(c);
        setBusy(null);
        return;
      }
      await draw(false, why);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not check the figure's data.");
      setBusy(null);
    }
  }

  async function save() {
    if (!out) return;
    setBusy("saving");
    try {
      const f = await out.r.save();
      setSaved(f);
      onSaved?.(f);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not save the new revision.");
    } finally {
      setBusy(null);
    }
  }

  if (!drawable) {
    return <p className={s.note}>This figure keeps no description of how it was drawn, so it is shown as the PNG that was saved. It cannot be drawn again or exported as SVG.</p>;
  }
  const idle = busy === null;
  return (
    <div className={s.wrap}>
      <div className={s.row}>
        {only === "svg" ? null : <Button icon={Redo2} disabled={!idle} onClick={() => void start("draw")}>{busy === "checking" || busy === "drawing" ? "Working…" : "Draw again"}</Button>}
        {schema === "scene/1" ? <Button icon={FileImage} disabled={!idle} onClick={() => void start("svg")}>Export SVG</Button> : null}
      </div>
      {check ? (
        <div className={s.box} role="region" aria-label="What changed since this figure was drawn">
          <ul className={s.list}>{check.sources.map((x) => <li key={`${x.role}-${x.datasetId}`} data-status={x.status} data-declaration={x.declaration}>{x.text}{x.declarationText && x.declaration !== "unchanged" ? ` ${x.declarationText.charAt(0).toUpperCase()}${x.declarationText.slice(1)}${x.declarationText.endsWith(".") ? "" : "."}` : ""}</li>)}</ul>
          <p className={s.prompt}>{check.prompt}</p>
          <div className={s.row}>
            {check.state === "ask" ? <Button variant="primary" onClick={() => void draw(true, intent)}>{intent === "svg" ? "Export anyway" : "Draw anyway"}</Button> : null}
            <Button variant="ghost" onClick={reset}>{check.state === "ask" ? "Cancel" : "Close"}</Button>
          </div>
        </div>
      ) : null}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      {note ? <p role="status" className={s.note}>{note}</p> : null}
      {out ? (
        <div className={s.box} role="region" aria-label="The figure drawn again">
          <img className={s.img} src={out.url} alt={`${figure.title}, drawn again`} />
          <p role="status" className={s.note}>{out.same ? (out.proven ? (out.declared ? MESSAGES.sameDeclared : MESSAGES.same) : MESSAGES.sameUnproven) : MESSAGES.differs}</p>
          {out.r.notes.length ? <ul className={s.list}>{out.r.notes.map((n) => <li key={n}>{n}</li>)}</ul> : null}
          <p className={s.note}>The original picture is kept. Saving makes a new figure, linked to this one; boards and shared reviews keep the figure they hold.</p>
          <div className={s.row}>
            <Button variant="primary" disabled={!idle || saved !== null} onClick={() => void save()}>{busy === "saving" ? "Saving…" : "Save as a new revision"}</Button>
            {out.r.svg ? <Button icon={FileImage} disabled={!idle} onClick={() => saveBlobAs(new Blob([out.r.svg!()], { type: "image/svg+xml" }), downloadName(figure.title, "svg"))}>Download SVG</Button> : null}
            <Button variant="ghost" onClick={reset}>Close</Button>
          </div>
          {saved ? (
            <>
              <p role="status" className={s.note}>Saved as a new revision: “{saved.title}”, linked to this figure.{addTo && !onBoard ? " It is not on a board yet." : ""}{onBoard ? " Added to this board, after the original." : ""}</p>
              {addTo && !onBoard ? (
                <div className={s.row}>
                  <Button onClick={() => void addTo(saved.id).then(() => setOnBoard(true)).catch((e: unknown) => setErr(e instanceof Error ? e.message : "Could not add it to the board."))}>Add to this board</Button>
                </div>
              ) : null}
            </>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
