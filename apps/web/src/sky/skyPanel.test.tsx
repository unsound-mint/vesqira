import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Dataset } from "../api/catalog";
import { SkyPanel } from "./SkyPanel";
import { DEFAULTS, parseSettings } from "./settings";
import { SURVEYS, contactNotice } from "./surveys";

// Footprints and positions are worker answers; they are mocked here (test_sky_context.py holds them to astropy). What these tests
// protect is the panel's contract: nothing leaves the computer until the person says so, a dataset without a celestial WCS is listed
// as such and never drawn, the cursor shows a world coordinate, and a click on an outline or a list entry opens the dataset.

const getSkyFootprint = vi.fn();
const getSkyPoint = vi.fn();
vi.mock("../api/sky", () => ({
  getSkyFootprint: (...a: unknown[]) => getSkyFootprint(...a),
  getSkyPoint: (...a: unknown[]) => getSkyPoint(...a),
}));
const createSkyFigure = vi.fn();
vi.mock("../api/boards", () => ({ createSkyFigure: (...a: unknown[]) => createSkyFigure(...a) }));
// jsdom has no canvas: the picture is a stand-in, the caption it is given is what these tests read
const renderPng = vi.fn();
vi.mock("./exportSky", async (orig) => ({ ...(await orig<typeof import("./exportSky")>()), renderPng: (...a: unknown[]) => renderPng(...a) }));
const loadAladin = vi.fn();
vi.mock("./aladinLoader", () => ({ loadAladin: () => loadAladin() }));

const ds = (id: number, name: string, extra: Record<string, unknown> = {}) =>
  ({ id, name, modifiedAt: `m${id}`, corrections: null, header: {}, ...extra }) as unknown as Dataset;
const cube = ds(1, "hi_cube.fits");
const bare = ds(2, "no_wcs.fits");
const catalog = ds(3, "stars.fits", { kind: "catalog" });

const square = (lon: number, lat: number): [number, number][] => [[lon - 0.5, lat - 0.5], [lon + 0.5, lat - 0.5], [lon + 0.5, lat + 0.5], [lon - 0.5, lat + 0.5]];
const footprint = (lon: number, lat: number) => ({
  frame: "icrs", from_frame: "fk5", from_status: "declared", footprint: { polygon: square(lon, lat), center: { lon, lat }, extent_deg: 1.4 },
});

beforeEach(() => {
  localStorage.clear();
  getSkyFootprint.mockReset();
  getSkyPoint.mockReset();
  loadAladin.mockReset();
  createSkyFigure.mockReset();
  renderPng.mockReset();
  renderPng.mockResolvedValue(new Blob(["x"], { type: "image/png" }));
  createSkyFigure.mockResolvedValue({ id: 9, title: "t" });
  getSkyFootprint.mockImplementation((id: number) =>
    Promise.resolve(id === 1 ? footprint(150, 2) : { frame: "icrs", footprint: null, reason: "this file has no celestial WCS, so no position on the sky is known" }));
  getSkyPoint.mockResolvedValue({ frame: "icrs", position: { lon: 150.25, lat: 2.5 } });
});
afterEach(() => vi.unstubAllGlobals());

const mount = (props: Partial<React.ComponentProps<typeof SkyPanel>> = {}) => {
  const onOpen = vi.fn();
  const r = render(<SkyPanel datasets={[cube, bare, catalog]} datasetId={1} probe={null} onOpen={onOpen} {...props} />);
  return { onOpen, ...r };
};

