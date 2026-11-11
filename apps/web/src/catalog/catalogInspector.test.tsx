import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import type { CatalogDataset, CatalogReport } from "../api/catalog";
import { CatalogInspector } from "./CatalogInspector";
import { CatalogView } from "./CatalogView";

const columns = [
  { name: "Star", dtype: "string", unit: null, scalar: true },
  { name: "RA(J2000)", dtype: "string", unit: null, scalar: true },
  { name: "DEC(J2000)", dtype: "string", unit: null, scalar: true },
  { name: "PA(deg)", dtype: "float", unit: null, scalar: true },
  { name: "sPA(deg)", dtype: "float", unit: "deg", scalar: true },
] as const;

const dataset = (over: Partial<CatalogDataset> = {}): CatalogDataset =>
  ({
    id: 4, path: "/data/mf_stars_LMC.dat", name: "mf_stars_LMC.dat", sizeBytes: 12000, modifiedAt: "2026-11-11T10:00:00Z", addedAt: "", hostId: 0, nature: "",
    interpretation: null, unitDeclaration: null, kind: "catalog", catalogDeclaration: null,
    header: { kind: "catalog", format: "tsv", rows: 214, columns: [...columns] },
    ...over,
  }) as unknown as CatalogDataset;

const declaration = (over: Record<string, unknown> = {}) => ({
  lon: "RA(J2000)", lat: "DEC(J2000)", format: "sexagesimal_hours_degrees", frame: "fk5", equinox: "J2000",
  angle: { column: "PA(deg)", unit: "deg", frame: "sky", quantity: "starlight_polarization", sigmaColumn: "sPA(deg)" }, values: [],
  declaredBy: "user", declaredAt: "2026-11-11T10:00:00Z", stale: false, ...over,
});

const report = (over: Partial<CatalogReport> = {}): CatalogReport =>
  ({
    format: "tsv", rows: 214, columns: [...columns], preview: { columns: [], rows: [] },
    read: { skippedTotal: 0, skipped: [], headerless: false, notes: [] },
    proposal: {
      basis: "proposed from the column names", lon: "RA(J2000)", lat: "DEC(J2000)", format: "sexagesimal_hours_degrees", format_basis: "the values look like h m s and d m s",
      frame: "fk5", frame_basis: "the column names say J2000; FK5 and ICRS differ by tens of milliarcseconds, check it", equinox: "J2000",
      angle: { column: "PA(deg)", unit: "deg", unit_basis: "the column name says (deg)", quantity: null, quantity_basis: "the file does not say whether the angle is along B" }, sigma: "sPA(deg)", notes: [],
    },
    limits: { fileBytes: 1, rows: 1, columns: 1 }, declaration: null, parse: null, parseError: null, ...over,
  }) as unknown as CatalogReport;

function stubReport(r: CatalogReport) {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(r), { status: 200, headers: { "Content-Type": "application/json" } })));
}

