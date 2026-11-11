import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from "react";
import { ApiError } from "../api/catalog";
import { expandPreview, getAxial, getConversions, getConvert, getCoord, getPlane, getSpectrum, getView, previewStep } from "../api/planes";
import type { AxialStats, Conversions, Converted, Coord, Plane, RegionShape, Spectrum, View } from "../api/planes";
import type { Dataset } from "../api/catalog";
import type { Tool } from "./region";
import { channelForKey } from "./geometry";
import { useSavedRegions } from "./useSavedRegions";
import { toShape } from "../api/regions";
import type { ColormapId } from "./colormaps";
import type { RangeMode, Stretch } from "./render";
import { initialPane, orientationOf, paneReducer, regionsOf, ORIENTATION_ID, REGIONS_ID } from "../scene/scene";
import type { PaneAction, ScenePoint } from "../scene/scene";

export type Pixel = { x: number; y: number };

/** What the readout shows besides the native values; empty text means native only. The convention is the person's choice and is
 *  only sent with a velocity asked of a frequency or wavelength axis. */
export type ConvChoice = { spectral: string; convention: string; frame: string; to: string };
export const NO_CONV: ConvChoice = { spectral: "", convention: "radio", frame: "", to: "" };

const isAbort = (e: unknown) => e instanceof DOMException && e.name === "AbortError";
const message = (e: unknown, fallback: string) => (e instanceof ApiError ? e.message : fallback);

/**
 * Everything the viewer panels share for one open dataset. Pixel and channel indices are 0-based here.
 * What the pane draws is its Scene (scene/scene.ts): channel, display, overlays and view, changed only through dispatch with
 * serialisable actions. Everything else here is ephemeral: pointer, pinned pixel, tool, server replies, loading and errors.
 * The setters are thin wrappers over dispatch, so the panels read as before.
 */
