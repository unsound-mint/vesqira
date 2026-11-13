import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import type { ReactNode } from "react";
import { Columns3, FileText, LifeBuoy, FolderOpen, FolderPlus, LayoutGrid, Server, Table2, Terminal as TerminalIcon, PanelBottom, PanelLeft, PanelRight } from "lucide-react";
import { BoardsView } from "../boards/BoardsView";
import { ReportsView } from "../reports/ReportsView";
import { RunsView } from "../runs/RunsView";
import { initialRunsView } from "../runs/runsViewState";
import type { RunsViewState } from "../runs/runsViewState";
import { JobsView, jobFor } from "../jobs/JobsView";
import { FamilyView } from "../family/FamilyView";
import type { FamilyPreset } from "../family/FamilyView";
import { NotificationBell } from "../notifications/NotificationBell";
import { useNotifications } from "../notifications/useNotifications";
import { FigurePanel } from "../viewer/FigurePanel";
import { DatasetInspector } from "../catalog/DatasetInspector";
import { CatalogView } from "../catalog/CatalogView";
import { CatalogInspector } from "../catalog/CatalogInspector";
import { LocationsPanel } from "../catalog/LocationsPanel";
import { NewerNotices } from "../catalog/NewerNotices";
import { UnitControl } from "../catalog/UnitControl";
import { CorrectionsPanel } from "../catalog/CorrectionsPanel";
import { MasksPanel } from "../catalog/MasksPanel";
import { fileStatesItsUnit } from "../catalog/unit";
import { Explorer } from "../catalog/Explorer";
import { virtualId } from "../api/planes";
import { SubsetPage } from "../catalog/SubsetPage";
import { SubsetsSection } from "../catalog/SubsetsSection";
import { useSubsets } from "../catalog/useSubsets";
import { useAccess } from "../catalog/useAccess";
import { AccessPanel } from "../catalog/AccessPanel";
import { AnalysisPanel, DerivedPanel } from "../catalog/AnalysisPanel";
import { RelativeOrientationPanel, RelativeOrientationResult } from "../catalog/RelativeOrientation";
import { NatureControl } from "../catalog/NatureControl";
import { StorageControl } from "../catalog/StorageControl";
import { UsedIn } from "../catalog/UsedIn";
import { HostsPanel } from "../catalog/HostsPanel";
import { RunPanel, RunsUsingFile, runFor } from "../catalog/RunPanel";
import { RunPage } from "../runs/RunPage";
import { EntryActions } from "../catalog/EntryActions";
import { DatasetOffline } from "../catalog/OfflineAction";
import { RerunDraft } from "../runs/RerunDraft";
import { RunsList } from "../catalog/RunsList";
import { FirstLook } from "../projects/FirstLook";
import { HealthView } from "../projects/HealthView";
import { ImportView } from "../projects/ImportView";
import { NeedsView } from "../projects/NeedsView";
import { OpenProject } from "../projects/OpenProject";
import { ProjectExplorer } from "../projects/ProjectExplorer";
import { healthScore, location } from "../projects/format";
import { useProject } from "../projects/useProject";
import { writeProjectFile } from "../api/projects";
import { editorEnabled } from "../api/editor";
import { terminalEnabled } from "../api/terminal";
import { getCompat, isCatalog, isImage } from "../api/catalog";
import { HealpixSampleDone, HealpixSamplePanel } from "../catalog/HealpixSample";
import type { Compat, ImageDataset } from "../api/catalog";
import { useCatalog } from "../catalog/useCatalog";
import { CompareBar, linkState } from "../viewer/CompareBar";
import { getRendererNote, subscribeRendererNote } from "../viewer/glRenderer";
import { CompareInspector } from "../viewer/CompareInspector";
import { EM } from "../catalog/format";
import { ChannelTimeline } from "../viewer/ChannelTimeline";
import { CoordinateReadout, DisplayControls } from "../viewer/Panels";
import { PlaneViewer } from "../viewer/PlaneViewer";
import { InterpretationPanel, OrientationStats } from "../viewer/OrientationPanels";
import { RegionPanel } from "../viewer/RegionPanel";
import { OverlayPanel } from "../viewer/OverlayPanel";
import { SpectrumPlot } from "../viewer/SpectrumPlot";
import { ChannelGrid } from "../viewer/ChannelGrid";
import { PvPanel } from "../viewer/PvPanel";
import { SkyPanel } from "../sky/SkyPanel";
import { GaussianComponents } from "../viewer/GaussianComponents";
import { flagState, spectralText } from "../viewer/format";
import { useViewer } from "../viewer/useViewer";
import { HistoryPanel } from "../history/HistoryPanel";
import { useExplorationHistory } from "../history/useExplorationHistory";
import { UndoBar } from "../undo/UndoBar";
import { useUndoKeys } from "../undo/keys";
import type { Scene } from "../scene/scene";
import type { Pixel } from "../viewer/useViewer";
import { limits } from "../viewer/render";
import { channelTarget, defaultPrefs, linkGroup, linkPills, links, mapPixel, sceneLink } from "../scene/link";
import type { LinkPrefs, PaneId } from "../scene/link";
import { useChannelLink, useSkyLink } from "../viewer/useLinkTransform";
import { useLinkFollow } from "../viewer/useLinkFollow";
import type { LinkControls } from "../viewer/CompareBar";
import { Button, IconButton, Pill, ResizeHandle, Section, getHostOffer, subscribeHostOffer } from "../ui";
import { ErrorReferenceHost } from "./ErrorReferenceHost";
import { LIMITS, useLayout } from "./useLayout";
import s from "./Shell.module.css";

// Loaded when a file is first opened for editing: it brings Monaco (ADR 0029).
const EditorScreen = lazy(() => import("../editor/EditorScreen"));
// Loaded when the terminal is first opened: it brings xterm.js (ADR 0029).
const TerminalScreen = lazy(() => import("../terminal/TerminalScreen"));
// Loaded when the support dialog is first opened: it brings the modal and React Aria.
const SupportDialog = lazy(() => import("./SupportDialog"));

const CATALOG_PROBLEM = "This catalog has changed on disk, is missing or is outside the source folders since it was registered, so its rows cannot be read until it is registered again.";