describe("sky panel with the background off", () => {
  it("draws footprints without touching the network or loading the survey library", async () => {
    const fetchSpy = vi.fn();
    vi.stubGlobal("fetch", fetchSpy);
    mount();
    expect(await screen.findByRole("button", { name: "Open hi_cube.fits" })).toBeInTheDocument();
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(loadAladin).not.toHaveBeenCalled();
    expect(screen.getByText(/Off\. Nothing leaves this computer/)).toBeInTheDocument();
    expect(screen.getByText(/Plain coordinate grid\. No survey image: the background is off/)).toBeInTheDocument();
  });

  it("lists a dataset without a celestial WCS, and a catalog, with the reason and no outline", async () => {
    // fresh modifiedAt: footprints are cached per dataset revision across mounts
    const fresh = [ds(1, "hi_cube.fits", { modifiedAt: "fresh" }), ds(2, "no_wcs.fits", { modifiedAt: "fresh" }), catalog];
    mount({ datasets: fresh });
    await screen.findByRole("button", { name: "Open hi_cube.fits" });
    const list = screen.getByRole("list", { name: "Datasets on the sky" });
    await waitFor(() => expect(within(list).getByText(/has no celestial WCS/)).toBeInTheDocument());
    expect(within(list).getByText(/point catalog has no image grid/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Open no_wcs.fits" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Open stars.fits" })).toBeNull();
    // the catalog is never sent to the worker as an image
    expect(getSkyFootprint.mock.calls.map((c) => c[0]).sort()).toEqual([1, 2]);
  });

  it("says so when the open dataset has no position, and shows no cursor for it", async () => {
    mount({ datasetId: 2, probe: { x: 3, y: 4 } });
    expect(await screen.findByText(/no_wcs\.fits: this file has no celestial WCS.*The cursor has no sky position here/)).toBeInTheDocument();
    expect(getSkyPoint).not.toHaveBeenCalled();
  });

  it("shows the cursor as a world coordinate the worker computed, not the pixel", async () => {
    mount({ probe: { x: 12, y: 7 } });
    expect(await screen.findByText(/RA 150\.25/)).toBeInTheDocument();
    expect(getSkyPoint).toHaveBeenCalledWith(1, "icrs", 12, 7, expect.anything());
    expect(screen.queryByText(/\b12\b.*\b7\b/)).toBeNull();
  });

  it("asks the worker again in the other frame when the frame changes", async () => {
    mount();
    await screen.findByRole("button", { name: "Open hi_cube.fits" });
    await userEvent.selectOptions(screen.getByLabelText("Frame"), "galactic");
    await waitFor(() => expect(getSkyFootprint).toHaveBeenCalledWith(1, "galactic"));
    expect(JSON.parse(localStorage.getItem("vesqira.sky.v1")!)).toMatchObject({ frame: "galactic", background: false });
  });

  it("opens a dataset from its outline and from the list", async () => {
    const { onOpen } = mount({ datasetId: null });
    await userEvent.click(await screen.findByRole("button", { name: "Open hi_cube.fits" }));
    expect(onOpen).toHaveBeenLastCalledWith(1);
    await userEvent.click(screen.getByRole("button", { name: "hi_cube.fits" }));
    expect(onOpen).toHaveBeenCalledTimes(2);
  });
});

describe("virtual subsets", () => {
  const subset = (id: number, over: Record<string, unknown> = {}) => ({ id, sourceId: 1, name: "Core", version: 2, stale: false, ...over }) as never;
  const fresh = [ds(1, "hi_cube.fits", { modifiedAt: "sub" })];

  it("draws a subset as its own window, keyed apart from a dataset with the same number, and reads it through the subset route", async () => {
    getSkyFootprint.mockImplementation((id: number) => Promise.resolve(footprint(id < 0 ? 150.1 : 150, 2)));
    const { onOpen } = mount({ datasets: [ds(3, "other.fits", { modifiedAt: "sub" }), ...fresh], subsets: [subset(3)], datasetId: null });
    const outline = await screen.findByRole("button", { name: "Open Core (subset)" });
    expect(screen.getByRole("button", { name: "Open other.fits" })).toBeInTheDocument();
    expect(getSkyFootprint.mock.calls.map((c) => c[0]).sort()).toEqual([-3, 1, 3]);
    await userEvent.click(outline);
    expect(onOpen).toHaveBeenLastCalledWith(-3);
  });

  it("asks for the open subset's cursor on the subset route, in the subset's own pixels", async () => {
    getSkyFootprint.mockImplementation((id: number) => Promise.resolve(footprint(id < 0 ? 150.1 : 150, 2)));
    mount({ datasets: fresh, subsets: [subset(5)], datasetId: -5, probe: { x: 2, y: 4 } });
    expect(await screen.findByText(/RA 150\.25/)).toBeInTheDocument();
    expect(getSkyPoint).toHaveBeenCalledWith(-5, "icrs", 2, 4, expect.anything());
  });

  it("lists a stale subset with its reason and never asks the worker for it", async () => {
    mount({ datasets: fresh, subsets: [subset(7, { stale: true })], datasetId: -7, probe: { x: 1, y: 1 } });
    expect(await screen.findByText(/Core \(subset\): the file this subset was defined on has changed.*The cursor has no sky position here/)).toBeInTheDocument();
    expect(getSkyFootprint.mock.calls.map((c) => c[0])).not.toContain(-7);
    expect(getSkyPoint).not.toHaveBeenCalled();
  });

  it("leaves out a subset whose source is not in the list or is a catalog", async () => {
    mount({ datasets: [...fresh, catalog], subsets: [subset(8, { sourceId: 99 }), subset(9, { sourceId: 3 })], datasetId: null });
    await screen.findByRole("button", { name: "Open hi_cube.fits" });
    expect(screen.queryByText(/Core \(subset\)/)).toBeNull();
  });
});

