import { useEffect, useState } from "react";
import { listBoards, listFigures } from "../api/boards";
import type { Board, Figure } from "../api/boards";
import type { Dataset } from "../api/catalog";
import { Button } from "../ui";
import s from "./AnalysisPanel.module.css";

/** Where a dataset is used: its figures and the boards that hold them, so files, runs and boards link both ways. */
export function UsedIn({ dataset, onOpenBoards }: { dataset: Dataset; onOpenBoards: () => void }) {
  const [figs, setFigs] = useState<Figure[]>([]);
  const [boards, setBoards] = useState<Board[]>([]);
  useEffect(() => {
    let live = true;
    Promise.all([listFigures(), listBoards()]).then(([f, b]) => {
      if (live) {
        setFigs(f.filter((x) => x.datasetId === dataset.id));
        setBoards(b);
      }
    }).catch(() => undefined);
    return () => {
      live = false;
    };
  }, [dataset.id]);
  if (figs.length === 0) return <p className={s.note}>No figure has been made from this file yet.</p>;
  return (
    <div className={s.wrap}>
      <ul className={s.note} style={{ margin: 0, paddingLeft: 16 }}>
        {figs.map((f) => {
          const on = boards.filter((b) => b.items.some((i) => i.figure.id === f.id)).map((b) => b.title);
          return <li key={f.id}>{f.title}{on.length ? `, on board ${on.join(", ")}` : ", not on a board"}</li>;
        })}
      </ul>
      <Button type="button" onClick={onOpenBoards}>Open boards</Button>
    </div>
  );
}
