import { useCallback, useEffect, useMemo, useState } from "react";
import { ApiError } from "../api/catalog";
import { getAxial, getCoord, getPlane, getSpectrum, getView } from "../api/planes";
import type { AxialStats, Coord, Plane, RegionShape, Spectrum, View } from "../api/planes";
import type { Dataset } from "../api/catalog";
import type { Tool } from "./region";
import { channelForKey } from "./geometry";
import type { ColormapId } from "./colormaps";
import type { RangeMode, Stretch } from "./render";

export type Pixel = { x: number; y: number };

const isAbort = (e: unknown) => e instanceof DOMException && e.name === "AbortError";
const message = (e: unknown, fallback: string) => (e instanceof ApiError ? e.message : fallback);

/** Everything the viewer panels share for one open dataset. Pixel and channel indices are 0-based here. */
export function useViewer(dataset: Dataset | null) {
  const datasetId = dataset?.id ?? null;
  const interp = dataset?.interpretation ?? null;
  const relId = interp?.reliabilityId ?? null;
  const unit = interp?.unit ?? null;
  const [view, setView] = useState<View | null>(null);
  const [viewError, setViewError] = useState<string | null>(null);
  const [channel, setChannel] = useState(0);
  const [plane, setPlane] = useState<Plane | null>(null);
  const [planeError, setPlaneError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [retry, setRetry] = useState(0);
  const [colormap, setColormap] = useState<ColormapId>("viridis");
  const [stretch, setStretch] = useState<Stretch>("linear");
  const [range, setRange] = useState<RangeMode>("percentile");
  const [hover, setHover] = useState<Pixel | null>(null);
  const [pinned, setPinned] = useState<Pixel | null>(null);
  const [coord, setCoord] = useState<Coord | null>(null);
  const [coordError, setCoordError] = useState<string | null>(null);
  const [tool, setTool] = useState<Tool>("pointer");
  const [region, setRegion] = useState<RegionShape | null>(null);
  const [spectrum, setSpectrum] = useState<Spectrum | null>(null);
  const [spectrumError, setSpectrumError] = useState<string | null>(null);
  const [spectrumLoading, setSpectrumLoading] = useState(false);
  const [reliability, setReliability] = useState<Plane | null>(null);
  const [axial, setAxial] = useState<AxialStats | null>(null);
  const [axialError, setAxialError] = useState<string | null>(null);
  const [showLines, setShowLines] = useState(true);

  // A different dataset: forget everything that belonged to the previous one.
  useEffect(() => {
    setView(null);
    setViewError(null);
    setPlane(null);
    setPlaneError(null);
    setChannel(0);
    setHover(null);
    setPinned(null);
    setCoord(null);
    setRegion(null);
    setSpectrum(null);
    setTool("pointer");
    if (datasetId === null) return;
    const ac = new AbortController();
    getView(datasetId, ac.signal)
      .then(setView)
      .catch((e: unknown) => !isAbort(e) && setViewError(message(e, "Could not read the dataset layout.")));
    return () => ac.abort();
  }, [datasetId, retry]);

  // The plane for the selected channel. A newer request aborts the older one, so scrubbing never queues stale reads.
  useEffect(() => {
    if (datasetId === null || !view) return;
    const ac = new AbortController();
    setLoading(true);
    getPlane(datasetId, channel, ac.signal)
      .then((p) => {
        setPlane(p);
        setPlaneError(null);
        setLoading(false);
      })
      .catch((e: unknown) => {
        if (isAbort(e)) return;
        setPlane(null); // never keep showing another channel's pixels under an error
        setPlaneError(message(e, "Could not read this plane."));
        setLoading(false);
      });
    return () => ac.abort();
  }, [datasetId, view, channel, retry]);

  const probe = pinned ?? hover;
  const shownChannel = plane?.head.channel ?? null;

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

  const pixelValue = useMemo(() => {
    if (!plane || !probe) return null;
    const { width, height } = plane.head;
    if (probe.x >= width || probe.y >= height) return null;
    return plane.data[probe.y * width + probe.x] ?? null;
  }, [plane, probe]);

  return {
    datasetId, view, viewError, channel, setChannel, channels, plane, planeError, loading, shownChannel,
    colormap, setColormap, stretch, setStretch, range, setRange,
    hover, setHover, pinned, setPinned, probe, pixelValue, coord, coordError,
    interp, reliability, axial, axialError, showLines, setShowLines,
    tool, setTool, region, setRegion, spectrum, spectrumError, spectrumLoading,
    onChannelKey, retry: () => setRetry((n) => n + 1),
  };
}

export type Viewer = ReturnType<typeof useViewer>;
