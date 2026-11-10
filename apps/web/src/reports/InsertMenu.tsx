import { useEffect, useRef, useState } from "react";
import { Activity, Image, Plus } from "lucide-react";
import type { Figure } from "../api/boards";
import type { Run } from "../api/runs";
import { Icon } from "../ui";
import s from "./ReportsView.module.css";

/** Insert a pinned figure or a run as a part of its own, picked from what exists (never typed blind). */
export function InsertMenu({ figures, runs, onInsert }: { figures: Figure[]; runs: Run[]; onInsert: (markdown: string) => void }) {
  const [open, setOpen] = useState<null | "figure" | "run">(null);
  const [q, setQ] = useState("");
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => { if (box.current && !box.current.contains(e.target as Node)) setOpen(null); };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [open]);
  const items = open === "figure" ? figures.map((f) => ({ key: f.revision, label: f.title, meta: f.provenance?.source.name ?? "", md: `![[figure:${f.revision}]]` })) : open === "run" ? runs.map((r) => ({ key: String(r.id), label: r.name, meta: r.kind.toUpperCase(), md: `[[run:${r.id}]]` })) : [];
  const shown = items.filter((i) => !q || (i.label + " " + i.meta).toLowerCase().includes(q.toLowerCase())).slice(0, 50);
  return (
    <div className={s.insert} ref={box} onKeyDown={(e) => { if (e.key === "Escape") setOpen(null); }}>
      <button type="button" aria-expanded={open === "figure"} onClick={() => { setOpen(open === "figure" ? null : "figure"); setQ(""); }}><Icon icon={Image} />Figure</button>
      <button type="button" aria-expanded={open === "run"} onClick={() => { setOpen(open === "run" ? null : "run"); setQ(""); }}><Icon icon={Activity} />Run</button>
      {open ? (
        <div className={s.menu} role="dialog" aria-label={open === "figure" ? "Insert a figure" : "Insert a run"}>
          <input autoFocus placeholder={open === "figure" ? "Search figures" : "Search runs"} value={q} onChange={(e) => setQ(e.target.value)} aria-label="Search" />
          {shown.length === 0 ? <p className={s.muted}>{items.length ? "Nothing matches." : open === "figure" ? "No pinned figures yet. Save one from the viewer." : "No runs yet. Import one in the explorer."}</p> : null}
          <ul>
            {shown.map((i) => (
              <li key={i.key}>
                <button type="button" onClick={() => { onInsert(i.md); setOpen(null); }}><Icon icon={Plus} /><span>{i.label}</span><span className={s.muted}>{i.meta}</span></button>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  );
}
