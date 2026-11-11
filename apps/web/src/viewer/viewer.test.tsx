import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { parsePlane } from "../api/planes";
import type { Plane, View } from "../api/planes";
import { lut } from "./colormaps";
import { ChannelTimeline } from "./ChannelTimeline";
import { CoordinateReadout } from "./Panels";
import { channelForKey, fit, pixelAt } from "./geometry";
import { flagState, precisionNote, spectralText, valueText } from "./format";
import { limits, normalize, outputSize, renderPlane } from "./render";
import type { Viewer } from "./useViewer";

const NODATA = [[10, 20, 30], [200, 210, 220]] as const;
const px = (img: { pixels: Uint8ClampedArray; width: number }, x: number, y: number) => Array.from(img.pixels.slice((y * img.width + x) * 4, (y * img.width + x) * 4 + 3));

describe("render", () => {
  const table = lut("gray");
  const opts = { lut: table, range: [0, 10] as [number, number], stretch: "linear" as const, outW: 4, outH: 1, nodata: NODATA };

  const stripe = (img: { pixels: Uint8ClampedArray; width: number; height: number }) => {
    const seen = new Set<string>();
    for (let y = 0; y < img.height; y++) for (let x = 0; x < img.width; x++) seen.add(px(img, x, y).join(","));
    return seen;
  };

  it("draws NaN as striped no-data and zero as a real value", () => {
    const img = renderPlane(new Float32Array([0, NaN, 10, Infinity]), 4, 1, { ...opts, outW: 12, outH: 3 }); // 3x zoom
    expect(px(img, 1, 0)).toEqual([0, 0, 0]);                 // pixel 0 = value 0 = bottom of the ramp, not missing
    expect(px(img, 7, 0)).toEqual([255, 255, 255]);           // pixel 2 = 10
    const nan = { pixels: img.pixels.slice(), width: 12, height: 3 };
    const colours = new Set<string>();
    for (const x of [3, 4, 5, 9, 10, 11]) for (let y = 0; y < 3; y++) colours.add(px(nan, x, y).join(","));
    expect(colours).toEqual(new Set(["10,20,30", "200,210,220"]));   // both stripe colours, never a colormap entry
  });

  it("keeps no-data striped when zoomed out, where one canvas pixel stands for many image pixels", () => {
    const data = new Float32Array(100 * 100).fill(NaN);
    const img = renderPlane(data, 100, 100, { ...opts, outW: 20, outH: 20 });
    expect(stripe(img)).toEqual(new Set(["10,20,30", "200,210,220"]));
  });

  it("puts plane row 0 (FITS y=1) at the bottom of the picture", () => {
    const img = renderPlane(new Float32Array([0, 10]), 1, 2, { ...opts, outW: 1, outH: 2 }); // row0 = 0, row1 = 10
    expect(px(img, 0, 1)).toEqual([0, 0, 0]);
    expect(px(img, 0, 0)).toEqual([255, 255, 255]);
  });

  it("zooms in by whole numbers and out to the display size", () => {
    expect(outputSize(32, 24, 16.7)).toEqual({ outW: 512, outH: 384 });
    expect(outputSize(2174, 2174, 0.32)).toEqual({ outW: 696, outH: 696 });
  });

  it("shows everything as no-data when there are no finite limits", () => {
    const img = renderPlane(new Float32Array([1, 2]), 2, 1, { ...opts, outW: 2, outH: 1, range: null });
    expect(stripe(img).has("255,255,255")).toBe(false);
    expect(px(img, 0, 0)).toEqual([10, 20, 30]);
  });

  it("limits come from finite statistics and stretches stay in [0, 1]", () => {
    const stats = { finite_count: 3, nonfinite_count: 1, min: -1, max: 9, p01: 0, p99: 8, overflow_count: 0 };
    expect(limits(stats, "percentile")).toEqual([0, 8]);
    expect(limits(stats, "minmax")).toEqual([-1, 9]);
    expect(limits({ ...stats, min: null, max: null, p01: null, p99: null, overflow_count: 0 }, "minmax")).toBeNull();
    for (const s of ["linear", "sqrt", "asinh"] as const) {
      expect(normalize(-5, 0, 1, s)).toBe(0);
      expect(normalize(5, 0, 1, s)).toBeCloseTo(1, 12);
    }
    expect(normalize(3, 3, 3, "linear")).toBe(0.5);
  });

  it("colormap tables run dark to bright without gaps", () => {
    const v = lut("viridis");
    expect(Array.from(v.slice(0, 3))).toEqual([68, 1, 84]);
    expect(v.length).toBe(768);
  });
});

describe("geometry and keys", () => {
  it("fits with square pixels", () => {
    expect(fit(200, 100, 32, 24)).toMatchObject({ height: 100, width: (100 / 24) * 32 });
    expect(fit(0, 0, 32, 24).width).toBe(0);
  });

  it("maps points to pixels with y counted from the bottom", () => {
    expect(pixelAt(0, 0, 40, 30, 4, 3)).toEqual({ x: 0, y: 2 });
    expect(pixelAt(39.9, 29.9, 40, 30, 4, 3)).toEqual({ x: 3, y: 0 });
    expect(pixelAt(40, 10, 40, 30, 4, 3)).toBeNull();
    expect(pixelAt(-1, 10, 40, 30, 4, 3)).toBeNull();
  });

  it("steps channels with arrows, pages and ends, clamped", () => {
    expect(channelForKey("ArrowRight", 5, 64)).toBe(6);
    expect(channelForKey("ArrowLeft", 0, 64)).toBe(0);
    expect(channelForKey("PageUp", 60, 64)).toBe(63);
    expect(channelForKey("PageDown", 25, 64)).toBe(15);
    expect(channelForKey("Home", 30, 64)).toBe(0);
    expect(channelForKey("End", 30, 64)).toBe(63);
    expect(channelForKey("a", 30, 64)).toBeNull();
  });
});

