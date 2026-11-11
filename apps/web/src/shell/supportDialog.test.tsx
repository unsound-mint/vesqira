import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import SupportDialog from "./SupportDialog";

afterEach(() => vi.unstubAllGlobals());

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const preview = (id: string) => ({ id, bytes: 2048, files: [{ name: "versions.txt", bytes: 20, text: "api 0.1 worker 0.1" }, { name: "workload.json", bytes: 30, text: '{"classes":[]}' }] });

function stub(handler: (url: string, init?: RequestInit) => Response) {
  const calls: { url: string; body: unknown }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : null });
    return handler(url, init);
  }));
  return calls;
}

describe("support dialog", () => {
  it("lists every previewed file with its text, says nothing is saved, and links the save to the preview id", async () => {
    const calls = stub(() => json(preview("p1")));
    render(<SupportDialog onClose={() => {}} />);
    expect(screen.queryByRole("link", { name: /Save bundle/ })).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Preview files" }));
    expect(await screen.findByText(/Nothing is saved yet/)).toBeInTheDocument();
    expect(calls[0]).toEqual({ url: "/api/support/preview", body: { includeDatasetNames: false } });
    expect(screen.getByText("versions.txt")).toBeInTheDocument();
    expect(screen.getByText("workload.json")).toBeInTheDocument();
    expect(screen.getByText("api 0.1 worker 0.1")).toBeInTheDocument();
    const save = screen.getByRole("link", { name: "Save bundle (.zip)" });
    expect(save).toHaveAttribute("href", "/api/support/bundle/p1");
    expect(save).toHaveAttribute("download", "vesqira-support.zip");
  });

  it("needs a new preview after the checkbox changes", async () => {
    const calls = stub(() => json(preview("p1")));
    render(<SupportDialog onClose={() => {}} />);
    await userEvent.click(screen.getByRole("button", { name: "Preview files" }));
    await screen.findByRole("link", { name: /Save bundle/ });
    await userEvent.click(screen.getByLabelText(/Include dataset names/));
    expect(screen.queryByRole("link", { name: /Save bundle/ })).toBeNull();
    expect(screen.queryByText("versions.txt")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Preview files" }));
    await screen.findByRole("link", { name: /Save bundle/ });
    expect(calls.map((c) => c.body)).toEqual([{ includeDatasetNames: false }, { includeDatasetNames: true }]);
  });

  it("says in words that an expired preview is gone", async () => {
    stub((url) => (url.startsWith("/api/support/bundle") ? new Response("not found", { status: 404, headers: { "Content-Type": "text/plain" } }) : json(preview("p1"))));
    render(<SupportDialog onClose={() => {}} />);
    await userEvent.click(screen.getByRole("button", { name: "Preview files" }));
    await userEvent.click(await screen.findByRole("link", { name: /Save bundle/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent("This preview expired; preview again");
    await waitFor(() => expect(screen.queryByRole("link", { name: /Save bundle/ })).toBeNull());
  });

  it("shows the server's words when the preview fails", async () => {
    stub(() => json({ error: "support bundle unavailable" }, 500));
    render(<SupportDialog onClose={() => {}} />);
    await userEvent.click(screen.getByRole("button", { name: "Preview files" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("support bundle unavailable");
  });
});
