import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { ResizeHandle, Section, Switch } from ".";

function Harness({ start = 280 }: { start?: number }) {
  const [v, setV] = useState(start);
  return <ResizeHandle orientation="vertical" label="Resize explorer" value={v} min={200} max={480} onChange={setV} />;
}

describe("ResizeHandle", () => {
  it("exposes separator semantics", () => {
    render(<Harness />);
    const h = screen.getByRole("separator", { name: "Resize explorer" });
    expect(h).toHaveAttribute("aria-valuenow", "280");
    expect(h).toHaveAttribute("aria-valuemin", "200");
    expect(h).toHaveAttribute("aria-valuemax", "480");
  });

  it("moves with arrows, Shift+arrows, Home and End, and clamps", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const h = screen.getByRole("separator");
    h.focus();
    await user.keyboard("{ArrowRight}");
    expect(h).toHaveAttribute("aria-valuenow", "288");
    await user.keyboard("{Shift>}{ArrowLeft}{/Shift}");
    expect(h).toHaveAttribute("aria-valuenow", "256");
    await user.keyboard("{End}");
    expect(h).toHaveAttribute("aria-valuenow", "480");
    await user.keyboard("{ArrowRight}");
    expect(h).toHaveAttribute("aria-valuenow", "480");
    await user.keyboard("{Home}");
    expect(h).toHaveAttribute("aria-valuenow", "200");
  });
});

describe("ResizeHandle direction", () => {
  function Insp() {
    const [v, setV] = useState(360);
    return <ResizeHandle orientation="vertical" direction={-1} label="Resize inspector" value={v} min={280} max={560} onChange={setV} />;
  }
  function Tl() {
    const [v, setV] = useState(132);
    return <ResizeHandle orientation="horizontal" direction={-1} label="Resize timeline" value={v} min={88} max={320} onChange={setV} />;
  }

  it("a right-hand panel grows when the handle moves left", async () => {
    const user = userEvent.setup();
    render(<Insp />);
    const h = screen.getByRole("separator");
    h.focus();
    await user.keyboard("{ArrowLeft}");
    expect(h).toHaveAttribute("aria-valuenow", "368");
    await user.keyboard("{ArrowRight}");
    await user.keyboard("{ArrowRight}");
    expect(h).toHaveAttribute("aria-valuenow", "352");
  });

  it("a bottom panel grows when the handle moves up", async () => {
    const user = userEvent.setup();
    render(<Tl />);
    const h = screen.getByRole("separator");
    expect(h).toHaveAttribute("aria-orientation", "horizontal");
    h.focus();
    await user.keyboard("{ArrowUp}");
    expect(h).toHaveAttribute("aria-valuenow", "140");
    await user.keyboard("{ArrowDown}{ArrowDown}");
    expect(h).toHaveAttribute("aria-valuenow", "124");
  });
});

describe("Section", () => {
  it("toggles aria-expanded and the controlled region", async () => {
    const user = userEvent.setup();
    render(<Section title="Region">content</Section>);
    const b = screen.getByRole("button", { name: /Region/ });
    expect(b).toHaveAttribute("aria-expanded", "true");
    await user.click(b);
    expect(b).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByText("content")).not.toBeVisible();
  });
});

describe("Switch", () => {
  it("is a labelled switch that toggles with Space", async () => {
    const user = userEvent.setup();
    render(<Switch label="Lock cursor" />);
    const sw = screen.getByRole("switch", { name: "Lock cursor" });
    sw.focus();
    await user.keyboard(" ");
    expect(sw).toBeChecked();
  });
});
