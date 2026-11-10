import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { Graph as GraphData } from "../api/reports";
import { Graph } from "./Graph";
import { layoutGraph, nearestInDirection } from "./graphLayout";

const data: GraphData = {
  nodes: [
    { id: "report:1", kind: "report", label: "Baseline", published: true },
    { id: "report:2", kind: "report", label: "Weekly notes" },
    { id: "run:a", kind: "run", label: "2026-11-08_a1f3c" },
    { id: "figure:r1", kind: "figure", label: "Fig 1" },
    { id: "figure:gone", kind: "figure", label: "Old figure", missing: true },
  ],
  edges: [
    { from: "report:2", to: "report:1", kind: "link" },
    { from: "report:1", to: "run:a", kind: "embeds" },
    { from: "report:1", to: "figure:r1", kind: "embeds" },
    { from: "report:2", to: "figure:gone", kind: "embeds" },
  ],
};

describe("layoutGraph", () => {
  it("is deterministic and stays inside the unit box", () => {
    const a = layoutGraph(data.nodes, data.edges);
    const b = layoutGraph(data.nodes, data.edges);
    expect(a).toEqual(b);
    expect(Object.keys(a)).toHaveLength(5);
    for (const p of Object.values(a)) {
      expect(p.x).toBeGreaterThanOrEqual(0);
      expect(p.x).toBeLessThanOrEqual(1);
      expect(p.y).toBeGreaterThanOrEqual(0);
      expect(p.y).toBeLessThanOrEqual(1);
    }
  });
  it("handles 0 and 1 nodes", () => {
    expect(layoutGraph([], [])).toEqual({});
    expect(layoutGraph([data.nodes[0]!], [])).toEqual({ "report:1": { x: 0.5, y: 0.5 } });
  });
  it("lays out ~200 nodes and falls back to a circle above 400", () => {
    const many = (n: number) => Array.from({ length: n }, (_, i) => ({ id: `report:${i}`, kind: "report" as const, label: `R${i}` }));
    const ring = (n: number) => Array.from({ length: n - 1 }, (_, i) => ({ from: `report:${i}`, to: `report:${i + 1}`, kind: "link" as const }));
    const p = layoutGraph(many(200), ring(200));
    expect(Object.keys(p)).toHaveLength(200);
    expect(Object.values(p).every((q) => Number.isFinite(q.x) && Number.isFinite(q.y))).toBe(true);
    expect(Object.keys(layoutGraph(many(450), []))).toHaveLength(450);
  });
  it("finds the nearest node in a direction", () => {
    const pos = { a: { x: 0, y: 0 }, b: { x: 10, y: 1 }, c: { x: 3, y: 0 }, d: { x: 0, y: -5 } };
    expect(nearestInDirection("a", pos, "right")).toBe("c");
    expect(nearestInDirection("a", pos, "up")).toBe("d");
    expect(nearestInDirection("a", pos, "left")).toBeNull();
  });
});

describe("Graph", () => {
  it("shows the empty state", () => {
    render(<Graph graph={{ nodes: [], edges: [] }} onOpen={() => {}} />);
    expect(screen.getByText(/No links yet/)).toBeInTheDocument();
  });

  it("opens from a list item and from Enter on a node", () => {
    const onOpen = vi.fn();
    render(<Graph graph={data} current="report:1" onOpen={onOpen} />);
    const list = screen.getByRole("complementary", { name: "Nodes" });
    fireEvent.click(within(list).getByRole("button", { name: /2026-11-08_a1f3c/ }));
    expect(onOpen).toHaveBeenLastCalledWith(data.nodes[2]);
    fireEvent.keyDown(screen.getByRole("button", { name: /^Report: Weekly notes, 2 links/ }), { key: "Enter" });
    expect(onOpen).toHaveBeenLastCalledWith(data.nodes[1]);
    fireEvent.click(screen.getByRole("button", { name: /^Figure: Fig 1/ }));
    expect(onOpen).toHaveBeenLastCalledWith(data.nodes[3]);
  });

  it("marks the current report", () => {
    render(<Graph graph={data} current="report:1" onOpen={() => {}} />);
    expect(screen.getByRole("button", { name: /^Report: Baseline, 3 links/ })).toHaveAttribute("aria-current", "true");
    expect(screen.getByText("this page")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Report: Weekly notes/ })).not.toHaveAttribute("aria-current");
  });

  it("labels a missing node as missing", () => {
    render(<Graph graph={data} onOpen={() => {}} />);
    expect(screen.getByRole("button", { name: /^Figure: Old figure \(missing\)/ })).toBeInTheDocument();
  });

  it("filters runs and figures and their edges", () => {
    const { container } = render(<Graph graph={data} onOpen={() => {}} />);
    expect(container.querySelectorAll("line")).toHaveLength(4);
    fireEvent.click(screen.getByRole("checkbox", { name: "Runs" }));
    expect(container.querySelector('[data-kind="run"]')).toBeNull();
    expect(container.querySelectorAll("line")).toHaveLength(3);
    fireEvent.click(screen.getByRole("checkbox", { name: "Figures" }));
    expect(container.querySelector('[data-kind="figure"]')).toBeNull();
    expect(container.querySelectorAll("line")).toHaveLength(1);
    expect(container.querySelectorAll('[data-kind="report"]')).toHaveLength(2);
  });
});

describe("unlinked reports", () => {
  it("are left out of the picture by default but stay in the list", () => {
    const g: GraphData = { nodes: [...data.nodes, { id: "report:9", kind: "report", label: "People" }], edges: data.edges };
    const { container } = render(<Graph graph={g} onOpen={() => {}} />);
    expect(container.querySelector('svg [data-id="report:9"]')).toBeNull();
    expect(screen.getAllByText("People").length).toBeGreaterThan(0); // the list
    fireEvent.click(screen.getByLabelText("Unlinked reports (1)"));
    expect(container.querySelector('svg [data-id="report:9"]')).not.toBeNull();
  });
});
