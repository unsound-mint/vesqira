import { act, render, renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Dataset } from "../api/catalog";
import type { Conversions, Converted } from "../api/planes";
import { CoordinateReadout } from "./Panels";
import { NO_CONV, useViewer } from "./useViewer";
import type { Viewer } from "./useViewer";

afterEach(() => vi.unstubAllGlobals());

const offers: Conversions = {
  spectral: {
    native_unit: "Hz", native_kind: "freq", ctype: "FREQ", file_convention: null, specsys: "LSRK", rest: null,
    reference_frame_note: "The reference frame of the axis is the file's and is not changed; a frame change such as TOPOCENT to LSRK is not offered.",
    targets: [
      { unit: "GHz", kind: "freq", enabled: true, reason: null, needs_convention: false },
      { unit: "km/s", kind: "vel", enabled: false, reason: "velocity needs a rest frequency or wavelength from the file (RESTFRQ, RESTFREQ, RESTWAV); there is none", needs_convention: false },
    ],
  },
  sky: { frame: null, frame_status: "assumed", source: "ICRS", refused: null, targets: [{ id: "icrs", label: "ICRS", enabled: true, reason: null }, { id: "galactic", label: "Galactic", enabled: true, reason: null }] },
  value: {
    unit: "Jy/beam", unit_status: "user", reason: null,
    targets: [
      { unit: "mJy/beam", enabled: true, reason: null, cross: false },
      { unit: "K", enabled: false, reason: "the header has no complete beam (BMAJ and BMIN)", cross: true },
    ],
  },
};

const base = (over: object = {}) => ({
  view: { bunit: "Jy/beam", bunit_status: "user", sky: { kind: "celestial", lon_name: "RA", lat_name: "Dec", frame: null, frame_status: "unresolved" }, spectral: null },
  plane: { head: { channel: 0, spectral_value: null } },
  probe: { x: 3, y: 4 },
  pixelValue: 2,
  coord: { x: 3, y: 4, channel: 0, sky: { lon: 150.5, lat: 2.5 }, spectral: null },
  conv: NO_CONV,
  setConv: () => {},
  offers,
  convError: null,
  convertedNow: null,
  ...over,
}) as unknown as Viewer;