describe("consent for the survey background", () => {
  it("names the server and what it receives before anything is contacted, and Cancel contacts nothing", async () => {
    const fetchSpy = vi.fn();
    vi.stubGlobal("fetch", fetchSpy);
    mount();
    await userEvent.click(screen.getByRole("button", { name: "Choose a survey image…" }));
    const group = screen.getByRole("group", { name: "Contact a server" });
    expect(within(group).getByText(contactNotice(SURVEYS[0]!))).toBeInTheDocument();
    expect(within(group).getByText(contactNotice(SURVEYS[0]!))).toHaveTextContent(/alasky\.cds\.unistra\.fr.*the parts of the sky you look at/);
    await userEvent.click(within(group).getByRole("button", { name: "Cancel" }));
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(loadAladin).not.toHaveBeenCalled();
    expect(JSON.parse(localStorage.getItem("vesqira.sky.v1") ?? "{}").background ?? false).toBe(false);
  });

  it("falls back to the plain grid, saying why, when the server cannot be reached", async () => {
    const fetchSpy = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
    vi.stubGlobal("fetch", fetchSpy);
    mount();
    await userEvent.click(screen.getByRole("button", { name: "Choose a survey image…" }));
    await userEvent.click(screen.getByRole("button", { name: /Contact alasky\.cds\.unistra\.fr and turn on/ }));
    expect(await screen.findByText(/Not shown: .*The footprints stay on the plain grid\./)).toBeInTheDocument();
    // the one request is the survey's own properties file; the library is not loaded when it fails
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(String(fetchSpy.mock.calls[0]![0])).toBe(`${SURVEYS[0]!.url}/properties`);
    expect(loadAladin).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Open hi_cube.fits" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Turn off" }));
    expect(screen.getByText(/Off\. Nothing leaves this computer/)).toBeInTheDocument();
  });

  it("labels a shown background as context only, with its id and provider", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, status: 200 }));
    const shape = { add: vi.fn(), removeAll: vi.fn(), show: vi.fn() };
    const al = {
      gotoRaDec: vi.fn(), setFoV: vi.fn(), getFoV: () => [1, 1], getRaDec: () => [150, 2], setBaseImageLayer: vi.fn(), addOverlay: vi.fn(), on: vi.fn(),
    };
    loadAladin.mockResolvedValue({ aladin: () => al, graphicOverlay: () => shape, polygon: vi.fn(), circle: vi.fn(), init: Promise.resolve() });
    mount();
    await userEvent.click(screen.getByRole("button", { name: "Choose a survey image…" }));
    await userEvent.click(screen.getByRole("button", { name: /Contact alasky\.cds\.unistra\.fr and turn on/ }));
    expect(await screen.findByRole("note")).toHaveTextContent(/Background: DSS2 colour .*CDS\/P\/DSS2\/color.*CDS, Strasbourg\. Context only.*never used in a measurement/);
    expect(JSON.parse(localStorage.getItem("vesqira.sky.v1")!)).toMatchObject({ background: true });
    expect(screen.getByRole("checkbox", { name: "Include the survey image in the saved file" })).not.toBeChecked();
  });
});

describe("stored settings", () => {
  it("default to off and to ICRS, and unreadable storage is the default", () => {
    expect(DEFAULTS.background).toBe(false);
    expect(parseSettings(null)).toEqual(DEFAULTS);
    expect(parseSettings("{not json")).toEqual(DEFAULTS);
    expect(parseSettings('{"background":"yes","frame":"fk4","survey":"evil/host"}')).toEqual(DEFAULTS);
    expect(parseSettings('{"background":true,"frame":"galactic","survey":"CDS/P/2MASS/color"}')).toEqual({ background: true, frame: "galactic", survey: "CDS/P/2MASS/color" });
  });

  it("only ever name surveys on the one CDS host", () => {
    expect(new Set(SURVEYS.map((s) => new URL(s.url).host))).toEqual(new Set(["alasky.cds.unistra.fr"]));
    expect(SURVEYS.every((s) => s.host === new URL(s.url).host)).toBe(true);
  });
});

