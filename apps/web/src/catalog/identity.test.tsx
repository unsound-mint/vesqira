import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import { ApiError } from "../api/http";
import type { Dataset, DatasetLocation, LocationList, NewerNotice } from "../api/catalog";
import * as api from "../api/catalog";
import { BoardsView } from "../boards/BoardsView";
import { Explorer } from "./Explorer";
import { differsText, noticeText } from "./identity";
import { LocationsPanel } from "./LocationsPanel";
import { NewerNotices } from "./NewerNotices";

vi.mock("../api/catalog", async (orig) => ({
  ...(await orig<typeof import("../api/catalog")>()),
  listLocations: vi.fn(),
  addLocation: vi.fn(),
  removeLocation: vi.fn(),
  fingerprintLocation: vi.fn(),
}));

const stamp = (value: string, by = "this computer") => ({ value, at: "2026-11-10T10:00:00Z", by });
const loc = (over: Partial<DatasetLocation>): DatasetLocation => ({
  id: 1, hostId: 0, path: "/data/a.fits", kind: "registered", primary: true, link: "registered", sizeBytes: 2048, modifiedAt: "", addedAt: "",
  sampledFingerprint: null, fullSha256: null, ...over,
});
const list = (locations: DatasetLocation[], over: Partial<LocationList> = {}): LocationList => ({
  locations, hashing: {}, algorithm: "sampled/1", sampledCovers: "file size, the primary FITS header, and 16 blocks of 64 KiB at fixed offsets across the data; a change elsewhere in the file is not detected",
  fullHashCapBytes: 16 << 30, readsFrom: { locationId: 1, hostId: 0, primary: true, checked: true, reason: "the registered file" }, ...over,
});
const dataset = { id: 7, path: "/data/a.fits", name: "a.fits", hostId: 0 } as unknown as Dataset;

beforeEach(() => vi.resetAllMocks());

