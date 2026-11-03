import { Database, FileText, FolderOpen, PanelBottom, PanelLeft, PanelRight } from "lucide-react";
import { IconButton, Pill, ResizeHandle, Section, TreeRow } from "../ui";
import { LIMITS, useLayout } from "./useLayout";
import s from "./Shell.module.css";

/** Workbench frame (ADR 0005). Panels hold honest empty states until datasets exist. */
export function Shell() {
  const { layout: L, set, toggle } = useLayout();
  const cols = [`var(--w-rail)`, L.sideOpen ? `${L.side}px` : "0px", "minmax(0,1fr)", L.inspOpen ? `${L.insp}px` : "0px"].join(" ");
  const rows = ["var(--h-title)", "minmax(0,1fr)", L.timelineOpen ? `${L.timeline}px` : "0px", "var(--h-status)"].join(" ");

  return (
    <div className={s.shell} style={{ gridTemplateColumns: cols, gridTemplateRows: rows }}>
      <header className={s.title}>
        <h1 className={s.name}>Vesqira</h1>
        <span className={s.crumb}>No project open</span>
      </header>

      <nav className={s.rail} aria-label="Workbench">
        <IconButton icon={FolderOpen} label="Explorer" size="lg" pressed={L.sideOpen} onClick={() => toggle("sideOpen")} />
      </nav>

      <aside className={s.side} aria-label="Explorer" hidden={!L.sideOpen}>
        <div className={s.scroll}>
          <Section title="Datasets" meta="0">
            <p className={s.empty}>Register a FITS cube or a VGT result to see it here.</p>
          </Section>
          <Section title="Examples" defaultOpen={false}>
            <TreeRow icon={Database} trailing="—">Cube (placeholder)</TreeRow>
            <TreeRow icon={FileText} trailing="—">Map (placeholder)</TreeRow>
          </Section>
        </div>
        <ResizeHandle orientation="vertical" label="Resize explorer" value={L.side} min={LIMITS.side.min} max={LIMITS.side.max} onChange={(v) => set("side", v)} />
      </aside>

      <main className={s.canvas} aria-label="Viewer">
        <div className={s.stage}>
          <p className={s.stageTitle}>No dataset open</p>
          <p className={s.empty}>Planes and maps appear here. The image keeps its native shape and never stretches.</p>
        </div>
      </main>

      <aside className={s.insp} aria-label="Inspector" hidden={!L.inspOpen}>
        <ResizeHandle orientation="vertical" direction={-1} label="Resize inspector" value={L.insp} min={LIMITS.insp.min} max={LIMITS.insp.max} onChange={(v) => set("insp", v)} />
        <div className={s.scroll}>
          <Section title="Dataset">
            <p className={s.empty}>Nothing selected. Unknown values will show as —.</p>
          </Section>
          <Section title="Coordinates" defaultOpen={false}>
            <p className={s.empty}>—</p>
          </Section>
          <Section title="Region" defaultOpen={false}>
            <p className={s.empty}>—</p>
          </Section>
        </div>
      </aside>

      <section className={s.timeline} aria-label="Channels" hidden={!L.timelineOpen}>
        <ResizeHandle orientation="horizontal" direction={-1} label="Resize timeline" value={L.timeline} min={LIMITS.timeline.min} max={LIMITS.timeline.max} onChange={(v) => set("timeline", v)} />
        <div className={s.scroll}>
          <p className={s.empty}>No spectral axis. Channels appear here once a cube is open.</p>
        </div>
      </section>

      <footer className={s.status}>
        <IconButton icon={PanelLeft} label="Toggle explorer" pressed={L.sideOpen} onClick={() => toggle("sideOpen")} />
        <IconButton icon={PanelBottom} label="Toggle timeline" pressed={L.timelineOpen} onClick={() => toggle("timelineOpen")} />
        <IconButton icon={PanelRight} label="Toggle inspector" pressed={L.inspOpen} onClick={() => toggle("inspOpen")} />
        <span className={s.grow} />
        <Pill>No dataset</Pill>
      </footer>
    </div>
  );
}
