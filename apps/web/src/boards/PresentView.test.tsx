import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PresentView } from "./PresentView";
import type { Board } from "../api/boards";

const prov = { source: { name: "cube.fits", sizeBytes: 1, modifiedAt: "", identity: "a reference by name, size and modification time" }, view: { kind: "plane", channel: 1 }, statements: ["Header values are declared by the file and are not verified."] };
const fig = (id: number, title: string) => ({ id, datasetId: 1, title, caption: `${title} caption`, createdAt: "", revision: `r${id}`, previewSha: "a".repeat(64), width: 4, height: 3, provenance: prov });
const board: Board = { id: 1, title: "Meeting", createdAt: "", updatedAt: "", items: [
  { position: 0, figure: fig(1, "First"), note: "private reminder" },
  { position: 1, figure: fig(2, "Second"), note: "" },
] };

describe("presenting a board", () => {
  it("steps through the figures in board order and leaves with Escape", async () => {
    const onClose = vi.fn();
    render(<PresentView board={board} onClose={onClose} />);
    expect(screen.getByRole("dialog", { name: "Presenting Meeting" })).toHaveFocus();
    expect(screen.getByText("1 / 2")).toBeInTheDocument();
    expect(screen.getByRole("img", { name: /First/ })).toHaveAttribute("src", "/api/figures/1/preview");
    await userEvent.keyboard("{ArrowRight}");
    expect(screen.getByText("2 / 2")).toBeInTheDocument();
    await userEvent.keyboard("{ArrowRight}");
    expect(screen.getByText("2 / 2")).toBeInTheDocument(); // does not run past the end
    await userEvent.keyboard("{Home}");
    expect(screen.getByText("1 / 2")).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalled();
  });

  it("keeps the author's notes and provenance hidden until asked", async () => {
    render(<PresentView board={board} onClose={() => undefined} />);
    expect(screen.queryByText(/private reminder/)).toBeNull();
    expect(screen.queryByText(/Header values are declared/)).toBeNull();
    await userEvent.keyboard("n");
    expect(screen.getByText(/private reminder/)).toBeInTheDocument();
    await userEvent.keyboard("p");
    expect(screen.getByText(/Header values are declared/)).toBeInTheDocument();
  });
});
