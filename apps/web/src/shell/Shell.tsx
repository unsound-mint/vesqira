import { FolderOpen, PanelBottom, PanelLeft, PanelRight } from "lucide-react";
import { DatasetInspector } from "../catalog/DatasetInspector";
import { Explorer } from "../catalog/Explorer";
import { useCatalog } from "../catalog/useCatalog";
import { EM } from "../catalog/format";
import { ChannelTimeline } from "../viewer/ChannelTimeline";
import { CoordinateReadout, DisplayControls } from "../viewer/Panels";
import { PlaneViewer } from "../viewer/PlaneViewer";
import { InterpretationPanel, OrientationStats } from "../viewer/OrientationPanels";
import { RegionPanel } from "../viewer/RegionPanel";
import { SpectrumPlot } from "../viewer/SpectrumPlot";
import { flagState } from "../viewer/format";
import { useViewer } from "../viewer/useViewer";
import { IconButton, Pill, ResizeHandle, Section } from "../ui";
import { LIMITS, useLayout } from "./useLayout";
import s from "./Shell.module.css";

/** Workbench frame (ADR 0005). Panels hold honest empty states until datasets exist. */
export function Shell() {
  const { layout: L, set, toggle } = useLayout();
  const cat = useCatalog();
  const viewer = useViewer(cat.selected);
  const flag = flagState(viewer.view, viewer.plane);
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
          <Section title="Datasets" meta={String(cat.datasets.length)}>
            <Explorer datasets={cat.datasets} selectedId={cat.selectedId} loading={cat.loading} error={cat.error} onSelect={cat.select} onRegister={cat.register} />
          </Section>
        </div>
        <ResizeHandle orientation="vertical" label="Resize explorer" value={L.side} min={LIMITS.side.min} max={LIMITS.side.max} onChange={(v) => set("side", v)} />
      </aside>

      <main className={s.canvas} aria-label="Viewer">
        {cat.selected ? (
          <PlaneViewer v={viewer} />
        ) : (
          <div className={s.stage}>
            <p className={s.stageTitle}>No dataset open</p>
            <p className={s.empty}>Planes and maps appear here. The image keeps its native shape and never stretches.</p>
          </div>
        )}
      </main>

      <aside className={s.insp} aria-label="Inspector" hidden={!L.inspOpen}>
        <ResizeHandle orientation="vertical" direction={-1} label="Resize inspector" value={L.insp} min={LIMITS.insp.min} max={LIMITS.insp.max} onChange={(v) => set("insp", v)} />
        <div className={s.scroll}>
          <Section title="Coordinates">
            {cat.selected ? <CoordinateReadout v={viewer} /> : <p className={s.empty}>{EM}</p>}
          </Section>
          {cat.selected && viewer.view?.channels === 1 ? (
            <Section title="Interpretation">
              <InterpretationPanel key={cat.selected.id} v={viewer} dataset={cat.selected} datasets={cat.datasets} declare={cat.declare} />
            </Section>
          ) : null}
          {viewer.interp ? (
            <Section title="Orientation">
              <OrientationStats v={viewer} />
            </Section>
          ) : null}
          <Section title="Display">
            {cat.selected ? <DisplayControls v={viewer} /> : <p className={s.empty}>{EM}</p>}
          </Section>
          <Section title="Region">
            {cat.selected ? <RegionPanel v={viewer} /> : <p className={s.empty}>{EM}</p>}
          </Section>
          <Section title="Spectrum">
            {cat.selected ? <SpectrumPlot v={viewer} /> : <p className={s.empty}>{EM}</p>}
          </Section>
          <Section title="Dataset">
            {cat.selected ? <DatasetInspector d={cat.selected} /> : <p className={s.empty}>Nothing selected. Unknown values show as —.</p>}
          </Section>
        </div>
      </aside>

      <section className={s.timeline} aria-label="Channels" hidden={!L.timelineOpen}>
        <ResizeHandle orientation="horizontal" direction={-1} label="Resize timeline" value={L.timeline} min={LIMITS.timeline.min} max={LIMITS.timeline.max} onChange={(v) => set("timeline", v)} />
        <div className={s.scroll}>
          {cat.selected ? <ChannelTimeline v={viewer} /> : <p className={s.empty}>No spectral axis. Channels appear here once a cube is open.</p>}
        </div>
      </section>

      <footer className={s.status}>
        <IconButton icon={PanelLeft} label="Toggle explorer" pressed={L.sideOpen} onClick={() => toggle("sideOpen")} />
        <IconButton icon={PanelBottom} label="Toggle timeline" pressed={L.timelineOpen} onClick={() => toggle("timelineOpen")} />
        <IconButton icon={PanelRight} label="Toggle inspector" pressed={L.inspOpen} onClick={() => toggle("inspOpen")} />
        <span className={s.grow} />
        <Pill tone={cat.selected ? flag.tone : "neutral"}>{cat.selected ? (viewer.view ? flag.text : "Reading layout") : "No dataset"}</Pill>
      </footer>
    </div>
  );
}
