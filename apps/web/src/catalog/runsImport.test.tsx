import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { RunsList } from "./RunsList";

// Importing a ROHSA fit from a host (ADR 0046): the person says where the fit is; the path is the host's and goes with its id.
describe("importing a run", () => {
  it("imports from this computer by default and offers no host choice when there are no hosts", async () => {
    const onImport = vi.fn().mockResolvedValue(null);
    render(<RunsList runs={[]} onImport={onImport} onOpen={() => {}} />);
    expect(screen.queryByLabelText("Where the run is")).toBeNull();
    await userEvent.type(screen.getByLabelText(/Import a run/), "/data/run1");
    await userEvent.click(screen.getByRole("button", { name: "Import run" }));
    expect(onImport).toHaveBeenCalledWith("/data/run1", undefined);
  });

  it("sends the host's id with the path of a ROHSA fit on that host, and says the .dat stays there", async () => {
    const onImport = vi.fn().mockResolvedValue(null);
    render(<RunsList runs={[]} hosts={[{ id: 7, name: "Cluster" }]} onImport={onImport} onOpen={() => {}} />);
    await userEvent.selectOptions(screen.getByLabelText("Where the run is"), "7");
    expect(screen.getByText(/the \.dat stays there and is indexed there/)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText(/ROHSA fit from the host/), "/shared/home/u/run/region_a");
    await userEvent.click(screen.getByRole("button", { name: "Import run" }));
    expect(onImport).toHaveBeenCalledWith("/shared/home/u/run/region_a", 7);
  });

  it("shows the host's refusal", async () => {
    const onImport = vi.fn().mockResolvedValue("this folder holds several ROHSA fits (a, b); give the .dat to import");
    render(<RunsList runs={[]} hosts={[{ id: 7, name: "Cluster" }]} onImport={onImport} onOpen={() => {}} />);
    await userEvent.selectOptions(screen.getByLabelText("Where the run is"), "7");
    await userEvent.type(screen.getByLabelText(/ROHSA fit from the host/), "/shared/x");
    await userEvent.click(screen.getByRole("button", { name: "Import run" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("several ROHSA fits");
  });
});