describe("converted readout", () => {
  it("says the frame is assumed when the header declares none, and keeps the native values listed", () => {
    render(<CoordinateReadout v={base()} />);
    expect(screen.getByText(/ICRS \(assumed\)/)).toBeInTheDocument();
    expect(screen.getByText(/The header declares no celestial frame, so the FITS standard default applies/)).toBeInTheDocument(); // in the Basis
    expect(screen.getByText("150.50000°")).toBeInTheDocument();
    expect(screen.getByText("2 Jy/beam")).toBeInTheDocument();
  });

  it("offers only what the file allows and writes out why the rest is not available", () => {
    render(<CoordinateReadout v={base()} />);
    const spectral = screen.getByLabelText("Spectral unit") as HTMLSelectElement;
    expect(Array.from(spectral.options).map((o) => [o.value, o.disabled])).toEqual([["", false], ["GHz", false], ["km/s", true]]);
    expect(screen.getByText(/velocity needs a rest frequency or wavelength from the file/)).toBeInTheDocument();
    expect(screen.getByText(/a frame change such as TOPOCENT to LSRK is not offered/)).toBeInTheDocument();
    const value = screen.getByLabelText("Value unit") as HTMLSelectElement;
    expect(value.options[0]?.textContent).toBe("Native (Jy/beam (declared by you))");
    expect(Array.from(value.options).find((o) => o.value === "K")?.disabled).toBe(true);
    expect(screen.getByText(/the header has no complete beam/)).toBeInTheDocument();
  });

  it("asks for the velocity convention only for a velocity of a frequency axis and labels it as the person's choice", async () => {
    const setConv = vi.fn();
    const withVelocity: Conversions = { ...offers, spectral: { ...offers.spectral!, rest: { value: 1.42e9, unit: "Hz", source: "RESTFRQ" }, targets: [{ unit: "km/s", kind: "vel", enabled: true, reason: null, needs_convention: true }] } };
    const { rerender } = render(<CoordinateReadout v={base({ offers: withVelocity, conv: { ...NO_CONV, spectral: "km/s" }, setConv })} />);
    const convention = screen.getByLabelText(/Velocity convention \(your choice; the file declares none\)/);
    await userEvent.selectOptions(convention, "optical");
    expect(setConv).toHaveBeenCalledWith({ ...NO_CONV, spectral: "km/s", convention: "optical" });
    rerender(<CoordinateReadout v={base({ offers: withVelocity, conv: NO_CONV })} />);
    expect(screen.queryByLabelText(/Velocity convention/)).toBeNull();
  });

  it("shows the converted values with the basis, the user's convention, and the declared label that survives a conversion", () => {
    const r: Converted = {
      x: 3, y: 4, channel: 0,
      spectral: { native: { value: 1.42e9, unit: "Hz", unit_status: "declared" }, converted: { value: 85.6, unit: "km/s" }, refused: null, basis: "astropy.units doppler_radio with the file's RESTFRQ", convention: "radio", convention_source: "user" },
      sky: { native: { lon: 150.5, lat: 2.5, frame: "ICRS", frame_status: "assumed", lon_name: "RA", lat_name: "Dec" }, converted: { lon: 226.1, lat: 48.2, frame: "Galactic", lon_name: "l", lat_name: "b" }, refused: null, basis: "astropy.coordinates SkyCoord, from ICRS (assumed) to Galactic", frame_note: "the header declares no RADESYS or EQUINOX: ICRS is assumed, the FITS standard default (what WCS does)" },
      value: { native: { value: 2, unit: "Jy/beam", unit_status: "user" }, converted: { value: 2000, unit: "mJy/beam", unit_status: "user" }, refused: null, basis: "unit change within one dimension (astropy.units)" },
    };
    render(<CoordinateReadout v={base({ conv: { spectral: "km/s", convention: "radio", frame: "galactic", to: "mJy/beam" }, convertedNow: r })} />);
    expect(screen.getByText(/85\.6 km\/s \(radio convention, chosen by you\)/)).toBeInTheDocument();
    expect(screen.getByText("l (Galactic)")).toBeInTheDocument();
    expect(screen.getByText("226.10000°")).toBeInTheDocument();
    expect(screen.getByText("2000 mJy/beam (declared by you)")).toBeInTheDocument();
    expect(screen.getByText(/doppler_radio/)).toBeInTheDocument();
    expect(screen.getByText(/FITS standard default \(what WCS does\)/)).toBeInTheDocument();
    expect(screen.getByText("2 Jy/beam")).toBeInTheDocument();       // the native value stays
  });

  it("says why a conversion was refused and shows no number for it", () => {
    const r: Converted = {
      x: 3, y: 4, channel: 0, spectral: null, sky: null,
      value: { native: { value: 2, unit: "K", unit_status: "declared" }, converted: null, refused: "K and km/s do not measure the same thing", basis: null },
    };
    render(<CoordinateReadout v={base({ conv: { ...NO_CONV, to: "mJy/beam" }, convertedNow: r })} />);
    expect(screen.getByText(/not converted: K and km\/s do not measure the same thing/)).toBeInTheDocument();
  });

  it("offers no value unit for an orientation map, and none for a file whose unit is unknown", () => {
    render(<CoordinateReadout v={base({ interp: { unit: "deg", reliabilityId: null } })} />);
    expect(screen.queryByLabelText("Value unit")).toBeNull();
  });

  it("explains an unknown unit instead of offering a choice", () => {
    render(<CoordinateReadout v={base({ offers: { ...offers, value: { unit: null, unit_status: "unresolved", targets: [], reason: "the unit is unknown (no BUNIT and none declared): no conversion is offered" } } })} />);
    expect(screen.queryByLabelText("Value unit")).toBeNull();
    expect(screen.getByText(/the unit is unknown \(no BUNIT and none declared\)/)).toBeInTheDocument();
  });
});

// The hook: choices become one request for the pixel and channel on screen, the answer is shown only for those, a choice
// the file stops allowing is dropped, and another dataset starts from native values.
function frame(width: number, height: number, values: number[]): ArrayBuffer {
  const head = JSON.stringify({ width, height, channel: 0, channels: 1, nbytes: values.length * 4, dtype: "<f4", stats: { min: 0, max: 1, mean: 0, finite_count: values.length, non_finite: 0, overflow_count: 0 }, bunit: "Jy/beam", bunit_status: "declared", spectral_value: null, spectral_unit: null, flagged: null, preview: null, nan_meaning: "" });
  const hb = new TextEncoder().encode(head);
  const pad = (4 - (hb.length % 4)) % 4;
  const buf = new ArrayBuffer(4 + hb.length + pad + values.length * 4);
  new DataView(buf).setUint32(0, hb.length + pad, true);
  new Uint8Array(buf, 4, hb.length).set(hb);
  new Uint8Array(buf, 4 + hb.length, pad).fill(32);
  new Float32Array(buf, 4 + hb.length + pad, values.length).set(values);
  return buf;
}

