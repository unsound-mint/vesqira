import { describe, expect, it } from "vitest";
import { angleConvention } from "./Panels";
import type { Viewer } from "./useViewer";

const decl = (over: Partial<NonNullable<Viewer["interp"]>>) =>
  ({ kind: "axial_orientation", unit: "rad", reliabilityId: null, frame: null, quantity: null, declaredBy: "user", declaredAt: "2026-11-13T00:00:00Z", stale: false, tier: "declared", ...over }) as NonNullable<Viewer["interp"]>;

describe("the axial legend's angle convention", () => {
  it("states only what was declared, and asks for a frame that was not", () => {
    expect(angleConvention(decl({ frame: null }))).toMatch(/not declared/);
    expect(angleConvention(decl({ frame: null }))).not.toMatch(/VGT|assum/);
    expect(angleConvention(decl({ frame: "pixel" }))).toMatch(/\+x axis towards \+y.*as you declared it/);
    expect(angleConvention(decl({ frame: "sky", declaredBy: "profile:VGT.jl@1" }))).toMatch(/east of north.*file’s header declares it/);
    expect(angleConvention(null)).toBe("");
  });
});
