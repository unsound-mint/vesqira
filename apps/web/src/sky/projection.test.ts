import { clampFov, contains, fitView, grid, gridStep, MAX_FOV, pan, separation, tangent, toScreen, wrap360, zoom } from "./projection";

// The projection only lays out world coordinates the worker computed; these tests hold the layout to independent numbers
// (the gnomonic formulas written out, great-circle separations of known pairs), never to a dataset's WCS.

describe("display geometry of the sky panel", () => {
  it("puts the view centre at the middle of the square and east on the left", () => {
    const v = { lon: 150, lat: 2, fov: 1 };
    expect(toScreen(150, 2, v, 360)).toEqual([180, 180]);
    const east = toScreen(150.1, 2, v, 360)!;
    const north = toScreen(150, 2.1, v, 360)!;
    expect(east[0]).toBeLessThan(180);          // larger longitude is to the left, as on a sky chart
    expect(north[1]).toBeLessThan(180);         // larger latitude is up (screen y grows downward)
    expect(north[0]).toBeCloseTo(180, 6);
  });

  it("matches the gnomonic formulas for an off-centre point", () => {
    // 1 degree east and 1 degree north of (30, 40): the formulas of a tangent plane, written out here
    const c = { lon: 30, lat: 40 };
    const t = tangent(31, 41, c)!;
    const d = Math.PI / 180;
    const cosc = Math.sin(40 * d) * Math.sin(41 * d) + Math.cos(40 * d) * Math.cos(41 * d) * Math.cos(1 * d);
    expect(t[0]).toBeCloseTo((Math.cos(41 * d) * Math.sin(1 * d)) / cosc, 12);
    expect(t[1]).toBeCloseTo((Math.cos(40 * d) * Math.sin(41 * d) - Math.sin(40 * d) * Math.cos(41 * d) * Math.cos(1 * d)) / cosc, 12);
    expect(tangent(210, -40, c)).toBeNull();     // the antipode has no tangent plane
  });

  it("scales so that the field of view spans the square", () => {
    const v = { lon: 10, lat: 0, fov: 2 };
    const left = toScreen(11, 0, v, 360)!;
    const right = toScreen(9, 0, v, 360)!;
    expect(left[0]).toBeCloseTo(0, 1);           // the edge of a 2 degree field is one degree from the centre
    expect(right[0]).toBeCloseTo(360, 1);
  });

  it("measures separations on the sphere, across the 0/360 seam too", () => {
    expect(separation([359.5, 0], [0.5, 0])).toBeCloseTo(1, 9);
    expect(separation([0, 89.5], [180, 89.5])).toBeCloseTo(1, 9);   // over the pole
    expect(separation([10, 10], [10, 10])).toBe(0);
  });

  it("fits a view around outlines, across longitude 0, and keeps a margin", () => {
    const square: [number, number][] = [[-0.5, -0.5], [0.5, -0.5], [0.5, 0.5], [-0.5, 0.5]];
    const v = fitView([square])!;
    expect(v.lon === 0 || v.lon === 360 || Math.abs(v.lon) < 1e-6 || Math.abs(v.lon - 360) < 1e-6).toBe(true);
    expect(v.lat).toBeCloseTo(0, 6);
    expect(v.fov).toBeGreaterThan(2 * 0.7071 * 1.2);      // the corners are 0.707 degrees out, with a margin
    expect(fitView([])).toBeNull();
  });

  it("tells whether a position is inside an outline, and refuses a hemisphere-sized one", () => {
    const sq: [number, number][] = [[149, 1], [151, 1], [151, 3], [149, 3]];
    expect(contains(sq, [150, 2])).toBe(true);
    expect(contains(sq, [152, 2])).toBe(false);
    expect(contains(sq, [150, 3.5])).toBe(false);
    expect(contains([[0, 0], [90, 0], [180, 0], [270, 0]], [0, 0])).toBe(false);
    // an outline across longitude 0 given in unwrapped longitudes
    expect(contains([[-1, -1], [1, -1], [1, 1], [-1, 1]], [359.5, 0])).toBe(true);
  });

  it("pans toward the sky's east when asked for left, and keeps latitude out of the pole", () => {
    const v = pan({ lon: 100, lat: 0, fov: 10 }, -0.2, 0);
    expect(v.lon).toBeGreaterThan(100);
    expect(pan({ lon: 5, lat: 89, fov: 10 }, 0, 5).lat).toBeLessThanOrEqual(89.9);
    expect(wrap360(-10)).toBe(350);
    expect(zoom({ lon: 0, lat: 0, fov: 1 }, 1000).fov).toBe(MAX_FOV);
    expect(clampFov(1e-9)).toBeGreaterThan(0);
  });

  it("draws a grid with labels in the panel's own coordinates, quietly, and never thousands of lines", () => {
    const g = grid({ lon: 150, lat: 2, fov: 1 }, 360);
    expect(g.length).toBeGreaterThan(4);
    expect(g.filter((l) => l.axis === "lat").every((l) => l.value >= 1 && l.value <= 3)).toBe(true);
    expect(g.every((l) => l.label === null || /°$/.test(l.label.text))).toBe(true);
    expect(gridStep(1)).toBeLessThanOrEqual(0.5);
    expect(gridStep(40)).toBeGreaterThanOrEqual(10);
    expect(grid({ lon: 10, lat: 89.99999, fov: 0.02 }, 360).length).toBeLessThan(40);   // a small field on the pole sees every longitude
  });

  it("keeps longitude labels apart and its lines about as far apart as the latitude lines, even at high declination", () => {
    const g = grid({ lon: 3, lat: 72.6, fov: 0.5 }, 360);
    const xs = g.filter((l) => l.axis === "lon" && l.label).map((l) => l.label!.at[0]).sort((a, b) => a - b);
    expect(xs.length).toBeGreaterThan(0);
    for (let i = 1; i < xs.length; i++) expect(xs[i]! - xs[i - 1]!).toBeGreaterThanOrEqual(46);
    expect(g.filter((l) => l.axis === "lon").length).toBeLessThanOrEqual(12);
  });
});
