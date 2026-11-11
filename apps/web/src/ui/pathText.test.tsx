import { render, screen } from "@testing-library/react";
import { PathText, splitPath } from "./PathText";

describe("path text", () => {
  it("keeps the file and its folder whole and lets the head shorten", () => {
    expect(splitPath("/home/spectre/Projects/x/configs/vgt.toml")).toEqual({ head: "/home/spectre/Projects/x", tail: "/configs/vgt.toml" });
  });
  it("shows a short path whole", () => {
    expect(splitPath("/a/b.txt")).toEqual({ head: "", tail: "/a/b.txt" });
  });
  it("carries the full path as the tooltip and for assistive technology", () => {
    render(<PathText path="/home/spectre/Projects/x/configs/vgt.toml" />);
    expect(screen.getByTitle("/home/spectre/Projects/x/configs/vgt.toml")).toBeInTheDocument();
    expect(screen.getByText("/home/spectre/Projects/x/configs/vgt.toml")).toBeInTheDocument();
  });
});