describe("the locations of a dataset", () => {
  it("says what is verified, what is only probable and what you declared, and never calls a sampled fingerprint a hash of the file", async () => {
    vi.mocked(api.listLocations).mockResolvedValue(list([
      loc({ id: 1, sampledFingerprint: stamp("a".repeat(64)), fullSha256: stamp("b".repeat(64)) }),
      loc({ id: 2, path: "/backup/a_copy.fits", kind: "copy", primary: false, link: "verified", sampledFingerprint: stamp("a".repeat(64)), fullSha256: stamp("b".repeat(64)) }),
      loc({ id: 3, path: "/backup/maybe.fits", kind: "copy", primary: false, link: "probable", sampledFingerprint: stamp("a".repeat(64)) }),
      loc({ id: 4, path: "/host/a.fits", hostId: 9, kind: "downloaded", primary: false, link: "declared" }),
    ]));
    render(<LocationsPanel dataset={dataset} hosts={[{ id: 9, name: "Cluster" }]} />);
    const items = await screen.findAllByRole("listitem");
    expect(items).toHaveLength(4);
    expect(within(items[0]!).getByText("registered file")).toBeInTheDocument();
    expect(within(items[1]!).getByText(/verified same bytes \(equal full SHA-256\)/)).toBeInTheDocument();
    expect(within(items[2]!).getByText(/probably the same \(sampled fingerprint\); not verified/)).toBeInTheDocument();
    expect(within(items[2]!).queryByText(/verified same bytes/)).toBeNull();
    expect(within(items[2]!).getByText(/Full SHA-256:\s*not computed/)).toBeInTheDocument();
    expect(within(items[3]!).getByText(/declared the same by you; not checked/)).toBeInTheDocument();
    expect(within(items[3]!).getByText("on Cluster")).toBeInTheDocument();
    expect(within(items[3]!).getByText("downloaded copy")).toBeInTheDocument();
    expect(within(items[3]!).getByText(/Sampled fingerprint:\s*not computed/)).toBeInTheDocument();
    // what a sampled fingerprint covers is on the screen, with the blind spot
    expect(screen.getByText(/It is not a hash of the whole file/)).toBeInTheDocument();
    expect(screen.getByText(/a change elsewhere in the file is not detected/)).toBeInTheDocument();
    expect(screen.getByTestId("reads-from")).toHaveTextContent("Reads from: a.fits on this computer. the registered file.");
  });

  it("says which location is read when it is not the registered file", async () => {
    vi.mocked(api.listLocations).mockResolvedValue(list([
      loc({ id: 1, hostId: 9, path: "/scratch/a.fits" }),
      loc({ id: 2, path: "/data/local_copy.fits", kind: "downloaded", primary: false, link: "verified", fullSha256: stamp("c".repeat(64)) }),
    ], { readsFrom: { locationId: 2, hostId: 0, primary: false, checked: true, reason: "a location verified to hold the same bytes (equal full SHA-256), because the registered file is not the better choice or is not available" } }));
    render(<LocationsPanel dataset={dataset} hosts={[{ id: 9, name: "Cluster" }]} />);
    expect(await screen.findByTestId("reads-from")).toHaveTextContent("Reads from: local_copy.fits on this computer");
    expect(screen.getByTestId("reads-from")).toHaveTextContent("verified to hold the same bytes");
  });

  it("computes fingerprints on request, shows a full hash in progress, and cancels it", async () => {
    const user = userEvent.setup();
    const l = list([loc({ id: 1 })]);
    vi.mocked(api.listLocations).mockResolvedValueOnce(l).mockResolvedValue(list([loc({ id: 1 })], { hashing: { "1": { state: "running", doneBytes: 512, totalBytes: 2048 } } }));
    vi.mocked(api.fingerprintLocation).mockResolvedValue({});
    render(<LocationsPanel dataset={dataset} />);
    await user.click(await screen.findByRole("button", { name: "Compute sampled fingerprint" }));
    expect(api.fingerprintLocation).toHaveBeenCalledWith(7, 1, "sampled");
    await waitFor(() => expect(api.listLocations).toHaveBeenCalledTimes(2));
    // the same screen, while a full hash runs
    expect(await screen.findByText("Computing the full SHA-256: 25%")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Compute full SHA-256" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(api.fingerprintLocation).toHaveBeenLastCalledWith(7, 1, "cancel");
  });

  it("starts a full hash, and shows why one was not computed", async () => {
    const user = userEvent.setup();
    vi.mocked(api.listLocations)
      .mockResolvedValueOnce(list([loc({ id: 1, sampledFingerprint: stamp("a".repeat(64)) })]))
      .mockResolvedValue(list([loc({ id: 1, sampledFingerprint: stamp("a".repeat(64)) })], { hashing: { "1": { state: "failed", doneBytes: 0, totalBytes: 2048, error: "the file changed while it was being read" } } }));
    vi.mocked(api.fingerprintLocation).mockResolvedValue({});
    render(<LocationsPanel dataset={dataset} />);
    await user.click(await screen.findByRole("button", { name: "Compute full SHA-256" }));
    expect(api.fingerprintLocation).toHaveBeenCalledWith(7, 1, "full");
    expect(await screen.findByRole("alert")).toHaveTextContent("The full SHA-256 was not computed: the file changed while it was being read");
  });

  it("links another file only through the server and shows its refusal; a declaration is the person's checkbox", async () => {
    const user = userEvent.setup();
    vi.mocked(api.listLocations).mockResolvedValue(list([loc({ id: 1 })]));
    vi.mocked(api.addLocation)
      .mockRejectedValueOnce(new ApiError("This file's bytes differ from a.fits (a size, sampled fingerprint or SHA-256 differs), so it is not another location of this dataset.", [], 409, { code: "bytes_differ" }))
      .mockResolvedValueOnce(loc({ id: 5, path: "/backup/b.fits", primary: false, link: "declared" }));
    render(<LocationsPanel dataset={dataset} />);
    await user.type(await screen.findByLabelText("Add another file that holds this dataset"), "/backup/b.fits");
    await user.click(screen.getByRole("button", { name: "Add location" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("bytes differ from a.fits");
    expect(api.addLocation).toHaveBeenCalledWith(7, { path: "/backup/b.fits", declare: false });
    await user.click(screen.getByRole("checkbox", { name: /I say it is the same dataset/ }));
    await user.click(screen.getByRole("button", { name: "Add location" }));
    expect(api.addLocation).toHaveBeenLastCalledWith(7, { path: "/backup/b.fits", declare: true });
    await waitFor(() => expect(screen.getByLabelText("Add another file that holds this dataset")).toHaveValue(""));
  });

  it("forgets a location without touching the file, and never offers that for the registered file", async () => {
    const user = userEvent.setup();
    vi.mocked(api.listLocations).mockResolvedValue(list([loc({ id: 1 }), loc({ id: 2, path: "/backup/b.fits", primary: false, link: "verified", kind: "copy" })]));
    vi.mocked(api.removeLocation).mockResolvedValue({ removed: 2 });
    const onChanged = vi.fn();
    render(<LocationsPanel dataset={dataset} onChanged={onChanged} />);
    const buttons = await screen.findAllByRole("button", { name: "Remove location" });
    expect(buttons).toHaveLength(1);
    expect(buttons[0]).toHaveAttribute("title", expect.stringContaining("the file is not deleted"));
    await user.click(buttons[0]!);
    expect(api.removeLocation).toHaveBeenCalledWith(7, 2);
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
  });
});

const n = (over: Partial<NewerNotice>): NewerNotice => ({ reason: "newer_file", name: "psi_x_1.fits", path: "/runs/psi_x_1.fits", datasetId: 0, sizeBytes: 1, modifiedAt: "", differs: ["size", "time", "fingerprint"], ...over });

describe("a newer result is available", () => {
  it("says what differs, in words, and that nothing was swapped", () => {
    expect(noticeText(n({}))).toBe("A newer result exists: psi_x_1.fits (what differs: size, modification time, sampled fingerprint). Nothing was swapped; this still points at the older file.");
    expect(noticeText(n({ reason: "changed_in_place", differs: ["time"] }))).toBe("The file at this path changed since it was registered (what differs: modification time).");
    expect(noticeText(n({ reason: "figure_data_changed", differs: ["fingerprint"] }))).toContain("not what it was drawn from (what differs: sampled fingerprint). The picture is unchanged.");
    expect(differsText([])).toBe("size, time and fingerprint are the same");
  });

  it("offers to register an unregistered newer file, or to open a registered one", async () => {
    const user = userEvent.setup();
    const onRegister = vi.fn();
    const onOpen = vi.fn();
    const { rerender } = render(<NewerNotices notices={[n({})]} onRegister={onRegister} onOpen={onOpen} />);
    expect(screen.getByRole("status", { name: "A newer result is available" })).toHaveTextContent("A newer result exists: psi_x_1.fits");
    await user.click(screen.getByRole("button", { name: "Register psi_x_1.fits" }));
    expect(onRegister).toHaveBeenCalledWith("/runs/psi_x_1.fits");
    rerender(<NewerNotices notices={[n({ datasetId: 12 })]} onRegister={onRegister} onOpen={onOpen} />);
    await user.click(screen.getByRole("button", { name: "Open psi_x_1.fits" }));
    expect(onOpen).toHaveBeenCalledWith(12);
    rerender(<NewerNotices notices={[]} />);
    expect(screen.queryByRole("status")).toBeNull();
  });
});

describe("registering a file whose content a dataset already holds", () => {
  const offer = { path: "/data/copy_of_a.fits", matches: [{ datasetId: 3, locationId: 3, name: "a.fits", relation: "verified" as const }, { datasetId: 4, locationId: 4, name: "b.fits", relation: "probable" as const }] };

  it("offers a location instead of a duplicate, labels verified and probable honestly, and lets the person register it separately", async () => {
    const user = userEvent.setup();
    const onAddLocation = vi.fn().mockResolvedValue(null);
    const onRegister = vi.fn().mockResolvedValue(null);
    render(<Explorer datasets={[]} selectedId={null} loading={false} error={null} onSelect={() => {}} onRegister={onRegister} offer={offer} onAddLocation={onAddLocation} onDismissOffer={() => {}} />);
    const box = screen.getByRole("status", { name: "A dataset already holds this content" });
    expect(box).toHaveTextContent("Nothing was registered yet");
    expect(within(box).getByText(/verified same bytes \(equal full SHA-256\)/)).toBeInTheDocument();
    expect(within(box).getByText(/probably the same \(equal size and sampled fingerprint\); not verified/)).toBeInTheDocument();
    await user.click(within(box).getByRole("button", { name: "Add as a location of b.fits" }));
    expect(onAddLocation).toHaveBeenCalledWith(4, "/data/copy_of_a.fits");
    await user.click(within(box).getByRole("button", { name: "Register as a separate dataset" }));
    expect(onRegister).toHaveBeenCalledWith("/data/copy_of_a.fits", { asNew: true });
  });

  it("is read from the server's 409 answer, and any other 409 is an ordinary error", () => {
    const offered = new ApiError("A dataset already holds this content.", [], 409, { code: "same_content", matches: offer.matches });
    expect(api.sameContentOffer(offered)).toEqual(offer.matches);
    expect(api.sameContentOffer(new ApiError("the file changed", [], 409, { error: "x" }))).toBeNull();
    expect(api.sameContentOffer(new ApiError("nope", [], 403, { code: "same_content", matches: [] }))).toBeNull();
    expect(api.sameContentOffer(new Error("x"))).toBeNull();
  });
});

describe("boards and figures built on older data", () => {
  it("each figure says what differs and the board says how many are affected", async () => {
    const boards = await import("../api/boards");
    const fig = (id: number, title: string) => ({ id, datasetId: 1, title, caption: "", createdAt: "", revision: `r${id}`.padEnd(12, "0"), previewSha: "", width: 8, height: 6, provenance: { source: { name: "psi.fits", sizeBytes: 1, modifiedAt: "", identity: "a reference", headerDeclaredByFile: {} }, view: { kind: "plane", channel: 1 }, statements: [] } });
    const board = { id: 1, title: "Draft", createdAt: "", updatedAt: "", items: [{ position: 0, kind: "figure", note: "", figure: fig(11, "Old psi") }, { position: 1, kind: "figure", note: "", figure: fig(12, "Fresh") }] };
    vi.spyOn(boards, "listBoards").mockResolvedValue([board] as never);
    vi.spyOn(boards, "listSnapshots").mockResolvedValue([]);
    render(<BoardsView newer={{ "11": [n({})] }} />);
    expect(await screen.findByText("Old psi")).toBeInTheDocument();
    expect(screen.getByText(/1 figure on this board was drawn from data that has a newer result or has changed/)).toBeInTheDocument();
    expect(screen.getAllByText(/A newer result exists: psi_x_1.fits/)).toHaveLength(1);
  });
});
