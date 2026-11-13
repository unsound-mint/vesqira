import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import type { Dataset } from "../api/catalog";
import { MasksPanel } from "./MasksPanel";
import { UnitControl } from "./UnitControl";
import { withFacts } from "./testDatasets";

const ds = withFacts({ id: 7, name: "a.fits", path: "/d/a.fits", hostId: 0, kind: "fits", header: { bunit: null, bunit_status: "unresolved" }, interpretation: null, unitDeclaration: null }) as unknown as Dataset;
const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { "Content-Type": "application/json" } });

afterEach(() => vi.unstubAllGlobals());

describe("making a mask", () => {
  it("sends a saturated threshold with where it comes from, and refuses to send one without", async () => {
    const calls: { url: string; body: unknown }[] = [];
    vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => {
      calls.push({ url: String(url), body: init?.body ? JSON.parse(String(init.body)) : null });
      return Promise.resolve(init?.method === "POST"
        ? json({ id: 3, datasetId: 7, type: "saturated", name: "Saturated ≥ 100", flagged: 1, total: 4, width: 2, height: 2, channels: 0, tier: "declared", mappingVersion: "", method: "computed", rule: {}, meaning: "", author: "user", createdAt: "", sourceIdentity: "", fileSize: 1, sha256: "", stale: false }, 201)
        : json({ masks: [] }));
    }));
    const user = userEvent.setup();
    render(<MasksPanel dataset={ds} datasets={[ds]} />);
    await screen.findByText("No mask yet.");
    await user.selectOptions(screen.getByLabelText("Make a mask"), "saturated");
    await user.type(screen.getByLabelText(/Threshold \(flag where value/), "100");
    await user.click(screen.getByRole("button", { name: "Make the mask" }));
    expect(await screen.findByText("Say where the threshold comes from.")).toBeInTheDocument();
    expect(calls.some((c) => c.url.endsWith("/masks") && c.body !== null)).toBe(false);
    await user.type(screen.getByLabelText("Where the threshold comes from"), "datasheet");
    await user.click(screen.getByRole("button", { name: "Make the mask" }));
    await waitFor(() => expect(calls.find((c) => c.body !== null)?.body).toEqual({ method: "saturated", threshold: 100, source: "datasheet" }));
  });

  it("says what a mask means: its type, tier and rule, and offers to forget it (the file stays)", async () => {
    const m = { id: 3, datasetId: 7, type: "low_snr", name: "Low S/N < 3", flagged: 5, total: 20, width: 5, height: 4, channels: 0, tier: "declared", mappingVersion: "", method: "computed", rule: { rule: "value / sigma < 3", sigma: { source: "a robust estimate" } }, meaning: "", author: "user", createdAt: "", sourceIdentity: "", fileSize: 1, sha256: "", stale: false };
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(json({ masks: [m] }))));
    render(<MasksPanel dataset={ds} datasets={[ds]} />);
    const row = await screen.findByTestId("mask-row");
    expect(row).toHaveTextContent("Low signal-to-noise (low signal-to-noise (diagonal /))");
    expect(row).toHaveTextContent("declared");
    expect(row).toHaveTextContent("5 of 20 pixels");
    expect(row).toHaveTextContent("value / sigma < 3. a robust estimate");
    expect(row).toHaveTextContent("its file stays on disk");
  });

  it("offers masks for a file on a host, made and kept there (ADR 0064)", () => {
    render(<MasksPanel dataset={{ ...ds, hostId: 2 } as Dataset} datasets={[]} />);
    expect(screen.queryByText(/made on this computer/)).toBeNull();
    expect(screen.getByTestId("masks-on-host")).toHaveTextContent(/made and kept there/);
    expect(screen.getByRole("button", { name: "Make the mask" })).toBeInTheDocument();
  });
});

describe("tiers in the unit control", () => {
  it("shows a unit a rule proposed as inferred, with the rule and its mapping version", () => {
    const d = withFacts({ ...ds, unitDeclaration: { unit: "K km/s", declaredBy: "rohsa-run-adapter", declaredAt: "2026-11-10T10:00:00Z", stale: false, tier: "inferred", mappingVersion: "1" } }) as unknown as Dataset;
    render(<UnitControl dataset={d} onDeclare={vi.fn()} onWithdraw={vi.fn()} />);
    expect(screen.getByText("inferred by rohsa-run-adapter/1")).toBeInTheDocument();
  });
});
