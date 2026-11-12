import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Dataset, Header, ImageDataset } from "../api/catalog";
import { lut } from "./colormaps";
import { glyphStride, orientationLines } from "./glyphs";
import { axialMeanText, InterpretationPanel, meanText, reliabilityChoices, suggestCompanion } from "./OrientationPanels";
import type { AxialSummary } from "../api/planes";
import { axialDegrees, renderPlane } from "./render";
import type { Viewer } from "./useViewer";

describe("axial angles", () => {
  it("treats 0 and 180 degrees, and -10 and 170, as the same orientation", () => {
    expect(axialDegrees(180, 1)).toBe(0);
    expect(axialDegrees(-10, 1)).toBe(170);
    expect(axialDegrees(Math.PI, 180 / Math.PI)).toBeCloseTo(0, 9);
  });

  it("colours 0 and 180 degrees identically on the cyclic map, and never with a seam at the ends", () => {
    const t = lut("twilight");
    expect(Array.from(t.slice(0, 3))).toEqual(Array.from(t.slice(765, 768)));
    const px = (v: number) => {
      const img = renderPlane(new Float32Array([v]), 1, 1, { lut: t, range: null, stretch: "linear", outW: 1, outH: 1, nodata: [[0, 0, 0], [9, 9, 9]], axial: { toDegrees: 1 } });
      return Array.from(img.pixels.slice(0, 3));
    };
    expect(px(0)).toEqual(px(180));
    expect(px(1)).toEqual(px(181));
    expect(px(45)).not.toEqual(px(135));
  });

  it("draws NaN angles as no value, not as an orientation", () => {
    const img = renderPlane(new Float32Array([NaN]), 1, 1, { lut: lut("twilight"), range: null, stretch: "linear", outW: 1, outH: 1, nodata: [[1, 2, 3], [9, 9, 9]], axial: { toDegrees: 1 } });
    expect(Array.from(img.pixels.slice(0, 3))).toEqual([1, 2, 3]);
  });
});

describe("axial mean text", () => {
  it("shows an orientation that rounds to 180 as 0, since they are the same", () => {
    expect(axialMeanText(179.9999999)).toBe("0.00°");
    expect(axialMeanText(0)).toBe("0.00°");
    expect(axialMeanText(152.0683)).toBe("152.07°");
  });
});

describe("orientation lines", () => {
  // the fixture's tangential field around the centre pixel: angle from +x towards +y (y up)
  const n = 33;
  const c = 16;
  const field = new Float32Array(n * n).map((_, i) => {
    const y = Math.floor(i / n);
    const x = i % n;
    return x === c && y === c ? NaN : (((Math.atan2(y - c, x - c) * 180) / Math.PI + 90) % 180 + 180) % 180;
  });
  const at = (dx: number, dy: number) => {
    const lines = orientationLines(field, n, n, 1, { stride: 1 });
    const cx = c + dx + 0.5;
    const cy = n - 1 - (c + dy) + 0.5;
    const l = lines.find((g) => Math.abs((g.x1 + g.x2) / 2 - cx) < 1e-9 && Math.abs((g.y1 + g.y2) / 2 - cy) < 1e-9)!;
    return { dx: l.x2 - l.x1, dy: l.y2 - l.y1 };
  };

  it("draws a tangential field the right way round (a mirrored convention would fail here)", () => {
    const right = at(5, 0);
    expect(Math.abs(right.dx)).toBeLessThan(1e-9); // right of the centre: vertical
    const above = at(0, 5);
    expect(Math.abs(above.dy)).toBeLessThan(1e-9); // above the centre: horizontal
    const upRight = at(5, 5);
    expect(upRight.dx * upRight.dy).toBeGreaterThan(0); // up-right: runs from up-left to down-right in the picture
    const upLeft = at(-5, 5);
    expect(upLeft.dx * upLeft.dy).toBeLessThan(0);
  });

  it("skips pixels with no angle and fades unknown reliability", () => {
    const lines = orientationLines(new Float32Array([NaN, 10]), 2, 1, 1, { stride: 1, reliability: new Float32Array([1, NaN]), relScale: 1 });
    expect(lines).toHaveLength(1);
    expect(lines[0]!.opacity).toBe(0.2);
  });

  it("scales opacity to the companion map's own range, not to 0..1", () => {
    const lines = orientationLines(new Float32Array([10, 10]), 2, 1, 1, { stride: 1, reliability: new Float32Array([0.5, 0.25]), relScale: 0.5 });
    expect(lines.map((l) => l.opacity)).toEqual([1, 0.5]);
    expect(glyphStride(4)).toBe(4);
  });
});