/** Workbench frame (ADR 0005). Panels hold honest empty states until datasets exist. */
/** `account` is the group server's account control for the title bar (ADR 0050 slice 2); local mode passes none. */
export function Shell({ account }: { account?: ReactNode } = {}) {
  const { layout: L, set, toggle } = useLayout();
  const cat = useCatalog();
  const subs = useSubsets();
  const access = useAccess();
  const [subsetId, setSubsetId] = useState<number | null>(null); // a virtual subset is open in the main area instead of a dataset (ADR 0063)
  useEffect(() => { void subs.refresh(); }, [cat.allDatasets]); // eslint-disable-line react-hooks/exhaustive-deps -- a changed file makes its subsets stale
  const hostOffer = useSyncExternalStore(subscribeHostOffer, getHostOffer); // a file-manager mount of an unregistered host (ADR 0031)
  const rendererNote = useSyncExternalStore(subscribeRendererNote, getRendererNote); // set only when drawing fell back to the CPU
  const { refreshDatasets, refreshRuns } = cat;
  const proj = useProject(useCallback(() => { void refreshDatasets(); void refreshRuns(); }, [refreshDatasets, refreshRuns]));
  const project = proj.project;
  const [editor, setEditor] = useState(false); // the server was started with --enable-editor
  const [editing, setEditing] = useState<{ path: string; hostId: number } | null>(null);
  const [terminal, setTerminal] = useState(false); // the server was started with --enable-terminal
  const [terminalOpen, setTerminalOpen] = useState(false);
  const [supportOpen, setSupportOpen] = useState(false);
  useEffect(() => {
    let live = true;
    void editorEnabled().then((on) => live && setEditor(on));
    void terminalEnabled().then((on) => live && setTerminal(on));
    return () => {
      live = false;
    };
  }, []);
  // a job ended or its results were imported: the lists the links point into are refreshed before anyone follows one
  const notes = useNotifications(useCallback(() => { void refreshDatasets(); void refreshRuns(); }, [refreshDatasets, refreshRuns]));
  const hostName = (id: number) => cat.hosts.find((h) => h.id === id)?.name ?? "host";
  const runHostName = useCallback((id: number) => cat.hosts.find((h) => h.id === id)?.name, [cat.hosts]);
  // A virtual subset opened from the explorer is read by the same viewer, through /api/subsets/{sid} (the negative of its id is the
  // viewer's id for it). A stale one is not read at all: the main area says why and offers the re-pin.
  const openSub = subsetId !== null ? subs.subsets.find((x) => x.id === subsetId) ?? null : null;
  const virtualDs = useMemo<(ImageDataset & { archived?: boolean }) | null>(() => {
    if (!openSub || openSub.stale) return null;
    const src = cat.allDatasets.find((d) => d.id === openSub.sourceId);
    if (!src || isCatalog(src)) return null;
    return { ...src, id: virtualId(openSub.id), name: openSub.name, modifiedAt: `${src.modifiedAt}#v${openSub.version}`, locations: [], archived: false } as ImageDataset & { archived?: boolean };
  }, [openSub, cat.allDatasets]);
  const shownImage = virtualDs ?? cat.selectedImage;
  const viewerA = useViewer(shownImage);
  const [compareId, setCompareId] = useState<number | null>(null);
  const compareDs = cat.datasets.find((d) => d.id === compareId && d.id !== shownImage?.id) ?? null;
  const viewerB = useViewer(compareDs);
  const [active, setActive] = useState<"a" | "b">("a");
  const [stage, setStage] = useState<"map" | "grid">("map"); // the map pane(s), or a grid of channel maps of the open dataset
  const [compat, setCompat] = useState<Compat | null>(null);
  const [compatErr, setCompatErr] = useState<string | null>(null);
  const [compatLoading, setCompatLoading] = useState(false);
  const [shared, setShared] = useState(false);
  const [sharedLimits, setSharedLimits] = useState<[number, number] | null>(null);
  const useB = active === "b" && compareDs !== null;
  const viewer = useB ? viewerB : viewerA;
  const actDs = useB ? compareDs : shownImage;
  const other = useB ? viewerA : viewerB;
  // The two panes are one link group (ADR 0042, section 4): on the same grid on their own, on different grids once the person
  // links them by WCS, and only while both are in it. Who has left, the name and the facets belong to this pair of datasets.
  const sameGrid = compareDs !== null && linkState(compat, compatLoading).linked;
  const pairKey = `${shownImage?.id ?? ""}:${compareDs?.id ?? ""}`;
  const [prefsFor, setPrefsFor] = useState<{ key: string; p: LinkPrefs }>(() => ({ key: pairKey, p: defaultPrefs() }));
  const linkPrefs = prefsFor.key === pairKey ? prefsFor.p : defaultPrefs();
  const updatePrefs = (patch: Partial<LinkPrefs>) => setPrefsFor({ key: pairKey, p: { ...linkPrefs, ...patch } });
  const group = compareDs !== null ? linkGroup(linkPrefs, sameGrid, compat?.channelLink.ok === false ? compat.channelLink.reason : null) : null;
  const linked = group !== null;
  const idA = shownImage?.id ?? null;
  const idB = compareDs?.id ?? null;
  const stamp = `${shownImage?.modifiedAt ?? ""}|${compareDs?.modifiedAt ?? ""}`;
  const imageA = viewerA.view ? { w: viewerA.view.width, h: viewerA.view.height } : null;
  const imageB = viewerB.view ? { w: viewerB.view.width, h: viewerB.view.height } : null;
  const [paneA, setPaneA] = useState<{ w: number; h: number } | null>(null);
  const [paneB, setPaneB] = useState<{ w: number; h: number } | null>(null);
  const byWcs = group !== null && group.basis === "wcs";
  const wantSky = byWcs && (group.facets.cursor || group.facets.view);
  const skyAB = useSkyLink(wantSky, idA, idB, viewerA.scene, imageA, paneA, stamp);
  const skyBA = useSkyLink(wantSky, idB, idA, viewerB.scene, imageB, paneB, stamp);
  const wantChannels = byWcs && group.facets.channel;
  const chAB = useChannelLink(wantChannels, idA, idB, stamp);
  const chBA = useChannelLink(wantChannels, idB, idA, stamp);
  useLinkFollow(group, { a: viewerA, b: viewerB }, active, { ab: skyAB, ba: skyBA }, { ab: chAB, ba: chBA }, { a: idA, b: idB });
  // a scene records how its pane was linked, so a figure says how its panes followed each other
  const linkKey = JSON.stringify(sceneLink(group, { a: idA, b: idB }));
  useEffect(() => {
    const l = sceneLink(group, { a: idA, b: idB });
    viewerA.dispatch({ type: "setLink", link: l });
    viewerB.dispatch({ type: "setLink", link: l });
  }, [linkKey, viewerA.scene?.base.datasetId, viewerB.scene?.base.datasetId]); // eslint-disable-line react-hooks/exhaustive-deps
  // where the hovered pixel of one pane is in the other: the same pixel on one grid, the worker's transform otherwise
  const [outsideIn, setOutsideIn] = useState<PaneId | null>(null);
  function mirror(from: PaneId, p: Pixel | null) {
    if (!group || !group.facets.cursor) return;
    const to: PaneId = from === "a" ? "b" : "a";
    const dest = to === "a" ? viewerA : viewerB;
    if (!p || group.basis === "same-grid") {
      dest.setMirror(p);
      setOutsideIn(null);
      return;
    }
    const m = mapPixel((from === "a" ? skyAB : skyBA).map, p);
    dest.setMirror(m.kind === "inside" ? m.at : null);
    setOutsideIn(m.kind === "outside" ? to : null);
  }
  const flag = flagState(viewer.view, viewer.plane);
  const activeSky = useB ? skyBA : skyAB;
  const activeChannels = useB ? chBA : chAB;
  const lead: PaneId = useB ? "b" : "a";
  const channelLine = (() => {
    if (!byWcs || !group.facets.channel || !activeChannels.map) return null;
    const t = channelTarget(activeChannels.map, viewer.channel);
    const here = viewer.scene?.base.spectral;
    if (!t || !here) return null;
    const there = lead === "a" ? "B" : "A";
    const here_ = lead === "a" ? "A" : "B";
    const at = `${here_}'s ${spectralText(here.value, here.unit)}`;
    return "channel" in t
      ? `${there} channel ${t.channel + 1} is ${spectralText(Math.abs(t.residual), t.unit)} from ${at}`
      : `${at} is outside ${there}'s spectral range: the nearest ${there} channel is ${spectralText(Math.abs(t.residual), t.unit)} away, so ${there} does not follow`;
  })();
  const linkControls: LinkControls = { group, prefs: linkPrefs, onPrefs: updatePrefs, pills: group ? linkPills(group, activeSky, activeChannels) : [], channelLine };
  const readoutOther = (() => {
    if (!group || !other.view) return null;
    const name = (useB ? shownImage : compareDs)?.name ?? "";
    if (group.basis === "same-grid") return { name, v: other };
    if (!group.facets.cursor) return null;
    const m = viewer.probe ? mapPixel(activeSky.map, viewer.probe) : null;
    return { name, v: other, at: m === null || m.kind === "stale" ? ("pending" as const) : m.kind === "inside" ? m.at : ("outside" as const) };
  })();

  // One colour scale for both maps: only for scalar maps in the same brightness unit. The limits start from the two
  // maps' 1st to 99th percentiles together and then follow whatever the person sets on the map they are working in.
  const unitA = viewerA.view?.bunit ?? null;
  const shareReason = !compareDs ? null : viewerA.interp || viewerB.interp ? "Orientation maps have no brightness scale to share." : !unitA || unitA !== (viewerB.view?.bunit ?? null) ? "The two maps do not declare the same brightness unit." : null;
  const canShare = compareDs !== null && shareReason === null && viewerA.plane !== null && viewerB.plane !== null;
  function toggleShared(on: boolean) {
    setShared(on);
    if (!on) {
      viewerA.setRange("percentile");
      viewerB.setRange("percentile");
      setSharedLimits(null);
      return;
    }
    const la = viewerA.plane ? limits(viewerA.plane.head.stats, "percentile") : null;
    const lb = viewerB.plane ? limits(viewerB.plane.head.stats, "percentile") : null;
    if (la && lb) setSharedLimits([Math.min(la[0], lb[0]), Math.max(la[1], lb[1])]);
  }
  useEffect(() => {
    if (shared && (!compareDs || shareReason !== null)) {
      setShared(false);
      setSharedLimits(null);
    }
  }, [shared, compareDs, shareReason]);
  useEffect(() => {
    // a limit changed on the map being worked in becomes the shared one
    if (!shared) return;
    const c = viewer.customLimits;
    if (viewer.range === "custom" && c && (!sharedLimits || c[0] !== sharedLimits[0] || c[1] !== sharedLimits[1])) setSharedLimits([c[0], c[1]]);
  }, [shared, viewer.customLimits, viewer.range]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!shared || !sharedLimits) return;
    for (const v of [viewerA, viewerB]) {
      if (v.range !== "custom" || !v.customLimits || v.customLimits[0] !== sharedLimits[0] || v.customLimits[1] !== sharedLimits[1]) {
        v.dispatchFact({ type: "setDisplay", display: { limits: sharedLimits, range: "custom" } }); // follows the shared scale, not a command
      }
    }
  }, [shared, sharedLimits, viewerA.range, viewerA.customLimits, viewerB.range, viewerB.customLimits]); // eslint-disable-line react-hooks/exhaustive-deps

  // both scenes say their colour scale is shared, also after a pane opens another dataset (which starts a new scene)
  useEffect(() => {
    for (const v of [viewerA, viewerB]) v.setScaleGroup(shared ? "compare" : null);
  }, [shared, viewerA.scene?.base.datasetId, viewerB.scene?.base.datasetId]); // eslint-disable-line react-hooks/exhaustive-deps

  // Is the other map known to share this one's grid? Only then are cursor and channel linked.
  useEffect(() => {
    setCompat(null);
    setCompatErr(null);
    if (!shownImage || !compareDs) return;
    let live = true;
    setCompatLoading(true);
    getCompat(shownImage.id, compareDs.id)
      .then((c) => live && setCompat(c))
      .catch((e: unknown) => live && setCompatErr(e instanceof Error ? e.message : "Could not compare the grids."))
      .finally(() => live && setCompatLoading(false));
    return () => {
      live = false;
    };
  }, [shownImage?.id, compareDs?.id, shownImage?.modifiedAt, compareDs?.modifiedAt]); // eslint-disable-line react-hooks/exhaustive-deps
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
  useEffect(() => {
    if (!links(group, "cursor")) viewerA.setMirror(null), viewerB.setMirror(null), setOutsideIn(null);
  }, [linked, group?.facets.cursor, group?.basis]); // eslint-disable-line react-hooks/exhaustive-deps
  const [focusJob, setFocusJob] = useState<number | null>(null);
  const [familyPreset, setFamilyPreset] = useState<FamilyPreset | null>(null);
  const [mode, setMode] = useState<"viewer" | "boards" | "jobs" | "reports" | "runs" | "run" | "rerun" | "open" | "look" | "needs" | "health" | "import" | "family">("viewer");
  const [runId, setRunId] = useState<number | null>(null);
  const [runFrom, setRunFrom] = useState<"viewer" | "boards" | "jobs" | "reports" | "runs">("viewer");
  // The runs page keeps its query, columns, sort, plot and selection while a run is open, so Back returns to it as it was.
  const [runsView, setRunsView] = useState<RunsViewState>(initialRunsView);
  const patchRunsView = useCallback((p: Partial<RunsViewState>) => setRunsView((v) => ({ ...v, ...p })), []);
  const from = mode === "boards" || mode === "jobs" || mode === "reports" || mode === "runs" ? mode : "viewer"; // the project pages go back to the viewer
  const openRun = (id: number) => {
    if (mode !== "run" && mode !== "rerun") setRunFrom(from);
    setRunId(id);
    setMode("run");
  };
  const runAgain = (id: number) => {
    if (mode !== "run" && mode !== "rerun") setRunFrom(from);
    setRunId(id);
    setMode("rerun");
  };
  const compareInViewer = (a: number, b: number) => {
    cat.select(a);
    setCompareId(b);
    setMode("viewer");
  };
  // Exploration history of the primary pane, per project, and undo of the active pane's edits (ADR 0057). "Continue from here" may
  // have to open another dataset first; the scene is applied once that pane has started over on it.
  const [pendingScene, setPendingScene] = useState<Scene | null>(null);
  const [historyOpen, setHistoryOpen] = useState(false); // up to 500 rows are only drawn while the section is open
  const [skyOpen, setSkyOpen] = useState(false); // footprints are read from the worker only while the sky section is open (ADR 0072)
  const hist = useExplorationHistory({
    projectId: project?.id ?? 0,
    scene: virtualDs ? null : viewerA.scene, // a window is not a dataset the history could continue from
    datasetName: (id) => cat.allDatasets.find((d) => d.id === id)?.name ?? `dataset ${id}`,
    haveDataset: (id) => cat.allDatasets.some((d) => d.id === id),
    restore: (scene, opensOther) => {
      setActive("a");
      setCompareId(null);
      setMode("viewer");
      setStage("map");
      if (opensOther) {
        setPendingScene(scene);
        cat.select(scene.base.datasetId);
      } else viewerA.applyScene(scene, "continue from history");
    },
  });
  const { applyScene: applyToA } = viewerA;
  useEffect(() => {
    if (pendingScene && viewerA.scene?.base.datasetId === pendingScene.base.datasetId && viewerA.view !== null) {
      applyToA(pendingScene, "continue from history");
      setPendingScene(null);
    }
  }, [pendingScene, viewerA.scene?.base.datasetId, viewerA.view, applyToA]);
  useUndoKeys(mode === "viewer" && shownImage !== null, {
    undo: () => { if (!viewer.undoName) return false; viewer.undo(); return true; },
    redo: () => { if (!viewer.redoName) return false; viewer.redo(); return true; },
  });
  const rerunRun = mode === "rerun" ? cat.runs.find((r) => r.id === runId) ?? null : null;
  const pageRun = mode === "run" ? cat.runs.find((r) => r.id === runId) ?? null : null;
  const singlePlane = viewer.view?.channels === 1; // nothing to scrub: give the room to the map
  // The run page is a full page of its own: the map inspector and channel timeline describe the viewer, not the run.
  const noProjectYet = mode === "viewer" && !project && !shownImage && !cat.selectedCatalog && subsetId === null; // nothing open at all: the way in is opening a folder
  // Every mode but the viewer is a page of its own (Jobs, Boards and Reports included): the map inspector and the channel
  // timeline describe a dataset in the viewer, so beside a page they would be empty sections taking its width.
  const onPage = mode !== "viewer" || noProjectYet;
  // On a narrow screen the explorer is an overlay on the canvas, so any page the person moves to (and the first paint, when a
  // layout saved on a wide screen says "open") has to uncover it: the explorer is open only when asked for.
  const narrowNow = () => typeof matchMedia === "function" && matchMedia("(width <= 800px)").matches;
  useEffect(() => {
    if (narrowNow()) set("sideOpen", false);
  }, [mode]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (typeof matchMedia !== "function") return;
    const q = matchMedia("(width <= 800px)");
    if (typeof q.addEventListener !== "function") return;
    const onChange = () => { if (q.matches) { set("sideOpen", false); set("inspOpen", false); } };
    q.addEventListener("change", onChange);
    return () => q.removeEventListener("change", onChange);
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  const openSubset = (id: number) => {
    setSubsetId(id);
    cat.select(null);
    if (mode !== "viewer" && mode !== "rerun") setMode("viewer");
    if (typeof matchMedia === "function" && matchMedia("(width <= 800px)").matches) set("sideOpen", false);
  };
  const selectDataset = (id: number) => {
    setSubsetId(null);
    cat.select(id);
    // choosing a dataset is asking to see it; a draft being edited (rerun) is not thrown away by a click in the explorer
    if (mode !== "viewer" && mode !== "rerun") setMode("viewer");
    // narrow screens: the explorer is an overlay on the viewer, so it has to get out of the way of what was just chosen
    if (typeof matchMedia === "function" && matchMedia("(width <= 800px)").matches) set("sideOpen", false);
  };
  async function openFolder(path: string, hostId: number) {
    const p = await proj.open(path, hostId);
    if (p) setMode(p.projectFile.exists ? "viewer" : "look");
  }
  async function writeFile(text: string): Promise<string | null> {
    if (!project) return "No project is open.";
    try {
      await writeProjectFile(project.id, text);
      await proj.refresh(project.id);
      setMode("viewer");
      return null;
    } catch (e) {
      return e instanceof Error ? e.message : "Could not write the project file.";
    }
  }
  const health = project?.scanned ? healthScore(project.health) : null;
  // The catalog's own lists (every run and dataset ever registered, hosts, one-off registration). With a project open they sit
  // under one closed group, so the sidebar does not show two "Runs" and two "Datasets" with different counts.
  const catalog = (
    <>
      <Section title="Runs" meta={String(cat.runs.length)} defaultOpen={cat.runs.length > 0}>
        <RunsList runs={cat.runs} hosts={cat.hosts} jobs={cat.jobs} query={runsView.query} onQuery={(q) => patchRunsView({ query: q })} onImport={cat.importRunFolder} onOpen={(r) => openRun(r.id)} onOpenTable={() => setMode("runs")} />
      </Section>
      <Section title="Hosts" meta={String(cat.hosts.length)} defaultOpen={cat.hosts.length > 0} openSignal={hostOffer?.serial}>
        <HostsPanel hosts={cat.hosts} onChange={cat.refreshHosts} onRegistered={cat.refreshDatasets} />
      </Section>
      <Section title="Datasets" meta={String(cat.datasets.length + cat.catalogCount)}>
        <Explorer hosts={cat.hosts} datasets={cat.allDatasets} selectedId={cat.selectedId} problems={cat.problems} access={access.labels} loading={cat.loading} error={cat.error} onSelect={selectDataset} subsets={subs.subsets} selectedSubsetId={subsetId} onSelectSubset={openSubset} onRegister={cat.register} offer={cat.offer} onAddLocation={cat.addAsLocation} onDismissOffer={cat.dismissOffer} />
      </Section>
    </>
  );
  const inspShown = L.inspOpen && !onPage;
  // a channel timeline belongs to a cube: not to a page, a catalog (rows, not channels) or nothing selected
  const timelineShown = L.timelineOpen && !singlePlane && !onPage && actDs !== null;
  const sideShown = L.sideOpen && mode !== "reports"; // reports have their own page tree
  const cols = [`var(--w-rail)`, sideShown ? `${L.side}px` : "0px", "minmax(0,1fr)", inspShown ? `${L.insp}px` : "0px"].join(" ");
  const rows = ["var(--h-title)", "minmax(0,1fr)", timelineShown ? `${L.timeline}px` : "0px", "var(--h-status)"].join(" ");

  return (
    <div className={s.shell} style={{ gridTemplateColumns: cols, gridTemplateRows: rows }}>
      <a className={s.skip} href="#main-content" onClick={(e) => { e.preventDefault(); document.getElementById("main-content")?.focus(); }}>{mode === "viewer" ? "Skip to the viewer" : "Skip to the page"}</a>
      <header className={s.title}>
        <h1 className={s.name}>Vesqira</h1>
        <span className={s.crumb} title={project?.path}>{project ? `${project.name} · ${location(project, hostName)}` : "No project open"}</span>
        <span className={s.grow} />
        <IconButton icon={LifeBuoy} label="Create a support bundle" onClick={() => setSupportOpen(true)} />
        <NotificationBell items={notes.items} unread={notes.unread} onOpened={notes.markAllRead} onOpenJob={(id) => { setFocusJob(id); setMode("jobs"); }} onOpenRun={openRun} />
        {account}
        {project && project.scanned && project.needsCount > 0 ? (
          <button type="button" className={s.pillBtn} aria-pressed={mode === "needs"} onClick={() => setMode("needs")}><Pill tone="warn">{`${project.needsCount} to answer`}</Pill></button>
        ) : null}
        {health && health.of > 0 ? (
          <button type="button" className={s.pillBtn} aria-pressed={mode === "health"} onClick={() => setMode("health")}><Pill tone={health.ok === health.of ? "ok" : "warn"}>{`health ${health.ok} of ${health.of}`}</Pill></button>
        ) : null}
      </header>

      <nav className={s.rail} aria-label="Workbench">
        <IconButton icon={FolderPlus} label="Open project…" size="lg" pressed={mode === "open"} onClick={() => setMode(mode === "open" ? "viewer" : "open")} />
        <IconButton icon={FolderOpen} label="Explorer" size="lg" pressed={L.sideOpen} onClick={() => toggle("sideOpen")} />
        <IconButton icon={LayoutGrid} label="Boards" size="lg" pressed={mode === "boards"} onClick={() => setMode(mode === "boards" ? "viewer" : "boards")} />
        <IconButton icon={Table2} label="Results table" size="lg" pressed={mode === "runs"} onClick={() => setMode(mode === "runs" ? "viewer" : "runs")} />
        <IconButton icon={Columns3} label="Compare a family of maps" size="lg" pressed={mode === "family"} onClick={() => setMode(mode === "family" ? "viewer" : "family")} />
        <IconButton icon={Server} label="Jobs" size="lg" pressed={mode === "jobs"} onClick={() => setMode(mode === "jobs" ? "viewer" : "jobs")} />
        <IconButton icon={FileText} label="Reports" size="lg" pressed={mode === "reports"} onClick={() => setMode(mode === "reports" ? "viewer" : "reports")} />
        {terminal ? <IconButton icon={TerminalIcon} label="Terminal" size="lg" pressed={terminalOpen} onClick={() => setTerminalOpen(true)} /> : null}
      </nav>

      <aside className={s.side} aria-label="Explorer" hidden={!sideShown}>
        <div className={s.scroll}>
          {project ? (
            <ProjectExplorer
              project={project}
              version={proj.version}
              hostName={hostName}
              selectedDatasetId={cat.selectedId}
              onOpenRun={openRun}
              onImportRun={cat.importRunResult}
              onSelectDataset={selectDataset}
              onEditFile={editor ? (path, hostId) => setEditing({ path, hostId }) : undefined}
              onOverview={() => setMode("look")}
              onImport={() => setMode("import")}
              onCancel={() => void proj.cancel()}
            />
          ) : (
            <div className={s.noProject}>
              <p className={s.empty}>No project open.</p>
              <Button onClick={() => setMode("open")}>Open a project…</Button>
            </div>
          )}
          {project ? <Section key={project.id} title="Everything registered" meta={String(cat.datasets.length + cat.catalogCount)} defaultOpen={false} openSignal={hostOffer?.serial}>{catalog}</Section> : catalog}
        </div>
        <ResizeHandle orientation="vertical" label="Resize explorer" value={L.side} min={LIMITS.side.min} max={LIMITS.side.max} onChange={(v) => set("side", v)} />
      </aside>

      <main id="main-content" tabIndex={-1} className={s.canvas} aria-label="Viewer">
        {mode === "open" || noProjectYet ? (
          <OpenProject recent={proj.recent} hosts={cat.hosts} currentId={project?.id ?? null} busy={proj.opening} error={proj.error} onOpen={(path, hostId) => void openFolder(path, hostId)} />
        ) : mode === "look" && project ? (
          <FirstLook key={project.id} project={project} onWrite={writeFile} onUseWithout={() => setMode("viewer")} onCancel={() => void proj.cancel()} />
        ) : mode === "needs" && project ? (
          <NeedsView project={project} version={proj.version} onChanged={() => { void proj.refresh(project.id); void refreshDatasets(); /* an answer can declare a unit on the files it is about */ }} />
        ) : mode === "import" && project ? (
          <ImportView key={project.id} project={project} version={proj.version} onChanged={() => { void proj.refresh(project.id); void refreshDatasets(); /* registering files and declaring on them changes the catalog */ }} />
        ) : mode === "health" && project ? (
          <HealthView project={project} />
        ) : mode === "jobs" ? (
          <JobsView focusJob={focusJob} hosts={cat.hosts} onImported={() => { void cat.refreshRuns(); void cat.refreshDatasets(); }} onOpenRun={(r) => openRun(r.id)} onOpenFamily={(p) => { setFamilyPreset(p); setMode("family"); }} />
        ) : mode === "run" ? (
          pageRun ? (
            <RunPage key={pageRun.id} run={pageRun} runs={cat.runs} datasets={cat.allDatasets} onEntryChanged={(c) => { void cat.refreshRuns(); void cat.refreshDatasets(); if (c === "deleted") setMode(runFrom); }} job={(pageRun.jobId != null ? cat.jobs.find((j) => j.id === pageRun.jobId) : undefined) ?? jobFor(cat.jobs, pageRun.id)} onBack={() => setMode(runFrom)} onOpenRun={openRun} onOpenInViewer={(id) => { cat.select(id); setMode("viewer"); }} onCompareInViewer={compareInViewer} onRunAgain={runAgain} onDraftedOnCube={(j) => { setFocusJob(j.id); setMode("jobs"); }} />
          ) : (
            <div className={s.stage}>
              <p className={s.stageTitle}>Run not found</p>
              <p className={s.empty}>It may have been removed. <button type="button" onClick={() => setMode(runFrom)}>Back</button></p>
            </div>
          )
        ) : mode === "rerun" ? (
          rerunRun ? (
            <RerunDraft key={rerunRun.id} run={rerunRun} hosts={cat.hosts} onBack={() => setMode("run")} onPrepared={(j) => { setFocusJob(j.id); setMode("jobs"); }} />
          ) : (
            <div className={s.stage}>
              <p className={s.stageTitle}>Run not found</p>
              <p className={s.empty}>It may have been removed. <button type="button" onClick={() => setMode(runFrom)}>Back</button></p>
            </div>
          )
        ) : mode === "runs" ? (
          <RunsView runs={cat.runs} jobs={cat.jobs} hostName={runHostName} state={runsView} onState={patchRunsView} onOpenRun={openRun} />
        ) : mode === "family" ? (
          <FamilyView datasets={cat.allDatasets} preset={familyPreset} onCatalogChanged={() => { void cat.refreshDatasets(); }} />
        ) : mode === "reports" ? (
          <ReportsView onOpenRun={openRun} />
        ) : mode === "boards" ? (
          <BoardsView onCompare={compareInViewer} newer={cat.newer.figures} />
        ) : subsetId !== null && !virtualDs ? (
          <SubsetPage key={subsetId} id={subsetId} onChanged={() => void subs.refresh()} onOpenDataset={(id) => { void cat.refreshDatasets(); selectDataset(id); }} onClose={() => { setSubsetId(null); void subs.refresh(); }} />
        ) : cat.selectedCatalog ? (
          <CatalogView dataset={cat.selectedCatalog} problem={cat.problems[String(cat.selectedCatalog.id)] ? CATALOG_PROBLEM : undefined} />
        ) : shownImage ? (
          <div className={s.stageWrap}>
            {virtualDs ? null : (
              <div className={s.stageTabs} role="tablist" aria-label="Stage view">
                <button type="button" role="tab" aria-selected={stage === "map"} className={s.stageTab} onClick={() => setStage("map")}>Map</button>
                <button type="button" role="tab" aria-selected={stage === "grid"} className={s.stageTab} onClick={() => setStage("grid")}>Channel maps</button>
              </div>
            )}
            {stage === "grid" && !virtualDs ? (
              <ChannelGrid key={shownImage.id} v={viewerA} dataset={shownImage} />
            ) : (
          <div className={s.compare}>
            <div className={s.undoRow}><UndoBar surface="map" undoName={viewer.undoName} redoName={viewer.redoName} onUndo={viewer.undo} onRedo={viewer.redo} /></div>
            {virtualDs ? null : <CompareBar hosts={cat.hosts} datasets={cat.datasets} selectedId={shownImage.id} compareId={compareDs?.id ?? null} onChange={setCompareId} compat={compat} loading={compatLoading} error={compatErr} shared={shared} canShare={canShare} shareReason={shareReason} onShared={toggleShared} link={linkControls} />}
            <div className={compareDs ? s.panes2 : s.panes1}>
              <section className={s.pane} aria-label={`Map: ${shownImage.name}`}>
                {compareDs ? <PaneHeader name={shownImage.name} axial={viewerA.interp !== null} active={!useB} onActivate={() => setActive("a")} note={outsideIn === "a" ? OUTSIDE_NOTE : null} /> : null}
                <div className={s.paneBody}><PlaneViewer v={viewerA} onProbe={(p) => mirror("a", p)} onSize={setPaneA} /></div>
              </section>
              {compareDs ? (
                <section className={s.pane} aria-label={`Map: ${compareDs.name}`}>
                  <PaneHeader name={compareDs.name} axial={viewerB.interp !== null} active={useB} onActivate={() => setActive("b")} note={outsideIn === "b" ? OUTSIDE_NOTE : null} />
                  <div className={s.paneBody}><PlaneViewer v={viewerB} onProbe={(p) => mirror("b", p)} onSize={setPaneB} /></div>
                </section>
              ) : null}
            </div>
          </div>
            )}
          </div>
        ) : (
          <div className={s.stage}>
            <p className={s.stageTitle}>No dataset open</p>
            <p className={s.empty}>Planes and maps appear here. The image keeps its native shape and never stretches.</p>
          </div>
        )}
      </main>

      <aside className={s.insp} aria-label="Inspector" hidden={!inspShown}>
        <ResizeHandle orientation="vertical" direction={-1} label="Resize inspector" value={L.insp} min={LIMITS.insp.min} max={LIMITS.insp.max} onChange={(v) => set("insp", v)} />
        <div className={s.scroll}>
          {cat.selectedCatalog ? (
            <>
              <NewerNotices notices={cat.newer.datasets[String(cat.selectedCatalog.id)]} onOpen={selectDataset} onRegister={(p) => { void cat.register(p); }} />
              <RelativeOrientationResult dataset={cat.selectedCatalog} datasets={cat.allDatasets} />
              <CatalogInspector d={cat.selectedCatalog} problem={cat.problems[String(cat.selectedCatalog.id)] ? CATALOG_PROBLEM : undefined} onDeclare={cat.declareCatalog} onReread={() => { void cat.register(cat.selectedCatalog!.path); }} />
              {cat.selectedCatalog.catalogDeclaration?.angle ? (
                <Section title="Compare orientation" defaultOpen={false}>
                  <RelativeOrientationPanel key={cat.selectedCatalog.id} dataset={cat.selectedCatalog} datasets={cat.allDatasets} onCreated={(d) => { void cat.refreshDatasets().then(() => cat.select(d.id)); }} />
                </Section>
              ) : null}
              <Section title="Nature of the data">
                <NatureControl dataset={cat.selectedCatalog} onChange={cat.declareNature} />
              </Section>
              <Section title="Locations" defaultOpen={false} meta={String(cat.selectedCatalog.locations?.length ?? 1)}>
                <LocationsPanel key={cat.selectedCatalog.id} dataset={cat.selectedCatalog} hosts={cat.hosts} onChanged={() => void cat.refreshDatasets()} />
              </Section>
              <Section title="Archive or delete" defaultOpen={false} meta={cat.selectedCatalog.archived ? "archived" : undefined}>
                <EntryActions key={cat.selectedCatalog.id} kind="datasets" id={cat.selectedCatalog.id} name={cat.selectedCatalog.name} archived={cat.selectedCatalog.archived === true} onChanged={async (c) => { await cat.refreshDatasets(); if (c === "deleted") { void cat.refreshRuns(); cat.select(null); } }} />
              </Section>
            </>
          ) : null}
          {cat.selectedCatalog ? null : virtualDs && actDs?.id === virtualDs.id ? (
            <>
              <Section title="Virtual subset">
                <SubsetPage key={virtualDs.id} id={-virtualDs.id} embedded onChanged={() => void subs.refresh()} onOpenDataset={(id) => { void cat.refreshDatasets(); selectDataset(id); }} onClose={() => { setSubsetId(null); void subs.refresh(); }} />
              </Section>
              <Section title="Coordinates"><CoordinateReadout v={viewer} /></Section>
              <Section title="Display"><DisplayControls v={viewer} /></Section>
              <Section title="Region"><RegionPanel v={viewer} /></Section>
              <Section title="Spectrum"><SpectrumPlot v={viewer} /></Section>
            </>
          ) : !actDs ? <p className={s.empty}>{subsetId !== null ? "A virtual subset is open: its window, history and actions are beside this panel. Choose its source for the viewer's tools." : cat.selectedHealpix ? (
            <Section title="HEALPix map">
              <HealpixSamplePanel key={cat.selectedHealpix.id} dataset={cat.selectedHealpix} datasets={cat.allDatasets} onResult={(r) => { void cat.refreshDatasets().then(() => cat.select(r.dataset.id)); }} />
            </Section>
          ) : "Nothing selected. Choose a dataset in the explorer."}</p> : <>
          {compareDs && shownImage ? (
            <Section title="Comparison">
              <CompareInspector a={shownImage} b={compareDs} va={viewerA} vb={viewerB} runs={cat.runs} compat={compat} onCreated={(d) => { void cat.refreshDatasets().then(() => { setCompareId(null); cat.select(d.id); }); }} />
            </Section>
          ) : null}
          {actDs && runFor(cat.runs, actDs.id) ? (
            <Section title="Run">
              <RunPanel run={runFor(cat.runs, actDs.id)!.run} role={runFor(cat.runs, actDs.id)!.role} product={runFor(cat.runs, actDs.id)!.product} job={jobFor(cat.jobs, runFor(cat.runs, actDs.id)!.run.id)} orientationUnresolved={runFor(cat.runs, actDs.id)!.role === "orientation" && !actDs.interpretation} onOpenPage={() => openRun(runFor(cat.runs, actDs.id)!.run.id)} onRunAgain={() => runAgain(runFor(cat.runs, actDs.id)!.run.id)} />
            </Section>
          ) : null}
          {actDs && !runFor(cat.runs, actDs.id) && cat.runs.some((r) => r.kind === "vgt" && r.inputHint?.datasetId === actDs.id) ? (
            <Section title="Runs that used a file like this">
              <RunsUsingFile runs={cat.runs} datasetId={actDs.id} onRunAgain={runAgain} />
            </Section>
          ) : null}
          <Section title="Coordinates">
            {actDs ? <CoordinateReadout v={viewer} other={readoutOther} /> : <p className={s.empty}>{EM}</p>}
          </Section>
          {actDs && (viewer.view === null || viewer.view.channels === 1) ? (
            <Section title="Interpretation">
              <InterpretationPanel key={actDs.id} v={viewer} dataset={actDs} datasets={cat.datasets} declare={cat.declare} applyActions={cat.applyActions} />
            </Section>
          ) : null}
          {actDs ? <HealpixSampleDone dataset={actDs} datasets={cat.allDatasets} onOpen={(d) => { void cat.refreshDatasets().then(() => cat.select(d.id)); }} /> : null}
          {actDs ? <RelativeOrientationResult dataset={actDs} datasets={cat.allDatasets} /> : null}
          {viewer.interp ? (
            <Section title="Orientation">
              <OrientationStats v={viewer} />
            </Section>
          ) : null}
          {actDs && actDs.interpretation?.kind === "axial_orientation" ? (
            <Section title="Compare orientation" defaultOpen={false}>
              <RelativeOrientationPanel key={actDs.id} dataset={actDs} datasets={cat.allDatasets} onCreated={(d) => { void cat.refreshDatasets().then(() => cat.select(d.id)); }} />
            </Section>
          ) : null}
          <Section title="Sky" defaultOpen={false} onToggle={setSkyOpen}>
            {skyOpen ? <SkyPanel datasets={cat.allDatasets} subsets={subs.subsets} datasetId={actDs ? actDs.id : subsetId !== null ? virtualId(subsetId) : null} probe={viewer.probe} onOpen={(id) => { if (id < 0) openSubset(virtualId(id)); else { setSubsetId(null); setMode("viewer"); cat.select(id); } }} /> : null}
          </Section>
          <Section title="Display">
            {actDs ? <DisplayControls v={viewer} /> : <p className={s.empty}>{EM}</p>}
          </Section>
          <Section title="Overlays" meta={viewer.scene ? String(viewer.scene.overlays.length) : undefined}>
            {actDs ? <OverlayPanel scene={viewer.scene} dispatch={viewer.dispatch} geometry={viewer.geometry} datasets={cat.allDatasets} picked={viewer.picked} onPick={viewer.setPicked} /> : <p className={s.empty}>{EM}</p>}
          </Section>
          <Section title="Region">
            {actDs ? <RegionPanel v={viewer} /> : <p className={s.empty}>{EM}</p>}
          </Section>
          <Section title="Spectrum">
            {actDs ? <><SpectrumPlot v={viewer} /><GaussianComponents v={viewer} /></> : <p className={s.empty}>{EM}</p>}
          </Section>
          <Section title="History" defaultOpen={false} onToggle={setHistoryOpen} meta={hist.steps.length > 0 ? String(hist.steps.length) : undefined}>
            {!historyOpen ? null : actDs ? <HistoryPanel steps={hist.steps} keep={hist.keep} error={hist.error} onContinue={hist.continueFrom} /> : <p className={s.empty}>{EM}</p>}
          </Section>
          <Section title="Position-velocity" defaultOpen={false} meta={viewer.path ? "line drawn" : undefined}>
            {actDs ? <PvPanel v={viewer} /> : <p className={s.empty}>{EM}</p>}
          </Section>
          {actDs ? (
            <Section title="Made from" defaultOpen={false}>
              <DerivedPanel dataset={actDs} datasets={cat.datasets} />
            </Section>
          ) : null}
          {actDs && !fileStatesItsUnit(actDs) && !actDs.interpretation ? (
            <Section title="Unit of the values">
              <UnitControl key={actDs.id} dataset={actDs} onDeclare={cat.declareUnit} onWithdraw={(id) => cat.declareUnit(id, null)} />
            </Section>
          ) : null}
          {actDs && isImage(actDs) ? (
            <Section title="Masks" defaultOpen={false}>
              <MasksPanel key={actDs.id} dataset={actDs} datasets={cat.datasets} />
            </Section>
          ) : null}
          {actDs ? (
            <Section title="Nature of the data">
              <NatureControl dataset={actDs} onChange={cat.declareNature} />
            </Section>
          ) : null}
          {actDs ? (
            <Section title="Access" defaultOpen={access.labels[String(actDs.id)] !== undefined} meta={access.labels[String(actDs.id)] === "unreleased" ? "unreleased" : access.labels[String(actDs.id)] === "collaboration" ? "collaboration only" : undefined}>
              <AccessPanel key={actDs.id} id={actDs.id} onChanged={access.refresh} />
            </Section>
          ) : null}
          {actDs ? (
            <Section title="Storage" defaultOpen={false}>
              <StorageControl dataset={actDs} onCopied={(d) => { void cat.refreshDatasets().then(() => cat.select(d.id)); }} />
            </Section>
          ) : null}
          {actDs ? (
            <Section title="Offline" defaultOpen={false} meta={actDs.hostId !== 0 ? undefined : "this computer"}>
              <DatasetOffline key={actDs.id} id={actDs.id} name={actDs.name} hostId={actDs.hostId} />
            </Section>
          ) : null}
          {actDs ? (
            <Section title="Archive or delete" defaultOpen={false} meta={actDs.archived ? "archived" : undefined}>
              <EntryActions key={actDs.id} kind="datasets" id={actDs.id} name={actDs.name} archived={actDs.archived === true} onChanged={async (c) => { await cat.refreshDatasets(); if (c === "deleted") { void cat.refreshRuns(); cat.select(null); } }} />
            </Section>
          ) : null}
          {actDs ? (
            <Section title="Used in" defaultOpen={false}>
              <UsedIn dataset={actDs} onOpenBoards={() => setMode("boards")} />
            </Section>
          ) : null}
          {actDs && isImage(actDs) ? (
            <Section title="Virtual subsets" defaultOpen={false} meta={subs.subsets.some((x) => x.sourceId === actDs.id) ? String(subs.subsets.filter((x) => x.sourceId === actDs.id).length) : undefined}>
              <SubsetsSection key={actDs.id} dataset={actDs} subsets={subs.subsets} onOpen={openSubset} onCreated={(x) => { void subs.refresh(); openSubset(x.id); }} />
            </Section>
          ) : null}
          {actDs ? (
            <Section title="Analysis" defaultOpen={false}>
              <AnalysisPanel dataset={actDs} datasets={cat.datasets} channels={viewer.view?.channels ?? 1} onCreated={(d) => { void cat.refreshDatasets().then(() => cat.select(d.id)); }} onJob={(j) => { setFocusJob(j.id); setMode("jobs"); }} />
            </Section>
          ) : null}
          <Section title="Figure" defaultOpen={false}>
            {actDs ? <FigurePanel v={viewer} dataset={actDs} datasets={cat.allDatasets} /> : <p className={s.empty}>{EM}</p>}
          </Section>
          <Section title="Dataset">
            {actDs ? (
              <>
                <NewerNotices notices={cat.newer.datasets[String(actDs.id)]} onOpen={selectDataset} onRegister={(p) => { void cat.register(p); }} />
                <DatasetInspector d={actDs} problem={cat.problems[String(actDs.id)]} onReread={() => { void cat.register(actDs.path); }} />
                <CorrectionsPanel key={actDs.id} dataset={actDs} onChanged={() => void cat.refreshDatasets()} />
              </>
            ) : <p className={s.empty}>Nothing selected. Unknown values show as —.</p>}
          </Section>
          {actDs ? (
            <Section title="Locations" defaultOpen={false} meta={String(actDs.locations?.length ?? 1)}>
              <LocationsPanel key={actDs.id} dataset={actDs} hosts={cat.hosts} onChanged={() => void cat.refreshDatasets()} />
            </Section>
          ) : null}
          </>}
        </div>
      </aside>

      <section className={s.timeline} aria-label="Channels" hidden={!timelineShown}>
        <ResizeHandle orientation="horizontal" direction={-1} label="Resize timeline" value={L.timeline} min={LIMITS.timeline.min} max={LIMITS.timeline.max} onChange={(v) => set("timeline", v)} />
        <div className={s.scroll}>
          {actDs ? <ChannelTimeline v={viewer} /> : <p className={s.empty}>{cat.selectedCatalog ? "A catalog has rows, not channels." : "No spectral axis. Channels appear here once a cube is open."}</p>}
        </div>
      </section>

      <footer className={s.status}>
        <IconButton icon={PanelLeft} label="Toggle explorer" pressed={L.sideOpen} onClick={() => toggle("sideOpen")} />
        <IconButton icon={PanelBottom} label="Toggle timeline" pressed={L.timelineOpen} onClick={() => toggle("timelineOpen")} />
        <IconButton icon={PanelRight} label="Toggle inspector" pressed={L.inspOpen} onClick={() => toggle("inspOpen")} />
        <ErrorReferenceHost />
        <span className={s.grow} />
        {rendererNote ? <Pill>{rendererNote}</Pill> : null}
        <Pill tone={actDs ? flag.tone : "neutral"}>{actDs ? (viewer.view ? flag.text : "Reading layout") : cat.selectedCatalog ? cat.selectedCatalog.name : "No dataset"}</Pill>
      </footer>
      {supportOpen ? <Suspense fallback={null}><SupportDialog onClose={() => setSupportOpen(false)} /></Suspense> : null}
      {terminal && terminalOpen ? (
        <Suspense fallback={null}>
          <TerminalScreen hosts={cat.hosts} onClose={() => setTerminalOpen(false)} />
        </Suspense>
      ) : null}
      {editor && editing ? (
        <Suspense fallback={null}>
          <EditorScreen key={`${editing.hostId}:${editing.path}`} path={editing.path} host={editing.hostId > 0 ? { id: editing.hostId, name: hostName(editing.hostId) } : undefined} onClose={() => setEditing(null)} />
        </Suspense>
      ) : null}
    </div>
  );
}

const OUTSIDE_NOTE = "Cursor is outside this map's footprint";

function PaneHeader({ name, axial, active, onActivate, note = null }: { name: string; axial: boolean; active: boolean; onActivate: () => void; note?: string | null }) {
  return (
    <div className={s.paneHead}>
      <span className={s.paneName} title={name}>{name}</span>
      <Pill>{axial ? "Axial orientation" : "Scalar"}</Pill>
      {note ? <span className={s.paneNote} role="status">{note}</span> : null}
      <button type="button" className={s.paneBtn} aria-pressed={active} onClick={onActivate}>{active ? "Inspector shows this map" : "Show in inspector"}</button>
    </div>
  );
}

