import { describe, expect, it } from "vitest";
import type { Provenance } from "../api/boards";
import { channelText } from "./channelText";

const prov = (channel: number | undefined, lengths: number[]) =>
  ({ source: { name: "m.fits", headerDeclaredByFile: { axes: lengths.map((length, i) => ({ axis: i + 1, length })) } }, view: { kind: "plane", channel } }) as unknown as Provenance;

describe("the channel a figure names", () => {
  it("numbers channels from 1, as the viewer does, though the provenance stores them from 0", () => {
    expect(channelText(prov(0, [64, 64, 40]))).toBe("channel 1");
    expect(channelText(prov(39, [64, 64, 40]))).toBe("channel 40");
  });
  it("says nothing for a map with one plane", () => {
    expect(channelText(prov(0, [2174, 2174]))).toBe("");
    expect(channelText(prov(0, [64, 64, 1]))).toBe("");
  });
  it("still names the channel when the header axes are not known", () => {
    expect(channelText(prov(3, []))).toBe("channel 4");
    expect(channelText(prov(undefined, [64, 64, 40]))).toBe("");
  });
});
