import { useEffect, useRef, useState } from "react";
import { FolderOpen, LayoutGrid, PanelBottom, PanelLeft, PanelRight } from "lucide-react";
import { BoardsView } from "../boards/BoardsView";
import { FigurePanel } from "../viewer/FigurePanel";
import { DatasetInspector } from "../catalog/DatasetInspector";
import { Explorer } from "../catalog/Explorer";
import { HostsPanel } from "../catalog/HostsPanel";
import { RunPanel, runFor } from "../catalog/RunPanel";
import { RunsList } from "../catalog/RunsList";
import { getCompat } from "../api/catalog";
import type { Compat } from "../api/catalog";
import { useCatalog } from "../catalog/useCatalog";
import { CompareBar, linkState } from "../viewer/CompareBar";
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
  const viewerA = useViewer(cat.selected);
  const [compareId, setCompareId] = useState<number | null>(null);
  const compareDs = cat.datasets.find((d) => d.id === compareId && d.id !== cat.selected?.id) ?? null;
  const viewerB = useViewer(compareDs);
  const [active, setActive] = useState<"a" | "b">("a");
  const [compat, setCompat] = useState<Compat | null>(null);
  const [compatErr, setCompatErr] = useState<string | null>(null);
  const [compatLoading, setCompatLoading] = useState(false);
  const useB = active === "b" && compareDs !== null;
  const viewer = useB ? viewerB : viewerA;
  const actDs = useB ? compareDs : cat.selected;
  const other = useB ? viewerA : viewerB;
  const linked = compareDs !== null && linkState(compat, compatLoading).linked;
  const flag = flagState(viewer.view, viewer.plane);

  // Is the other map known to share this one's grid? Only then are cursor and channel linked.
  useEffect(() => {
    setCompat(null);
    setCompatErr(null);
    if (!cat.selected || !compareDs) return;
    let live = true;
    setCompatLoading(true);
    getCompat(cat.selected.id, compareDs.id)
      .then((c) => live && setCompat(c))
      .catch((e: unknown) => live && setCompatErr(e instanceof Error ? e.message : "Could not compare the grids."))
      .finally(() => live && setCompatLoading(false));
    return () => {
      live = false;
    };
  }, [cat.selected?.id, compareDs?.id, cat.selected?.modifiedAt, compareDs?.modifiedAt]); // eslint-disable-line react-hooks/exhaustive-deps
  const collapsedForCompare = useRef(false);
  useEffect(() => {
    if (!compareDs) {
      setActive("a");
      if (collapsedForCompare.current) toggle("sideOpen"); // give the explorer back when the comparison ends
      collapsedForCompare.current = false;
    } else if (L.sideOpen && !collapsedForCompare.current) {
      collapsedForCompare.current = true; // two maps need the width
      toggle("sideOpen");
    }
  }, [compareDs?.id]); // eslint-disable-line react-hooks/exhaustive-deps
  // the pane you are working in drives the channel of the other one
  useEffect(() => {
    if (linked && other.channels === viewer.channels && other.channel !== viewer.channel) other.setChannel(viewer.channel);
  }, [linked, viewer.channel, viewer.channels, other.channels]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!linked) viewerA.setMirror(null), viewerB.setMirror(null);
  }, [linked]); // eslint-disable-line react-hooks/exhaustive-deps
  const [mode, setMode] = useState<"viewer" | "boards">("viewer");
  const cols = [`var(--w-rail)`, L.sideOpen ? `${L.side}px` : "0px", "minmax(0,1fr)", L.inspOpen ? `${L.insp}px` : "0px"].join(" ");
  const singlePlane = viewer.view?.channels === 1; // nothing to scrub: give the room to the map
  const rows = ["var(--h-title)", "minmax(0,1fr)", L.timelineOpen && !singlePlane ? `${L.timeline}px` : "0px", "var(--h-status)"].join(" ");

  return (
    <div className={s.shell} style={{ gridTemplateColumns: cols, gridTemplateRows: rows }}>
      <header className={s.title}>
        <h1 className={s.name}>Vesqira</h1>
        <span className={s.crumb}>No project open</span>
      </header>

      <nav className={s.rail} aria-label="Workbench">
        <IconButton icon={FolderOpen} label="Explorer" size="lg" pressed={L.sideOpen} onClick={() => toggle("sideOpen")} />
        <IconButton icon={LayoutGrid} label="Boards" size="lg" pressed={mode === "boards"} onClick={() => setMode(mode === "boards" ? "viewer" : "boards")} />
      </nav>

      <aside className={s.side} aria-label="Explorer" hidden={!L.sideOpen}>
        <div className={s.scroll}>
          <Section title="Runs" meta={String(cat.runs.length)} defaultOpen={cat.runs.length > 0}>
            <RunsList runs={cat.runs} onImport={cat.importRunFolder} onOpen={(r) => { const o = r.outputs.find((x) => x.role === "psi") ?? r.outputs[0]; if (o) cat.select(o.datasetId); }} />
          </Section>
          <Section title="Hosts" meta={String(cat.hosts.length)} defaultOpen={cat.hosts.length > 0}>
            <HostsPanel hosts={cat.hosts} onChange={cat.refreshHosts} onRegistered={cat.refreshDatasets} />
          </Section>
          <Section title="Datasets" meta={String(cat.datasets.length)}>
            <Explorer hosts={cat.hosts} datasets={cat.datasets} selectedId={cat.selectedId} loading={cat.loading} error={cat.error} onSelect={cat.select} onRegister={cat.register} />
          </Section>
        </div>
        <ResizeHandle orientation="vertical" label="Resize explorer" value={L.side} min={LIMITS.side.min} max={LIMITS.side.max} onChange={(v) => set("side", v)} />
      </aside>

      <main className={s.canvas} aria-label="Viewer">
        {mode === "boards" ? (
          <BoardsView />
        ) : cat.selected ? (
          <div className={s.compare}>
            <CompareBar datasets={cat.datasets} selectedId={cat.selected.id} compareId={compareDs?.id ?? null} onChange={setCompareId} compat={compat} loading={compatLoading} error={compatErr} />
            <div className={compareDs ? s.panes2 : s.panes1}>
              <section className={s.pane} aria-label={`Map: ${cat.selected.name}`}>
                {compareDs ? <PaneHeader name={cat.selected.name} axial={viewerA.interp !== null} active={!useB} onActivate={() => setActive("a")} /> : null}
                <div className={s.paneBody}><PlaneViewer v={viewerA} onProbe={(p) => linked && viewerB.setMirror(p)} /></div>
              </section>
              {compareDs ? (
                <section className={s.pane} aria-label={`Map: ${compareDs.name}`}>
                  <PaneHeader name={compareDs.name} axial={viewerB.interp !== null} active={useB} onActivate={() => setActive("b")} />
                  <div className={s.paneBody}><PlaneViewer v={viewerB} onProbe={(p) => linked && viewerA.setMirror(p)} /></div>
                </section>
              ) : null}
            </div>
          </div>
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
          {actDs && runFor(cat.runs, actDs.id) ? (
            <Section title="Run">
              <RunPanel run={runFor(cat.runs, actDs.id)!.run} role={runFor(cat.runs, actDs.id)!.role} datasets={cat.datasets} onOpen={cat.select} onCompare={(id) => { if (id !== cat.selected?.id) setCompareId(id); }} />
            </Section>
          ) : null}
          <Section title="Coordinates">
            {actDs ? <CoordinateReadout v={viewer} other={linked && other.view ? { name: (useB ? cat.selected : compareDs)?.name ?? "", v: other } : null} /> : <p className={s.empty}>{EM}</p>}
          </Section>
          {actDs && (viewer.view === null || viewer.view.channels === 1) ? (
            <Section title="Interpretation">
              <InterpretationPanel key={actDs.id} v={viewer} dataset={actDs} datasets={cat.datasets} declare={cat.declare} />
            </Section>
          ) : null}
          {viewer.interp ? (
            <Section title="Orientation">
              <OrientationStats v={viewer} />
            </Section>
          ) : null}
          <Section title="Display">
            {actDs ? <DisplayControls v={viewer} /> : <p className={s.empty}>{EM}</p>}
          </Section>
          <Section title="Region">
            {actDs ? <RegionPanel v={viewer} /> : <p className={s.empty}>{EM}</p>}
          </Section>
          <Section title="Spectrum">
            {actDs ? <SpectrumPlot v={viewer} /> : <p className={s.empty}>{EM}</p>}
          </Section>
          <Section title="Figure" defaultOpen={false}>
            {actDs ? <FigurePanel v={viewer} dataset={actDs} /> : <p className={s.empty}>{EM}</p>}
          </Section>
          <Section title="Dataset">
            {actDs ? <DatasetInspector d={actDs} /> : <p className={s.empty}>Nothing selected. Unknown values show as —.</p>}
          </Section>
        </div>
      </aside>

      <section className={s.timeline} aria-label="Channels" hidden={!L.timelineOpen || singlePlane}>
        <ResizeHandle orientation="horizontal" direction={-1} label="Resize timeline" value={L.timeline} min={LIMITS.timeline.min} max={LIMITS.timeline.max} onChange={(v) => set("timeline", v)} />
        <div className={s.scroll}>
          {actDs ? <ChannelTimeline v={viewer} /> : <p className={s.empty}>No spectral axis. Channels appear here once a cube is open.</p>}
        </div>
      </section>

      <footer className={s.status}>
        <IconButton icon={PanelLeft} label="Toggle explorer" pressed={L.sideOpen} onClick={() => toggle("sideOpen")} />
        <IconButton icon={PanelBottom} label="Toggle timeline" pressed={L.timelineOpen} onClick={() => toggle("timelineOpen")} />
        <IconButton icon={PanelRight} label="Toggle inspector" pressed={L.inspOpen} onClick={() => toggle("inspOpen")} />
        <span className={s.grow} />
        <Pill tone={actDs ? flag.tone : "neutral"}>{actDs ? (viewer.view ? flag.text : "Reading layout") : "No dataset"}</Pill>
      </footer>
    </div>
  );
}

function PaneHeader({ name, axial, active, onActivate }: { name: string; axial: boolean; active: boolean; onActivate: () => void }) {
  return (
    <div className={s.paneHead}>
      <span className={s.paneName} title={name}>{name}</span>
      <Pill>{axial ? "Axial orientation" : "Scalar"}</Pill>
      <button type="button" className={s.paneBtn} aria-pressed={active} onClick={onActivate}>{active ? "Inspector shows this map" : "Show in inspector"}</button>
    </div>
  );
}