describe("plane frame", () => {
  const frame = (head: object, pixels: number[], pad = 0) => {
    let json = JSON.stringify(head);
    json += " ".repeat((4 - ((4 + json.length) % 4)) % 4 + pad);
    const buf = new ArrayBuffer(4 + json.length + pixels.length * 4);
    const dv = new DataView(buf);
    dv.setUint32(0, json.length, true);
    new Uint8Array(buf, 4, json.length).set(new TextEncoder().encode(json));
    pixels.forEach((p, i) => dv.setFloat32(4 + json.length + i * 4, p, true));
    return buf;
  };

  it("reads little-endian float32 and keeps NaN", () => {
    const p = parsePlane(frame({ width: 2, height: 1 }, [1.5, NaN]));
    expect(p.data[0]).toBe(1.5);
    expect(Number.isNaN(p.data[1])).toBe(true);
  });

  it("copes with a header that leaves the pixels unaligned", () => {
    const p = parsePlane(frame({ width: 2, height: 1 }, [2, 3], 1));
    expect(Array.from(p.data)).toEqual([2, 3]);
  });

  it("rejects a frame whose size does not match its descriptor", () => {
    expect(() => parsePlane(frame({ width: 2, height: 2 }, [1, 2]))).toThrow();
  });
});

describe("status words", () => {
  const view = (declared: boolean) => ({ flags: { declared, channels: [] } }) as unknown as View;
  const plane = (flagged: boolean | null) => ({ head: { flagged } }) as unknown as Plane;

  it("never says valid, and absent flags are not 'not flagged'", () => {
    expect(flagState(view(false), plane(null)).text).toBe("No channel flags declared");
    expect(flagState(view(true), plane(false)).text).toBe("Channel not flagged");
    expect(flagState(view(true), plane(true))).toEqual({ tone: "warn", text: "Channel flagged in the file" });
    for (const t of [flagState(view(false), plane(null)).text, flagState(view(true), plane(false)).text]) expect(t.toLowerCase()).not.toContain("valid");
  });

  it("discloses float32 precision loss only when the file stores more", () => {
    expect(precisionNote(-32)).toBe("");
    expect(precisionNote(16)).toBe("");
    expect(precisionNote(-64)).toContain("BITPIX -64");
    expect(precisionNote(32)).toContain("float32");
  });

  it("formats unknown, no-value and zero distinctly", () => {
    expect(valueText(null, "K")).toBe("—");
    expect(valueText(NaN, "K")).toBe("no value");
    expect(valueText(0, "K")).toBe("0 K");
    expect(spectralText(null, "m/s")).toBe("—");
    expect(spectralText(-24000, "m/s")).toBe("-24 km/s");
  });
});

describe("channel timeline keyboard", () => {
  function viewer(channel: number, set: (c: number) => void, flags = { declared: true, channels: [10, 11] }): Viewer {
    const v = { view: { channels: 64, flags, spectral: { name: "radio velocity", ctype: "VRAD", resolved: true, unit: "m/s" } }, channels: 64, channel, plane: { head: { channel, flagged: flags.declared ? false : null, spectral_value: -24000 } } } as unknown as Viewer;
    v.setChannel = set as Viewer["setChannel"];
    v.onChannelKey = (k: string) => {
      const n = channelForKey(k, channel, 64);
      if (n === null) return false;
      set(n);
      return true;
    };
    return v;
  }

  it("steps by one with arrows and by ten with Page keys", async () => {
    const seen: number[] = [];
    render(<ChannelTimeline v={viewer(20, (c) => seen.push(c))} />);
    const slider = screen.getByRole("slider", { name: "Channel" });
    slider.focus();
    await userEvent.keyboard("{ArrowRight}{PageUp}{PageDown}{Home}{End}");
    expect(seen).toEqual([21, 30, 10, 0, 63]);
  });

  it("lists flagged channels and says flagged data is kept", () => {
    render(<ChannelTimeline v={viewer(0, () => {})} />);
    expect(screen.getByText(/Flagged in the file: 11, 12/)).toBeInTheDocument();
    expect(screen.getByText(/data kept/)).toBeInTheDocument();
  });

  it("says when no flags are declared", () => {
    render(<ChannelTimeline v={viewer(0, () => {}, { declared: false, channels: [] })} />);
    expect(screen.getByText("No channel flags declared")).toBeInTheDocument();
    expect(screen.queryByText(/valid/i)).toBeNull();
  });
});

describe("coordinate readout", () => {
  const base = {
    view: { bunit: "K", bunit_status: "declared", sky: { kind: "celestial", lon_name: "RA", lat_name: "Dec", frame: "ICRS", frame_status: "declared" }, spectral: null },
    plane: { head: { channel: 5, spectral_value: null } },
    probe: { x: 3, y: 4 },
    pixelValue: 1,
  };

  it("never pairs a pixel with another pixel's coordinates", () => {
    const stale = { x: 2, y: 4, channel: 5, sky: { lon: 150.5, lat: 2.5 }, spectral: null };
    render(<CoordinateReadout v={{ ...base, coord: stale } as unknown as Viewer} />);
    expect(screen.queryByText(/150\.5/)).toBeNull();
  });

  it("shows coordinates that match the pixel and channel", () => {
    const fresh = { x: 3, y: 4, channel: 5, sky: { lon: 150.5, lat: 2.5 }, spectral: null };
    render(<CoordinateReadout v={{ ...base, coord: fresh } as unknown as Viewer} />);
    expect(screen.getByText("150.50000°")).toBeInTheDocument();
  });
});
