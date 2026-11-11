import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { Button } from ".";
import { Modal, ModalActions } from "./Modal";

function Harness({ onClose = () => {}, fullscreen = false }: { onClose?: () => void; fullscreen?: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>Open it</button>
      <button type="button">Behind</button>
      {open ? (
        <Modal label="A dialog" fullscreen={fullscreen} onClose={() => { onClose(); setOpen(false); }}>
          <h2>Title</h2>
          <input aria-label="First" />
          <ModalActions>
            <Button>Cancel</Button>
            <Button variant="danger">Delete</Button>
          </ModalActions>
        </Modal>
      ) : null}
    </>
  );
}

describe("Modal", () => {
  it("is one shared surface: a scrim around a card, the actions in the card", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole("button", { name: "Open it" }));
    const dialog = await screen.findByRole("dialog", { name: "A dialog" });
    expect(dialog.className).toMatch(/card/); // the card is Modal's own, not each caller's
    expect(dialog.parentElement?.parentElement?.className).toMatch(/scrim/);
    expect(dialog).toContainElement(screen.getByRole("button", { name: "Delete" }));
  });

  it("keeps Tab and Shift+Tab inside the dialog", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole("button", { name: "Open it" }));
    const dialog = await screen.findByRole("dialog");
    for (let i = 0; i < 6; i++) {
      await user.tab();
      expect(dialog.contains(document.activeElement)).toBe(true);
    }
    for (let i = 0; i < 6; i++) {
      await user.tab({ shift: true });
      expect(dialog.contains(document.activeElement)).toBe(true);
    }
  });

  it("asks to close on Escape and gives focus back to what opened it", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<Harness onClose={onClose} />);
    const opener = screen.getByRole("button", { name: "Open it" });
    await user.click(opener);
    await screen.findByRole("dialog");
    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(opener).toHaveFocus());
  });

  it("has no card when fullscreen: the content owns the viewport", async () => {
    const user = userEvent.setup();
    render(<Harness fullscreen />);
    await user.click(screen.getByRole("button", { name: "Open it" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog.className).not.toMatch(/card/);
  });
});

describe("Button danger", () => {
  it("is a variant of the one Button, and a disabled one is not a red button", () => {
    render(<><Button variant="danger">Delete from catalog</Button><Button variant="danger" disabled>Deleting…</Button></>);
    expect(screen.getByRole("button", { name: "Delete from catalog" }).className).toMatch(/danger/);
    expect(screen.getByRole("button", { name: "Deleting…" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Delete from catalog" })).toHaveAttribute("type", "button");
  });
});
