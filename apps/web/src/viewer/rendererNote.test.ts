import { cpuNote, getRendererNote, setRendererNote, subscribeRendererNote } from "./glRenderer";

describe("the renderer note", () => {
  it("names the reason drawing is on the CPU, with the user's own setting first", () => {
    expect(cpuNote({ forced: true, supported: true, failed: false })).toBe("CPU drawing (set by you)");
    expect(cpuNote({ forced: true, supported: false, failed: true })).toBe("CPU drawing (set by you)");
    expect(cpuNote({ forced: false, supported: false, failed: false })).toBe("CPU drawing: no WebGL2");
    expect(cpuNote({ forced: false, supported: true, failed: true })).toBe("CPU drawing: graphics failed");
    expect(cpuNote({ forced: false, supported: true, failed: false })).toBe("CPU drawing: plane too large for graphics");
  });

  it("tells subscribers only when the note changes, and null means the GPU is drawing", () => {
    const seen: (string | null)[] = [];
    const off = subscribeRendererNote(() => seen.push(getRendererNote()));
    setRendererNote("CPU drawing: no WebGL2");
    setRendererNote("CPU drawing: no WebGL2");
    setRendererNote(null);
    off();
    setRendererNote("ignored");
    setRendererNote(null);
    expect(seen).toEqual(["CPU drawing: no WebGL2", null]);
  });
});