describe("save as figure", () => {
  it("sends what is drawn, with no survey while the background is off, and says the figure has none", async () => {
    mount();
    await screen.findByRole("button", { name: "Open hi_cube.fits" });
    await userEvent.click(screen.getByRole("button", { name: "Save as figure" }));
    expect(await screen.findByText(/It has no survey image/)).toBeInTheDocument();
    const body = createSkyFigure.mock.calls[0]![0];
    expect(body.frame).toBe("icrs");
    expect(body.survey).toBeNull();
    // only the outline that was drawn: the dataset without a WCS and the catalog are not in it
    expect(body.footprints).toEqual([{ kind: "dataset", id: 1 }]);
    expect(body.fovDeg).toBeGreaterThan(0);
    expect(renderPng.mock.calls[0]![1]).toBeNull();
    expect(renderPng.mock.calls[0]![0].lines.join(" ")).not.toMatch(/Background|HiPS/);
  });

  it("names a subset by its own id, apart from a dataset with the same number", async () => {
    getSkyFootprint.mockImplementation((id: number) => Promise.resolve(footprint(id < 0 ? 151 : 150, 2)));
    const sub = { id: 1, sourceId: 1, name: "Core", version: 2, stale: false } as never;
    mount({ datasets: [ds(1, "hi_cube.fits", { modifiedAt: "sf" })], subsets: [sub] });
    await screen.findByRole("button", { name: "Open Core (subset)" });
    await userEvent.click(screen.getByRole("button", { name: "Save as figure" }));
    await waitFor(() => expect(createSkyFigure).toHaveBeenCalled());
    expect(createSkyFigure.mock.calls[0]![0].footprints).toEqual([{ kind: "dataset", id: 1 }, { kind: "subset", id: 1 }]);
  });

  it("records the view Aladin shows when the survey is in the picture, not the panel's own view", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, status: 200 }));
    const shape = { add: vi.fn(), removeAll: vi.fn(), show: vi.fn() };
    // the person dragged and zoomed Aladin: its centre (in the view's own frame) and width are not the fitted view's
    const al = {
      gotoRaDec: vi.fn(), setFoV: vi.fn(), getFoV: () => [0.37, 0.37], getRaDec: () => [1, 1], wasm: { getCenter: () => [150.2, 2.3] },
      setBaseImageLayer: vi.fn(), addOverlay: vi.fn(), on: vi.fn(), getViewDataURL: vi.fn().mockResolvedValue("data:image/png;base64,AAAA"),
    };
    loadAladin.mockResolvedValue({ aladin: () => al, graphicOverlay: () => shape, polygon: vi.fn(), circle: vi.fn(), init: Promise.resolve() });
    mount();
    await userEvent.click(screen.getByRole("button", { name: "Choose a survey image…" }));
    await userEvent.click(screen.getByRole("button", { name: /Contact alasky\.cds\.unistra\.fr and turn on/ }));
    await userEvent.click(await screen.findByRole("checkbox", { name: "Include the survey image in the saved file" }));
    await userEvent.click(screen.getByRole("button", { name: "Save as figure" }));
    await screen.findByText(/It includes the survey image, which is context only/);
    const body = createSkyFigure.mock.calls[0]![0];
    expect(body.centre).toEqual({ lon: 150.2, lat: 2.3 });
    expect(body.fovDeg).toBe(0.37);
    expect(body.survey).toBe("CDS/P/DSS2/color");
    expect(renderPng.mock.calls[0]![1]).toBe("data:image/png;base64,AAAA");
    expect(renderPng.mock.calls[0]![0].lines.join(" ")).toMatch(/22\.2|0\.37|22\.2″/);
  });

  it("makes no figure when Aladin will not say where it is looking", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, status: 200 }));
    const shape = { add: vi.fn(), removeAll: vi.fn(), show: vi.fn() };
    const al = { gotoRaDec: vi.fn(), setFoV: vi.fn(), getFoV: () => [1, 1], getRaDec: () => [150, 2], setBaseImageLayer: vi.fn(), addOverlay: vi.fn(), on: vi.fn(), getViewDataURL: vi.fn() };
    loadAladin.mockResolvedValue({ aladin: () => al, graphicOverlay: () => shape, polygon: vi.fn(), circle: vi.fn(), init: Promise.resolve() });
    mount();
    await userEvent.click(screen.getByRole("button", { name: "Choose a survey image…" }));
    await userEvent.click(screen.getByRole("button", { name: /Contact alasky\.cds\.unistra\.fr and turn on/ }));
    await userEvent.click(await screen.findByRole("checkbox", { name: "Include the survey image in the saved file" }));
    await userEvent.click(screen.getByRole("button", { name: "Save as figure" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/position could not be read/);
    expect(createSkyFigure).not.toHaveBeenCalled();
  });

  it("shows the server's refusal and offers no saved message", async () => {
    createSkyFigure.mockRejectedValue(new Error("a footprint could not be read"));
    mount();
    await screen.findByRole("button", { name: "Open hi_cube.fits" });
    await userEvent.click(screen.getByRole("button", { name: "Save as figure" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("a footprint could not be read");
    expect(screen.queryByText(/Saved as the figure/)).toBeNull();
  });
});