describe("the viewer's conversion state", () => {
  const dataset = (id: number, unit?: string) => ({ id, hostId: 0, interpretation: null, modifiedAt: "t", unitDeclaration: unit ? { unit, declaredBy: "user", declaredAt: "2026-11-10T10:00:00Z", stale: false } : null }) as unknown as Dataset;

  function api() {
    const converts: string[] = [];
    let current: Conversions = offers;
    const impl = (input: RequestInfo | URL): Promise<Response> => {
      const url = String(input);
      const json = (b: unknown) => Promise.resolve(new Response(JSON.stringify(b), { status: 200, headers: { "Content-Type": "application/json" } }));
      if (url.includes("/convert?")) {
        converts.push(url);
        return json({ x: 1, y: 0, channel: 0, spectral: null, sky: null, value: { native: { value: 0.5, unit: "Jy/beam", unit_status: "user" }, converted: { value: 500, unit: "mJy/beam", unit_status: "user" }, refused: null, basis: "b" } });
      }
      if (url.endsWith("/conversions")) return json(current);
      if (url.endsWith("/view")) return json({ width: 2, height: 2, channels: 1, bunit: "Jy/beam", bunit_status: "user", fits_axes: { x: 1, y: 2, channel: null }, sky: { kind: "pixel", note: "" }, spectral: null, flags: { declared: false, channels: [] } });
      if (url.includes("/plane")) return Promise.resolve(new Response(frame(2, 2, [0.25, 0.5, 0.75, 1]), { status: 200 }));
      return json([]);
    };
    return { converts, impl, setOffers: (o: Conversions) => { current = o; } };
  }

  it("sends the pixel value and the chosen unit for the pixel on screen, and shows the answer only for that pixel", async () => {
    const a = api();
    vi.stubGlobal("fetch", vi.fn(a.impl));
    const { result } = renderHook(() => useViewer(dataset(7, "Jy/beam")));
    await waitFor(() => expect(result.current.offers).not.toBeNull());
    await waitFor(() => expect(result.current.plane).not.toBeNull());
    act(() => {
      result.current.setPinned({ x: 1, y: 0 });
      result.current.setConv({ ...NO_CONV, to: "mJy/beam" });
    });
    await waitFor(() => expect(result.current.convertedNow?.value?.converted?.value).toBe(500));
    expect(a.converts).toHaveLength(1);
    const q = new URL(a.converts[0] ?? "", "http://x").searchParams;
    expect(Object.fromEntries(q)).toEqual({ x: "1", y: "0", channel: "0", to: "mJy/beam", value: "0.5" });
    act(() => result.current.setPinned({ x: 0, y: 0 }));
    expect(result.current.convertedNow).toBeNull();     // the old pixel's answer is never shown for the new pixel
    await waitFor(() => expect(a.converts).toHaveLength(2));
    expect(new URL(a.converts[1] ?? "", "http://x").searchParams.get("value")).toBe("0.25");
  });

  it("sends nothing while only native values are chosen, and the convention only with a spectral unit", async () => {
    const a = api();
    vi.stubGlobal("fetch", vi.fn(a.impl));
    const { result } = renderHook(() => useViewer(dataset(7, "Jy/beam")));
    await waitFor(() => expect(result.current.plane).not.toBeNull());
    act(() => result.current.setPinned({ x: 1, y: 1 }));
    await new Promise((r) => setTimeout(r, 120));
    expect(a.converts).toHaveLength(0);
    act(() => result.current.setConv({ ...NO_CONV, frame: "galactic" }));
    await waitFor(() => expect(a.converts).toHaveLength(1));
    const q = new URL(a.converts[0] ?? "", "http://x").searchParams;
    expect(q.get("frame")).toBe("galactic");
    expect(q.has("convention")).toBe(false);
    expect(q.has("value")).toBe(false);
  });

  it("drops a choice the file no longer allows, and starts from native values for another dataset", async () => {
    const a = api();
    vi.stubGlobal("fetch", vi.fn(a.impl));
    const { result, rerender } = renderHook(({ d }) => useViewer(d), { initialProps: { d: dataset(7, "Jy/beam") } });
    await waitFor(() => expect(result.current.offers).not.toBeNull());
    act(() => result.current.setConv({ ...NO_CONV, to: "mJy/beam" }));
    expect(result.current.conv.to).toBe("mJy/beam");
    // the unit is withdrawn: no value conversion is allowed any more
    a.setOffers({ ...offers, value: { unit: null, unit_status: "unresolved", targets: [], reason: "the unit is unknown" } });
    rerender({ d: dataset(7) });
    await waitFor(() => expect(result.current.conv.to).toBe(""));
    act(() => result.current.setConv({ ...NO_CONV, frame: "icrs" }));
    rerender({ d: dataset(8) });
    await waitFor(() => expect(result.current.conv).toEqual(NO_CONV));
  });
});
