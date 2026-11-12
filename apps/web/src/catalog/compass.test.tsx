import { render, screen } from "@testing-library/react";
import type { RelOrientSummary } from "../api/analysis";
import { CompassGlyph, compassNote, compassTips, usableCompass } from "../ui/Compass";
import type { CompassFacts } from "../ui/Compass";
import { SegmentsFigure } from "./RelativeOrientation";

// What the worker says for a map whose WCS has CROTA2 = 30: north 30 degrees away from "up", east 30 away from "left"
// (test_compass.py checks those numbers against an independent astropy computation).
const rotated: CompassFacts = { available: true, x: 20, y: 15, north_deg: 120, east_deg: 210, frame: "icrs" };
const noWcs: CompassFacts = { available: false, reason: "this file has no complete celestial WCS, so an angle cannot be related to the sky" };

const summary = (compass?: CompassFacts): RelOrientSummary => ({
  label: "agreement statistic", mode: "map_catalog", am: 0.4, parameters: {}, counts: {}, histogram: {}, null: {}, policy: "p",
  map: { nx: 40, ny: 30, pixel_scale_arcmin: 1, ...(compass ? { compass } : {}) },
  segments: { x: [5, 20], y: [5, 15], map_pix_deg: [10, 80], other_pix_deg: [12, 70], used: [true, true], truncated: false },
}) as unknown as RelOrientSummary;

/** The angle of a drawn arrow, counter-clockwise from +x with y up, like the worker's. */
const drawn = (tip: { x: number; y: number }) => ((Math.atan2(-tip.y, tip.x) * 180) / Math.PI + 360) % 360;

describe("compass geometry", () => {
  it("draws each arrow at the angle the worker computed, with screen y flipped", () => {
    const t = compassTips({ north_deg: 120, east_deg: 210 }, 10);
    expect(drawn(t.north)).toBeCloseTo(120, 6);
    expect(drawn(t.east)).toBeCloseTo(210, 6);
    expect(t.north.y).toBeLessThan(0);                        // north is up and to the left on screen, not straight up
    expect(t.north.x).toBeLessThan(0);
    const flat = compassTips({ north_deg: 90, east_deg: 180 }, 10);
    expect(flat.north.x).toBeCloseTo(0, 9);
    expect(flat.east.y).toBeCloseTo(0, 9);
  });

  it("uses a compass only when the worker computed one", () => {
    expect(usableCompass(rotated)).not.toBeNull();
    expect(usableCompass(noWcs)).toBeNull();
    expect(usableCompass(undefined)).toBeNull();
    expect(usableCompass({ available: true } as CompassFacts)).toBeNull();            // no angles: nothing to draw
    expect(compassNote(noWcs)).toBe("No north/east arrows: this file has no complete celestial WCS, so an angle cannot be related to the sky.");
    expect(compassNote(undefined)).toMatch(/does not record where north and east point/);
    expect(compassNote(rotated)).toMatch(/icrs/);
  });
});

describe("the segments figure of a relative-orientation result", () => {
  it("draws N and E from the file's WCS, not from the image axes", () => {
    render(<SegmentsFigure s={summary(rotated)} />);
    const g = screen.getByTestId("compass");
    expect(g.getAttribute("data-north-deg")).toBe("120.00");
    expect(g.getAttribute("data-east-deg")).toBe("210.00");
    expect(g.textContent).toContain("N");
    expect(g.textContent).toContain("E");
    expect(screen.getByText(/increasing latitude and longitude \(icrs\)/)).toBeInTheDocument();
  });

  it("draws no compass without a celestial WCS and says why in the legend", () => {
    render(<SegmentsFigure s={summary(noWcs)} />);
    expect(screen.queryByTestId("compass")).toBeNull();
    expect(screen.getByText(/No north\/east arrows: this file has no complete celestial WCS/)).toBeInTheDocument();
  });

  it("draws no compass for a result that predates it, and says so", () => {
    render(<SegmentsFigure s={summary()} />);
    expect(screen.queryByTestId("compass")).toBeNull();
    expect(screen.getByText(/does not record where north and east point/)).toBeInTheDocument();
  });

  it("renders nothing for the glyph alone without a usable compass", () => {
    const { container } = render(<svg><CompassGlyph compass={noWcs} cx={10} cy={10} /></svg>);
    expect(container.querySelector("[data-testid=compass]")).toBeNull();
  });
});
