import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DatasetInspector } from "./DatasetInspector";
import { Explorer } from "./Explorer";
import { bytes, show } from "./format";
import type { Dataset } from "../api/catalog";

const unresolved = { crval: null, crval_status: "unresolved", crpix: null, crpix_status: "unresolved", cdelt: null, cdelt_status: "unresolved", cunit: null, cunit_status: "unresolved" } as const;

const ds: Dataset = {
  interpretation: null,
  nature: "", unitDeclaration: null,
  hostId: 0,
  id: 1, path: "/data/a.fits", name: "a.fits", sizeBytes: 1536, modifiedAt: "", addedAt: "",
  header: {
    naxis: 3, bitpix: -32, bunit: null, bunit_status: "unresolved", specsys: null, radesys: null,
    axes: [
      { axis: 1, length: 32, ctype: "RA---TAN", ctype_status: "declared", ...unresolved, crval: 150, crval_status: "declared", cdelt: -0.01, cdelt_status: "declared", cunit: "deg", cunit_status: "declared" },
      { axis: 3, length: 64, ctype: "VRAD", ctype_status: "declared", ...unresolved },
    ],
  },
};

describe("format", () => {
  it("never turns unknown into zero", () => {
    expect(show(null)).toBe("—");
    expect(show(undefined)).toBe("—");
    expect(show(0)).toBe("0");
  });
  it("formats sizes", () => {
    expect(bytes(512)).toBe("512 B");
    expect(bytes(1536)).toBe("1.50 KiB");
  });
});

describe("DatasetInspector", () => {
  it("shows declared values and marks unresolved ones instead of inventing them", () => {
    render(<DatasetInspector d={ds} />);
    const rows = screen.getAllByRole("row");
    expect(within(rows[1]!).getByText("-0.01")).toBeInTheDocument();
    const spectral = rows[2]!;
    expect(within(spectral).getAllByText("unresolved")).toHaveLength(2);
    expect(within(spectral).queryByText("0")).toBeNull();
    expect(screen.getByText(/not verified/)).toBeInTheDocument();
  });
});

describe("Explorer", () => {
  it("shows the server's refusal message and keeps the typed path", async () => {
    const user = userEvent.setup();
    const onRegister = vi.fn().mockResolvedValue("path is outside the configured source roots");
    render(<Explorer datasets={[]} selectedId={null} loading={false} error={null} onSelect={() => {}} onRegister={onRegister} />);
    await user.type(screen.getByLabelText("Register a FITS file"), "/etc/passwd");
    await user.click(screen.getByRole("button", { name: "Register" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("outside the configured source roots");
    expect(screen.getByLabelText("Register a FITS file")).toHaveValue("/etc/passwd");
  });

  it("lists datasets and reports the empty state", () => {
    const { rerender } = render(<Explorer datasets={[]} selectedId={null} loading={false} error={null} onSelect={() => {}} onRegister={async () => null} />);
    expect(screen.getByText(/No datasets yet/)).toBeInTheDocument();
    rerender(<Explorer datasets={[ds]} selectedId={1} loading={false} error={null} onSelect={() => {}} onRegister={async () => null} />);
    expect(screen.getByRole("button", { name: /a\.fits/ })).toHaveAttribute("aria-current", "true");
  });
});

describe("raw header cards", () => {
  it("loads the file's own header text when opened and offers it as a text file", async () => {
    const { HeaderCards } = await import("./HeaderCards");
    const calls: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      calls.push(url);
      return new Response(JSON.stringify({ cards: ["SIMPLE  =                    T", "CTYPE3  = 'VRAD    '"], truncated: false }), { status: 200, headers: { "Content-Type": "application/json" } });
    }));
    render(<HeaderCards id={4} />);
    expect(calls).toEqual([]); // nothing is fetched until it is opened
    await userEvent.click(screen.getByText("Raw header cards"));
    expect((await screen.findByLabelText("FITS header cards")).textContent).toContain("CTYPE3  = 'VRAD    '");
    expect(calls).toEqual(["/api/datasets/4/header-cards"]);
    expect(screen.getByRole("link", { name: "Save as text" })).toHaveAttribute("href", "/api/datasets/4/header-cards?format=text");
    vi.unstubAllGlobals();
  });
});

describe("files that changed on disk", () => {
  const d = { id: 7, name: "cube.fits", path: "/data/cube.fits", sizeBytes: 10, modifiedAt: "", addedAt: "", hostId: 0, nature: "", unitDeclaration: null, interpretation: null, header: { ok: true, naxis: 3, bitpix: -32, axes: [] } } as unknown as Dataset;
  it("says so in the list and the inspector, and offers to read the file again only when it changed", async () => {
    const reread = vi.fn();
    render(<Explorer datasets={[d]} selectedId={7} problems={{ "7": "modified" }} loading={false} error={null} onSelect={() => {}} onRegister={async () => null} />);
    expect(screen.getByText(/changed on disk/)).toBeInTheDocument();
    render(<DatasetInspector d={d} problem="modified" onReread={reread} />);
    await userEvent.click(screen.getByRole("button", { name: "Read it again" }));
    expect(reread).toHaveBeenCalledTimes(1);
    render(<DatasetInspector d={d} problem="missing" onReread={reread} />);
    expect(screen.getByText(/not at its registered path/)).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Read it again" })).toHaveLength(1);
  });
});