describe("the catalog inspector", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("shows the columns as read, with units only where the file declares them", async () => {
    stubReport(report());
    render(<CatalogInspector d={dataset()} onDeclare={vi.fn()} />);
    const table = await screen.findByRole("table", { name: /Columns as read/ });
    const rows = within(table).getAllByRole("row");
    expect(within(rows[5]!).getByText("deg")).toBeInTheDocument();
    expect(within(rows[1]!).getByText(/not declared/)).toBeInTheDocument();
    expect(screen.getByText(/Vesqira does not guess them from the names/)).toBeInTheDocument();
  });

  it("labels the proposal, fills the form from it only when asked, and saves nothing until the person declares", async () => {
    stubReport(report());
    const onDeclare = vi.fn().mockResolvedValue(null);
    const user = userEvent.setup();
    render(<CatalogInspector d={dataset()} onDeclare={onDeclare} />);
    const group = await screen.findByRole("group", { name: "Proposal from the column names" });
    expect(within(group).getByText("proposed from the column names")).toBeInTheDocument();
    expect(within(group).getByText(/the column names say J2000/)).toBeInTheDocument();
    expect(screen.getByLabelText("Longitude column")).toHaveValue(""); // not pre-filled
    expect(screen.getByLabelText("Celestial frame")).toHaveValue("");
    await user.click(screen.getByRole("button", { name: "Declare" }));
    expect(onDeclare).toHaveBeenCalledTimes(1); // the server decides what is missing; the form sends what is chosen
    expect(onDeclare.mock.calls[0]![1]).toMatchObject({ lon: "", frame: "" });
    onDeclare.mockClear();
    await user.click(within(group).getByRole("button", { name: "Fill the form from this proposal" }));
    expect(screen.getByLabelText("Longitude column")).toHaveValue("RA(J2000)");
    expect(screen.getByLabelText("Celestial frame")).toHaveValue("fk5");
    expect(screen.getByLabelText("Equinox")).toHaveValue("J2000");
    expect(screen.getByLabelText("Angle unit")).toHaveValue("deg");
    expect(screen.getByLabelText("What the angle is")).toHaveValue(""); // the names do not say it
    expect(screen.getByText(/Filled from the proposal/)).toBeInTheDocument();
    expect(onDeclare).not.toHaveBeenCalled();
    await user.selectOptions(screen.getByLabelText("What the angle is"), "starlight_polarization");
    await user.click(screen.getByRole("button", { name: "Declare" }));
    expect(onDeclare).toHaveBeenCalledWith(4, {
      lon: "RA(J2000)", lat: "DEC(J2000)", format: "sexagesimal_hours_degrees", frame: "fk5", equinox: "J2000",
      angle: { column: "PA(deg)", unit: "deg", frame: "sky", quantity: "starlight_polarization", sigmaColumn: "sPA(deg)" }, values: [],
    });
  });

  it("asks for an equinox only for FK5 and FK4, and the angle's frame is always the sky", async () => {
    stubReport(report());
    const user = userEvent.setup();
    render(<CatalogInspector d={dataset()} onDeclare={vi.fn()} />);
    await screen.findByLabelText("Celestial frame");
    expect(screen.queryByLabelText("Equinox")).toBeNull();
    await user.selectOptions(screen.getByLabelText("Celestial frame"), "fk4");
    expect(screen.getByLabelText("Equinox")).toHaveAttribute("placeholder", "B1950");
    await user.selectOptions(screen.getByLabelText("Celestial frame"), "galactic");
    expect(screen.queryByLabelText("Equinox")).toBeNull();
    await user.click(screen.getByRole("checkbox", { name: "This catalog has an angle column" }));
    expect(screen.getByText(/position angle on the sky, east of north/)).toBeInTheDocument();
    expect(screen.queryByLabelText(/pixel/i)).toBeNull();
  });

  it("shows the server's reasons when the declaration is refused, and keeps the form", async () => {
    stubReport(report());
    const onDeclare = vi.fn().mockResolvedValue("the declaration is not complete or not consistent · frame: choose the celestial frame");
    const user = userEvent.setup();
    render(<CatalogInspector d={dataset()} onDeclare={onDeclare} />);
    await screen.findByLabelText("Longitude column");
    await user.selectOptions(screen.getByLabelText("Longitude column"), "RA(J2000)");
    await user.click(screen.getByRole("button", { name: "Declare" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("choose the celestial frame");
    expect(screen.getByLabelText("Longitude column")).toHaveValue("RA(J2000)");
  });

  it("reports the rows that could not be read, with their line, and says empty cells are not zero", async () => {
    const d = dataset({ catalogDeclaration: declaration() as never });
    stubReport(report({
      declaration: declaration() as never,
      parse: {
        rows: 214, placed: 211, unreadable: 2, missing: 1,
        samples: [{ row: 16, line: 17, column: "RA(J2000)", value: "25:61:00", reason: "is not a valid angle (hours must be 0 to 24)" }, { row: 40, line: 41, column: "DEC(J2000)", value: "foo", reason: "is not a valid angle (not sexagesimal)" }],
        lon: { column: "RA(J2000)", missing: 1, invalid: 1, samples: [] }, lat: { column: "DEC(J2000)", missing: 0, invalid: 1, samples: [] },
        angle: { column: "PA(deg)", missing: 4, invalid: 0, samples: [] },
      } as never,
    }));
    render(<CatalogInspector d={d} onDeclare={vi.fn()} />);
    const status = await screen.findByRole("status", { name: "How the declaration reads the rows" });
    expect(within(status).getByText("211 of 214 rows have a position.")).toBeInTheDocument();
    expect(within(status).getByText("2 rows could not be read:")).toBeInTheDocument();
    expect(within(status).getByText(/line 17 RA\(J2000\) '25:61:00' is not a valid angle/)).toBeInTheDocument();
    expect(within(status).getByText(/1 row has no longitude or latitude.*not placed at 0/)).toBeInTheDocument();
    expect(within(status).getByText(/Angle column PA\(deg\): 4 missing/)).toBeInTheDocument();
    expect(within(status).getByText(/not a check that the frame or the unit is the right one/)).toBeInTheDocument();
  });

  it("lists lines that were not rows", async () => {
    stubReport(report({ read: { skippedTotal: 3, skipped: [{ line: 19, reason: "has 1 fields, the header has 3", text: "short" }], headerless: false, notes: [] } }));
    render(<CatalogInspector d={dataset()} onDeclare={vi.fn()} />);
    expect(await screen.findByText("3 lines are not rows and were left out:")).toBeInTheDocument();
    expect(screen.getByText(/line 19 has 1 fields, the header has 3/)).toBeInTheDocument();
    expect(screen.getByText("and 2 more")).toBeInTheDocument();
  });

  it("says a stale declaration is not being used and offers to confirm or withdraw it", async () => {
    const stale = declaration({ stale: true });
    const onDeclare = vi.fn().mockResolvedValue(null);
    stubReport(report({ declaration: stale as never }));
    const user = userEvent.setup();
    render(<CatalogInspector d={dataset({ catalogDeclaration: stale as never })} onDeclare={onDeclare} />);
    expect(await screen.findByText(/changed since you declared this, so the declaration is not being used/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "It still applies" }));
    expect(onDeclare).toHaveBeenCalledWith(4, expect.objectContaining({ lon: "RA(J2000)", frame: "fk5", equinox: "J2000" }));
    await user.click(screen.getByRole("button", { name: "Withdraw" }));
    expect(onDeclare).toHaveBeenLastCalledWith(4, null);
  });

  it("shows a read failure instead of an empty form", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: "the file changed since it was registered; register it again" }), { status: 409, headers: { "Content-Type": "application/json" } })));
    render(<CatalogInspector d={dataset()} onDeclare={vi.fn()} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("changed since it was registered");
    expect(screen.queryByLabelText("Longitude column")).toBeNull();
  });
});

