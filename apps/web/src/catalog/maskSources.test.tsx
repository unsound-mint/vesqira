import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import type { Dataset } from "../api/catalog";
import { Explorer } from "./Explorer";
import { MasksPanel } from "./MasksPanel";

const make = (id: number, name: string, over: Record<string, unknown> = {}) =>
  ({ id, name, path: `/d/${name}`, hostId: 0, kind: "fits", sizeBytes: 100, header: { bunit: null, bunit_status: "unresolved" }, interpretation: null, unitDeclaration: null, ...over }) as unknown as Dataset;
const json = (b: unknown) => new Response(JSON.stringify(b), { status: 200, headers: { "Content-Type": "application/json" } });

const psi = make(1, "psi.fits");
const maskFile = make(2, "psi_mask.fits", { maskSourceFor: [{ id: 1, name: "psi.fits" }] });
const other = make(3, "other_mask.fits", { maskSourceFor: [{ id: 1, name: "psi.fits" }] });

const names = () => screen.getAllByRole("listitem").map((l) => l.textContent ?? "");
const explorer = (datasets: Dataset[], selectedId: number | null = null) =>
  render(<Explorer datasets={datasets} selectedId={selectedId} loading={false} error={null} onSelect={() => {}} onRegister={async () => null} />);

afterEach(() => vi.unstubAllGlobals());

describe("mask files in the explorer", () => {
  it("hides files whose only role is being a mask, says how many, and shows them on request", async () => {
    const user = userEvent.setup();
    explorer([psi, maskFile, other]);
    expect(names()).toHaveLength(1);
    expect(names()[0]).toContain("psi.fits");
    expect(screen.getByTestId("mask-sources-note")).toHaveTextContent("2 mask files are hidden");
    await user.click(screen.getByRole("button", { name: "Show" }));
    expect(names()).toHaveLength(3);
    expect(names().join(" ")).toContain("mask file for psi.fits");
    expect(screen.getByRole("button", { name: "Hide them" })).toHaveAttribute("aria-pressed", "true");
    await user.click(screen.getByRole("button", { name: "Hide them" }));
    expect(names()).toHaveLength(1);
  });

  it("lists everything and shows no note when the server marks no file", () => {
    explorer([psi, make(4, "plain.fits")]);
    expect(names()).toHaveLength(2);
    expect(screen.queryByTestId("mask-sources-note")).toBeNull();
  });

  it("keeps the open dataset in the list even if it is a mask file", () => {
    explorer([psi, maskFile, other], 2);
    expect(names().join(" ")).toContain("psi_mask.fits");
    expect(screen.getByTestId("mask-sources-note")).toHaveTextContent("1 mask file is hidden");
  });

  it("counts only the files that match the filter", async () => {
    const user = userEvent.setup();
    explorer([psi, make(5, "a1.fits"), make(6, "a2.fits"), make(7, "a3.fits"), make(8, "a4.fits"), maskFile, other]);
    await user.type(screen.getByLabelText("Filter datasets"), "psi_mask");
    expect(screen.getByTestId("mask-sources-note")).toHaveTextContent("1 mask file is hidden (2 in all)");
  });
});

describe("the masks section of the target", () => {
  it("names the file an imported mask was made from", async () => {
    const m = { id: 3, datasetId: 1, type: "invalid", name: "psi_mask", flagged: 5, total: 20, width: 5, height: 4, channels: 0, tier: "declared", mappingVersion: "", method: "import", rule: { rule: "imported from psi_mask.fits" }, meaning: "", author: "rule:masks", createdAt: "", sourceIdentity: "", fileSize: 1, sha256: "", stale: false, sourceDataset: { id: 2, name: "psi_mask.fits" } };
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(json({ masks: [m] }))));
    render(<MasksPanel dataset={psi} datasets={[psi, maskFile]} />);
    expect(await screen.findByTestId("mask-source")).toHaveTextContent("Mask file: psi_mask.fits, a mask source for psi.fits");
  });
});