describe("declaring an interpretation", () => {
  const ds = (id: number, name: string, size = 4, path = `/d/${name}`): ImageDataset => ({ id, name, path, sizeBytes: 1, modifiedAt: "", addedAt: "", interpretation: null, nature: "", unitDeclaration: null, hostId: 0, header: { axes: [{ axis: 1, length: size }, { axis: 2, length: size }] } as unknown as Header });
  const viewer = { view: { channels: 1 }, plane: { head: { stats: { min: 0, max: 3.1 } } } } as unknown as Viewer;

  it("suggests a reliability map by file name only", () => {
    expect(suggestCompanion("psi_hnv1.fits", [ds(2, "psi_reliability_hnv1.fits"), ds(3, "other.fits")])?.id).toBe(2);
    expect(suggestCompanion("psi_hnv1.fits", [ds(3, "other.fits")])).toBeNull();
  });

  it("does not let the unit be left unchosen and says the value range is only a hint", async () => {
    const calls: unknown[] = [];
    render(<InterpretationPanel v={viewer} dataset={ds(1, "psi_hnv1.fits")} datasets={[ds(1, "psi_hnv1.fits"), ds(2, "psi_reliability_hnv1.fits")]} declare={async (_id, b) => (calls.push(b), null)} />);
    await userEvent.selectOptions(screen.getByLabelText("Treat this map as"), "axial");
    const apply = screen.getByRole("button", { name: "Declare axial" });
    expect(apply).toBeDisabled();
    expect(screen.getAllByText(/never infers the unit/).length).toBeGreaterThan(0);
    expect(screen.getByText(/may be its reliability map/)).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Angle unit (required)"), "rad");
    await userEvent.click(apply);
    expect(calls).toEqual([{ kind: "axial_orientation", unit: "rad", reliabilityId: null, frame: null, quantity: null }]);
  });

  it("says the file declares neither the frame nor the quantity, and lets the person declare both with plain explanations", async () => {
    const calls: unknown[] = [];
    render(<InterpretationPanel v={viewer} dataset={ds(1, "psi.fits")} datasets={[ds(1, "psi.fits")]} declare={async (_id, b) => (calls.push(b), null)} />);
    await userEvent.selectOptions(screen.getByLabelText("Treat this map as"), "axial");
    expect(screen.getAllByText(/frame is unresolved: the file does not say which direction the angle is measured from/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/What the angle describes is unresolved: the file does not say/).length).toBeGreaterThan(0);
    expect(screen.queryByText(/assumes/)).not.toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Angle unit (required)"), "deg");
    await userEvent.selectOptions(screen.getByLabelText("Angle frame"), "sky");
    expect(screen.getAllByText(/position angle east of north \(IAU\)/).length).toBeGreaterThan(0);
    for (const [value, text] of [
      ["b_field", /the angle of the field itself/],
      ["gradient", /perpendicular to B, as raw velocity gradients/],
      ["dust_polarization", /E-vector of emission, perpendicular to B/],
      ["starlight_polarization", /parallel to B/],
      ["unspecified", /refused for comparison with other datasets/],
    ] as const) {
      await userEvent.selectOptions(screen.getByLabelText("What the angle is"), value);
      expect(screen.getAllByText(text).length).toBeGreaterThan(0);
    }
    await userEvent.selectOptions(screen.getByLabelText("What the angle is"), "dust_polarization");
    await userEvent.click(screen.getByRole("button", { name: "Declare axial" }));
    expect(calls).toEqual([{ kind: "axial_orientation", unit: "deg", reliabilityId: null, frame: "sky", quantity: "dust_polarization" }]);
  });

  it("shows an earlier run import's assumed convention as an assumption, overridable, and starts from it", async () => {
    const proposed: Dataset = { ...ds(1, "psi.fits"), interpretation: { kind: "axial_orientation", unit: "rad", reliabilityId: null, frame: "pixel", quantity: "b_field", declaredBy: "vgt-run-adapter", tier: "inferred", mappingVersion: "1", declaredAt: "", stale: false } };
    const calls: unknown[] = [];
    render(<InterpretationPanel v={viewer} dataset={proposed} datasets={[proposed]} declare={async (_id, b) => (calls.push(b), null)} />);
    expect(screen.getByText(/Proposed by an earlier version of the run import, which assumed the pipeline’s convention \(ψ = ½ atan2\(U,Q\) \+ 90°\).*an assumption, not a statement by the file/)).toBeInTheDocument();
    expect(screen.getByLabelText("Angle frame")).toHaveValue("pixel");
    expect(screen.getByLabelText("What the angle is")).toHaveValue("b_field");
    await userEvent.selectOptions(screen.getByLabelText("What the angle is"), "gradient");
    await userEvent.click(screen.getByRole("button", { name: "Declare axial" }));
    expect(calls).toEqual([{ kind: "axial_orientation", unit: "rad", reliabilityId: null, frame: "pixel", quantity: "gradient" }]);
  });

  it("describes a declaration from before the frame and quantity existed as not declared", () => {
    const old: Dataset = { ...ds(1, "psi.fits"), interpretation: { kind: "axial_orientation", unit: "deg", reliabilityId: null, frame: null, quantity: null, declaredBy: "user", tier: "declared", mappingVersion: "", declaredAt: "", stale: false } };
    render(<InterpretationPanel v={viewer} dataset={old} datasets={[old]} declare={async () => null} />);
    expect(screen.getByText(/Frame: unresolved \(not declared yet\)\. Quantity: unresolved \(not declared yet\)\./)).toBeInTheDocument();
  });

  it("says plainly that a header its profile cannot place leaves the frame and quantity unresolved, and assumes nothing", () => {
    const vgtHeader = { product: "psi", psiFrame: null, psiAngle: null, params: [], resolution: null, notes: [], profile: "profile:VGT.jl@1", unresolved: ["frame", "quantity"],
      orientation: null, conflicts: [] } as unknown as Dataset["vgtHeader"];
    const d: Dataset = { ...ds(1, "psi.fits"), vgtHeader };
    render(<InterpretationPanel v={viewer} dataset={d} datasets={[d]} declare={async () => null} />);
    expect(screen.getByText(/profile:VGT\.jl@1 cannot place the frame, quantity of this file.*unresolved\. Nothing is inferred/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Use the file’s declaration" })).not.toBeInTheDocument();
  });

  it("shows what the file's own header declares, a disagreement with the person's declaration, and adopts it only on request", async () => {
    const vgtHeader = { product: "psi", psiFrame: "GALACTIC", psiAngle: "BFIELD", params: [{ key: "VGTDN", value: "10" }], resolution: null, notes: [], profile: "profile:VGT.jl@1", unresolved: [],
      orientation: { profile: "profile:VGT.jl@1", unit: "rad", frame: "sky", skyFrame: "galactic", zeroDirection: "north", sense: "east of north", cards: { frame: "PSIFRAME=GALACTIC" }, quantity: "b_field", basis: { frame: "file", quantity: "file", unit: "file" } },
      conflicts: ["the header states the angle as a position angle east of north in the Galactic frame (PSIFRAME=GALACTIC), which is not this grid's own frame; the declaration in force says east of north in the grid's own frame"] } as unknown as Dataset["vgtHeader"];
    const mine: Dataset = { ...ds(1, "psi.fits"), vgtHeader, interpretation: { kind: "axial_orientation", unit: "rad", reliabilityId: null, frame: "sky", quantity: "b_field", declaredBy: "user", tier: "declared", mappingVersion: "", declaredAt: "", stale: false } };
    const calls: unknown[] = [];
    render(<InterpretationPanel v={viewer} dataset={mine} datasets={[mine]} declare={async (_id, b) => (calls.push(b), null)} />);
    expect(screen.getByRole("alert")).toHaveTextContent(/disagrees with the declaration in force.*stays in use/);
    expect(screen.getByRole("alert")).toHaveTextContent(/Galactic frame/);
    expect(screen.getByText(/VGT keywords in the file \(psi\)/)).toBeInTheDocument();
    expect(calls).toEqual([]);                                   // nothing was applied by showing it
    await userEvent.click(screen.getByRole("button", { name: "Use the file’s declaration" }));
    expect(calls).toEqual([{ kind: "axial_orientation", fromFile: true }]);
  });
});

describe("reliability map choices", () => {
  const ds = (id: number, name: string, size: number, path: string, hostId = 0, extra: object = {}): Dataset =>
    ({ id, name, path, sizeBytes: 1, modifiedAt: "", addedAt: "", interpretation: null, nature: "", unitDeclaration: null, hostId, header: { axes: [{ axis: 1, length: size }, { axis: 2, length: size }] } as unknown as Header, ...extra }) as Dataset;

  it("lists only maps of the same size on the same host, never itself or an archived one", () => {
    const self = ds(1, "psi.fits", 64, "/a/psi.fits");
    const all = [self, ds(2, "rel.fits", 64, "/a/rel.fits"), ds(3, "small.fits", 32, "/a/small.fits"), ds(4, "old.fits", 64, "/a/old.fits", 0, { archived: true }), ds(5, "far.fits", 64, "/b/far.fits", 7)];
    expect(reliabilityChoices(self, all, null).map((c) => c.id)).toEqual([2]);
  });

  it("keeps an already declared choice listed even when it no longer fits", () => {
    const self = ds(1, "psi.fits", 64, "/a/psi.fits");
    expect(reliabilityChoices(self, [self, ds(3, "small.fits", 32, "/a/small.fits")], 3).map((c) => c.id)).toEqual([3]);
  });

  it("tells two files of one name apart by their folder, and by host when it is not this computer", () => {
    const self = ds(1, "psi.fits", 64, "/a/psi.fits");
    const labels = reliabilityChoices(self, [self, ds(2, "rel.fits", 64, "/run1/rel.fits"), ds(3, "rel.fits", 64, "/run2/rel.fits"), ds(4, "alone.fits", 64, "/x/alone.fits")], null).map((c) => c.label);
    expect(labels).toEqual(["rel.fits (…/run1)", "rel.fits (…/run2)", "alone.fits"]);
  });
});

describe("mean orientation wording", () => {
  const a = (over: Partial<AxialSummary>): AxialSummary => ({ defined: false, mean_deg: null, resultant: null, n: 5, reason: null, ...over }) as AxialSummary;
  it("says orientations cancel when the resultant vanishes, and not when there is nothing to average", () => {
    expect(meanText(a({ resultant: 0 }))).toBe("no mean orientation (the orientations cancel)");
    expect(meanText(a({ n: 0 }))).toBe("no mean orientation (no pixel has a value)");
    expect(meanText(a({ defined: true, mean_deg: 12.345, resultant: 0.9 }))).toBe("12.35°");
  });
});
