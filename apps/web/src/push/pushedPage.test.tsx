import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PushedPage } from "./PushedPage";
import { PermissionsContext } from "../account/permissions";

const json = (b: unknown) => new Response(JSON.stringify(b), { status: 200, headers: { "Content-Type": "application/json" } });
const sha = "a".repeat(64);
const summary = { id: 7, kind: "figure", title: "Peak channel", hash: "b".repeat(64), pushedBy: "acct-1", pushedAt: "2026-11-13T10:00:00Z", archived: false };

// The answer of GET /api/pushed/{id} as internal/server/push_group.go writes it: {item: Detail, pushedByName}, where Detail is
// the summary's fields, the pushed envelope under item, the lineage with its total, and the references.
const detail = {
  item: {
    ...summary,
    item: { hash: summary.hash, kind: "figure", origin: "c".repeat(32), title: "Peak channel", doc: {}, assets: [sha], references: [{ role: "source", name: "cube.fits", sizeBytes: 9 }], omitted: 2 },
    lineage: [{ ...summary, id: 6, hash: "d".repeat(64), pushedAt: "2026-11-12T10:00:00Z", supersededBy: 7 }, summary],
    lineageTotal: 2,
    references: [{ n: 0, role: "source", name: "cube.fits", sizeBytes: 9, match: "probable", datasetId: 3, datasetName: "server-cube.fits" }],
  },
  pushedByName: "Ada",
};

afterEach(() => vi.unstubAllGlobals());

describe("pushed work", () => {
  it("renders an item's detail from the server's answer", async () => {
    vi.stubGlobal("fetch", vi.fn(async (url: string) => (url === "/api/pushed" ? json({ items: [{ ...summary, pushedByName: "Ada" }] }) : json(detail))));
    render(<PermissionsContext.Provider value={() => true}><PushedPage /></PermissionsContext.Provider>);
    await userEvent.click(await screen.findByRole("button", { name: "Peak channel" }));
    const card = await screen.findByRole("region", { name: "Pushed figure: Peak channel" });
    expect(card).toHaveTextContent("pushed by Ada");
    expect(card).toHaveTextContent("2 run setting(s)");
    expect(card).toHaveTextContent("cube.fits (source): probably on this server as server-cube.fits (sampled; not verified)");
    expect(screen.getByRole("img", { name: "Picture of Peak channel" })).toHaveAttribute("src", `/api/pushed/7/assets/${sha}`);
    expect(card).toHaveTextContent("(current)");
  });
});