export function useViewer(dataset: Dataset | null) {
  const datasetId = dataset?.id ?? null;
  const fileTime = dataset?.modifiedAt ?? null; // a file that was read again starts the viewer over
  const interp = dataset?.interpretation ?? null;
  const relId = interp?.reliabilityId ?? null;
  const unit = interp?.unit ?? null;
  // the unit you declared for a file without BUNIT, as the server applies it (a stale declaration is not applied)
  const unitKey = dataset?.unitDeclaration && !dataset.unitDeclaration.stale ? dataset.unitDeclaration.unit : "";
  const seenUnit = useRef<{ id: number | null; key: string }>({ id: datasetId, key: unitKey });
  const canvas = useRef<HTMLCanvasElement>(null);
  const [view, setView] = useState<View | null>(null);
  const [viewError, setViewError] = useState<string | null>(null);
  const [plane, setPlane] = useState<Plane | null>(null);
  const [planeError, setPlaneError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [retry, setRetry] = useState(0);
  const [pane, dispatch] = useReducer(paneReducer, null, () => initialPane(datasetId, interp !== null));
  const scene = pane.scene;
  const [hover, setHover] = useState<Pixel | null>(null);
  const [pinned, setPinned] = useState<Pixel | null>(null);
  // the pixel under the cursor in the linked pane, when the two maps share a grid
  const [mirror, setMirror] = useState<Pixel | null>(null);
  const [coord, setCoord] = useState<Coord | null>(null);
  const [coordError, setCoordError] = useState<string | null>(null);
  const [conv, setConv] = useState<ConvChoice>(NO_CONV);
  const [offers, setOffers] = useState<Conversions | null>(null);
  const [converted, setConverted] = useState<{ key: string; r: Converted } | null>(null);
  const [convError, setConvError] = useState<string | null>(null);
  const [tool, setTool] = useState<Tool>("pointer");
  const [spectrum, setSpectrum] = useState<Spectrum | null>(null);
  const [spectrumError, setSpectrumError] = useState<string | null>(null);
  const [spectrumLoading, setSpectrumLoading] = useState(false);
  const [reliability, setReliability] = useState<Plane | null>(null);
  const [axial, setAxial] = useState<AxialStats | null>(null);
  const [axialError, setAxialError] = useState<string | null>(null);

  // the scene, read the way the panels always have
  const display = scene?.base.display ?? pane.keep.display;
  const channel = scene?.base.channel ?? 0;
  const colormap = display.colormap as ColormapId;
  const stretch = display.stretch as Stretch;
  const range = display.range as RangeMode;
  const customLimits = (display.limits as [number, number] | undefined) ?? null;
  const region: RegionShape | null = regionsOf(scene)?.active ?? null; // keeps its identity across unrelated actions
  const showLines = orientationOf(scene)?.visible ?? pane.keep.linesVisible;
  const zoom = scene?.view.zoom ?? 1; // 1 = the picture fitted to its pane

  const setChannel = useCallback((c: number) => dispatch({ type: "setChannel", channel: c }), []);
  const setColormap = useCallback((c: ColormapId) => dispatch({ type: "setDisplay", display: { colormap: c } }), []);
  const setStretch = useCallback((x: Stretch) => dispatch({ type: "setDisplay", display: { stretch: x } }), []);
  const setRange = useCallback((r: RangeMode) => dispatch({ type: "setDisplay", display: { range: r } }), []);
  const setCustomLimits = useCallback((l: [number, number] | null) => dispatch({ type: "setDisplay", display: { limits: l } }), []);
  const setScaleGroup = useCallback((g: string | null) => dispatch({ type: "setDisplay", display: { scaleGroup: g } }), []);
  const setRegion = useCallback((r: RegionShape | null) => dispatch({ type: "updateOverlay", id: REGIONS_ID, patch: { active: r } }), []);
  const setShowLines = useCallback((on: boolean) => dispatch({ type: "updateOverlay", id: ORIENTATION_ID, patch: { visible: on } }), []);
  const setZoom = useCallback((z: number) => dispatch({ type: "setView", view: { zoom: z } }), []);
  const setCentre = useCallback((c: ScenePoint | null) => dispatch({ type: "setView", view: { centre: c } }), []);
  const savedRegions = useSavedRegions(datasetId, setRegion);

  // The declaration can change while the map is open (declared axial, or withdrawn); the scene's mapping and its own
  // orientation lines follow it.
  const isAxial = interp !== null;
  useEffect(() => dispatch({ type: "syncAxial", axial: isAxial }), [isAxial]);

  // Saved regions are server rows. The scene keeps the shapes it draws (the shown ones, the selected one emphasised), so
  // a scene says what is on screen without a lookup.
  const { saved: savedList, hidden: hiddenSaved, selectedId: selectedSaved } = savedRegions;
  const sceneDataset = scene?.base.datasetId ?? null;
  useEffect(() => {
    const saved = savedList.filter((r) => !hiddenSaved.has(r.id)).map((r) => ({ id: r.id, shape: toShape(r), ...(r.id === selectedSaved ? { selected: true } : {}) }));
    dispatch({ type: "updateOverlay", id: REGIONS_ID, patch: { saved } });
  }, [savedList, hiddenSaved, selectedSaved, sceneDataset]);

  // A different dataset: forget everything that belonged to the previous one.
  useEffect(() => {
    setView(null);
    setViewError(null);
    setPlane(null);
    setPlaneError(null);
    dispatch({ type: "resetForDataset", datasetId, axial: interp !== null }); // channel 0, fitted, no region; colours carry over
    setHover(null);
    setPinned(null);
    setCoord(null);
    setConv(NO_CONV);
    setOffers(null);
    setConverted(null);
    setSpectrum(null);
    setTool("pointer");
    if (datasetId === null) return;
    const ac = new AbortController();
    getView(datasetId, ac.signal)
      .then(setView)
      .catch((e: unknown) => !isAbort(e) && setViewError(message(e, "Could not read the dataset layout.")));
    return () => ac.abort();
  }, [datasetId, retry, fileTime]); // eslint-disable-line react-hooks/exhaustive-deps

  // Declaring or withdrawing a unit changes what every head says. Read the layout again (the plane and the spectrum follow the
  // new view) without starting over: the channel and the region stay. A different dataset starts over by itself, above.
  useEffect(() => {
    const prev = seenUnit.current;
    seenUnit.current = { id: datasetId, key: unitKey };
    if (datasetId === null || prev.id !== datasetId || prev.key === unitKey) return;
    const ac = new AbortController();
    getView(datasetId, ac.signal)
      .then(setView)
      .catch((e: unknown) => !isAbort(e) && setViewError(message(e, "Could not read the dataset layout.")));
    return () => ac.abort();
  }, [datasetId, unitKey]);

  // The plane for the selected channel. A newer request aborts the older one, so scrubbing never queues stale reads.
  // A large plane on a remote host shows a reduced preview first and the full plane once the channel has settled, so
  // scrubbing over a slow link stays responsive; the preview is labelled as one. After the full plane arrives the
  // neighbouring channels are requested quietly so the next step is already cached by the server.
  const lastStep = useRef(1);
  const remote = (dataset?.hostId ?? 0) !== 0;
  useEffect(() => {
    if (datasetId === null || !view) return;
    const ac = new AbortController();
    setLoading(true);
    const shown = (p: Plane) => {
      setPlane(p);
      setPlaneError(null);
    };
    const failed = (e: unknown) => {
      if (isAbort(e)) return;
      setPlane(null); // never keep showing another channel's pixels under an error
      setPlaneError(message(e, "Could not read this plane."));
      setLoading(false);
    };
    const step = remote && !isAxial ? previewStep(view.width, view.height) : 1; // block means of angles are wrong, so orientation maps load whole
    let settled = false; // the full request has answered, with a plane or an error: a late preview must change nothing
    let settle: ReturnType<typeof setTimeout> | undefined;
    const full = () =>
      getPlane(datasetId, channel, ac.signal)
        .then((p) => {
          settled = true;
          shown(p);
          setLoading(false);
          const dir = Math.sign(channel - lastStep.current) || 1;
          lastStep.current = channel;
          for (const c of [channel + dir, channel - dir]) {
            if (c >= 0 && c < view.channels) void getPlane(datasetId, c, ac.signal).catch(() => undefined);
          }
        })
        .catch((e: unknown) => {
          settled = true;
          failed(e);
        });
    if (step > 1 && view.channels > 1) {
      getPlane(datasetId, channel, ac.signal, step)
        .then((p) => !settled && shown(expandPreview(p)))
        .catch((e: unknown) => !settled && failed(e)); // a preview that fails must not wipe a full plane or replace its error
      settle = setTimeout(full, 150);
    } else {
      void full();
    }
    return () => {
      clearTimeout(settle);
      ac.abort();
    };
  }, [datasetId, view, channel, retry, remote, isAxial]);

  const probe = pinned ?? hover ?? mirror;
  const shownChannel = plane?.head.channel ?? null;

  // The scene records the shown channel's spectral world value as the worker reported it, never one computed here.
  useEffect(() => {
    if (!plane || plane.head.preview) return;
    const v = plane.head.spectral_value;
    dispatch({ type: "setSpectral", channel: plane.head.channel, spectral: v !== null ? { value: v, unit: plane.head.spectral_unit ?? "" } : null });
  }, [plane]);

  // World coordinates come from the worker (astropy), for the pixel under the pointer in the plane on screen.
  useEffect(() => {
    if (datasetId === null || !probe || shownChannel === null) {
      setCoord(null);
      setCoordError(null);
      return;
    }
    const ac = new AbortController();
    const t = setTimeout(() => {
      getCoord(datasetId, probe.x, probe.y, shownChannel, ac.signal)
        .then((c) => {
          setCoord(c);
          setCoordError(null);
        })
        .catch((e: unknown) => {
          if (isAbort(e)) return;
          setCoord(null);
          setCoordError(message(e, "Could not compute coordinates."));
        });
    }, 40);
    return () => {
      clearTimeout(t);
      ac.abort();
    };
  }, [datasetId, probe?.x, probe?.y, shownChannel]); // eslint-disable-line react-hooks/exhaustive-deps

  // What the file allows (spectral units, sky frames, brightness units) is read again whenever the layout is: a declared unit changes it.
  useEffect(() => {
    if (datasetId === null || !view) {
      setOffers(null);
      return;
    }
    const ac = new AbortController();
    getConversions(datasetId, ac.signal)
      .then((o) => setOffers(o && typeof o === "object" && "value" in o ? o : null))
      .catch((e: unknown) => !isAbort(e) && setOffers(null));
    return () => ac.abort();
  }, [datasetId, view]);

  // A choice the file no longer allows (the unit was withdrawn, say) is dropped, never kept silently.
  useEffect(() => {
    if (!offers) return;
    const ok = (list: { unit: string; enabled: boolean }[] | undefined, v: string) => v === "" || !!list?.some((t) => t.unit === v && t.enabled);
    const frameOk = (v: string) => v === "" || !!offers.sky?.targets.some((t) => t.id === v && t.enabled);
    setConv((c) => {
      const next = { ...c, spectral: ok(offers.spectral?.targets, c.spectral) ? c.spectral : "", to: ok(offers.value.targets, c.to) ? c.to : "", frame: frameOk(c.frame) ? c.frame : "" };
      return next.spectral === c.spectral && next.to === c.to && next.frame === c.frame ? c : next;
    });
  }, [offers]);

  const pixelNumber = useMemo(() => {
    if (!plane || !probe || plane.head.preview) return null; // a reduced preview holds block means, not pixel values
    const { width, height } = plane.head;
    if (probe.x >= width || probe.y >= height) return null;
    return plane.data[probe.y * width + probe.x] ?? null;
  }, [plane, probe]);
  const convWanted = conv.spectral !== "" || conv.frame !== "" || conv.to !== "";
  const convValue = conv.to !== "" && pixelNumber !== null && Number.isFinite(pixelNumber) ? pixelNumber : null;
  // a conversion is shown only for the pixel, channel and choices it was computed for
  const convKey = probe && shownChannel !== null ? [probe.x, probe.y, shownChannel, conv.spectral, conv.spectral ? conv.convention : "", conv.frame, conv.to, convValue].join("|") : null;

  useEffect(() => {
    if (datasetId === null || !probe || shownChannel === null || !convWanted || convKey === null) {
      setConverted(null);
      setConvError(null);
      return;
    }
    const ac = new AbortController();
    const t = setTimeout(() => {
      getConvert(datasetId, probe.x, probe.y, shownChannel, { ...conv, value: convValue }, ac.signal)
        .then((r) => {
          setConverted({ key: convKey, r });
          setConvError(null);
        })
        .catch((e: unknown) => {
          if (isAbort(e)) return;
          setConverted(null);
          setConvError(message(e, "Could not convert."));
        });
    }, 40);
    return () => {
      clearTimeout(t);
      ac.abort();
    };
  }, [datasetId, convKey, convWanted]); // eslint-disable-line react-hooks/exhaustive-deps

  // The mean spectrum of the region, from the worker. A newer region aborts the older request.
  useEffect(() => {
    if (datasetId === null || !view || !region) {
      setSpectrum(null);
      setSpectrumError(null);
      setSpectrumLoading(false);
      return;
    }
    const ac = new AbortController();
    setSpectrumLoading(true);
    const t = setTimeout(() => {
      getSpectrum(datasetId, region, ac.signal)
        .then((sp) => {
          setSpectrum(sp);
          setSpectrumError(null);
          setSpectrumLoading(false);
        })
        .catch((e: unknown) => {
          if (isAbort(e)) return;
          setSpectrum(null);
          setSpectrumError(message(e, "Could not compute the spectrum."));
          setSpectrumLoading(false);
        });
    }, 120);
    return () => {
      clearTimeout(t);
      ac.abort();
    };
  }, [datasetId, view, region]);

  // The paired reliability map (one plane), for readouts and line opacity.
  useEffect(() => {
    setReliability(null);
    if (datasetId === null || !view || relId === null) return;
    const ac = new AbortController();
    getPlane(relId, 0, ac.signal)
      .then(setReliability)
      .catch((e: unknown) => !isAbort(e) && setReliability(null));
    return () => ac.abort();
  }, [datasetId, view, relId]);

  // Orientation statistics (doubled-angle) for the whole map or the region, once the user has declared the map axial.
  useEffect(() => {
    if (datasetId === null || !view || !interp) {
      setAxial(null);
      setAxialError(null);
      return;
    }
    const ac = new AbortController();
    const t = setTimeout(() => {
      getAxial(datasetId, region, ac.signal)
        .then((r) => {
          setAxial(r.stats);
          setAxialError(null);
        })
        .catch((e: unknown) => {
          if (isAbort(e)) return;
          setAxial(null);
          setAxialError(message(e, "Could not compute orientation statistics."));
        });
    }, 120);
    return () => {
      clearTimeout(t);
      ac.abort();
    };
  }, [datasetId, view, interp?.kind, unit, relId, region]); // eslint-disable-line react-hooks/exhaustive-deps

  const channels = view?.channels ?? 0;
  const onChannelKey = useCallback(
    (key: string): boolean => {
      const next = channelForKey(key, channel, channels);
      if (next === null) return false;
      setChannel(next);
      return true;
    },
    [channel, channels],
  );

  const pixelValue = pixelNumber;

  return {
    scene, dispatch: dispatch as (a: PaneAction) => void, datasetId, view, viewError, channel, setChannel, channels, plane, planeError, loading, shownChannel,
    zoom, setZoom, setCentre, colormap, setColormap, stretch, setStretch, range, setRange, customLimits, setCustomLimits, setScaleGroup,
    hover, setHover, mirror, setMirror, pinned, setPinned, probe, pixelValue, coord, coordError,
    conv, setConv, offers, convError, convertedNow: converted && converted.key === convKey ? converted.r : null,
    canvas, interp, reliability, axial, axialError, showLines, setShowLines,
    tool, setTool, region, setRegion, savedRegions, spectrum, spectrumError, spectrumLoading,
    onChannelKey, retry: () => setRetry((n) => n + 1),
  };
}

export type Viewer = ReturnType<typeof useViewer>;