describe("the catalog table", () => {
  afterEach(() => vi.unstubAllGlobals());

  const page = (offset: number) => ({
    offset, total: 214, columns: ["Star", "RA(J2000)", "PA(deg)"], lines: [2 + offset, 3 + offset],
    rows: [["67-2", "4 47 07.65", 25], ["69-7", "4 49 15.83", null]],
  });

  it("shows rows with their file line, a dash for a missing value, and the declared role in the header", async () => {
    const fetchMock = vi.fn(async (url: string) => new Response(JSON.stringify(page(Number(new URL(url, "http://x").searchParams.get("offset")))), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    render(<CatalogView dataset={dataset({ catalogDeclaration: declaration() as never })} />);
    const table = await screen.findByRole("table");
    expect(within(table).getByText("67-2")).toBeInTheDocument();
    expect(within(table).getByTitle("missing value")).toHaveTextContent("—");
    expect(within(table).getByRole("columnheader", { name: /RA\(J2000\).*longitude/ })).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: /PA\(deg\).*angle/ })).toBeInTheDocument();
    expect(within(table).getByRole("rowheader", { name: "2" })).toBeInTheDocument(); // the line in the file
    expect(screen.getByText(/Rows 1 to 100 of 214/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Previous 100" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Next 100" }));
    await waitFor(() => expect(fetchMock).toHaveBeenLastCalledWith("/api/datasets/4/catalog/rows?offset=100&limit=100", undefined));
    expect(await screen.findByText(/Rows 101 to 200 of 214/)).toBeInTheDocument();
  });

  it("shows why the rows cannot be read", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: "the file changed since it was registered; register it again" }), { status: 409, headers: { "Content-Type": "application/json" } })));
    render(<CatalogView dataset={dataset()} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("changed since it was registered");
  });
});
