import { getMaskPlane, listMasks, maskTypeLabel } from "../api/masks";
import type { Mask, MaskPlane } from "../api/masks";
import { tierText } from "../api/tier";
import type { SceneMask } from "../scene/scene";
import { maskPath } from "./maskHatch";
import type { FigureMask } from "./figure";

/** A mask shown over a plane and the plane of it that was drawn: what a saved figure hatches (ADR 0055). */
export type FigureMaskInput = { mask: Mask; plane: MaskPlane };

/** The scene's record of a hatched mask: its type, name, tier and fingerprint as the viewer showed them. The id is the owner's way back to it for "draw again"; the reviewer sees tiers in the provenance, never ids. */
export const sceneMaskOf = (m: Mask): SceneMask => ({ id: m.id, type: m.type, name: m.name, tier: m.tier, fingerprint: `sha256:${m.sha256}` });

/** The legend line of a hatched mask: its type and name, how far it is established, how many pixels it flags. Always words: the pattern alone would be a colour-free code. */
export const maskLegendLine = (m: Pick<Mask, "type" | "name" | "tier" | "mappingVersion" | "flagged">, step = 1): string =>
  `${maskTypeLabel(m.type)}: ${m.name} (${tierText(m.tier, m.mappingVersion)}; ${m.flagged.toLocaleString("en-US")} pixels flagged)${step > 1 ? `, drawn in blocks of ${step} pixels` : ""}`;

/**
 * The masks as the figure draws them: the runs of flagged pixels at `cell` figure pixels per image pixel, north up, and the legend
 * line. A plane that is not the picture's size is refused rather than drawn out of register.
 */
export function figureMaskLayers(items: FigureMaskInput[], planeW: number, planeH: number, cell: number): FigureMask[] {
  return items.map(({ mask, plane }) => {
    if (plane.width !== planeW || plane.height !== planeH) throw new Error(`The mask “${mask.name}” is not on the picture's pixel grid, so it cannot be hatched on the figure.`);
    const { d, step } = maskPath(plane.data, plane.width, plane.height, cell, 20000);
    return { type: mask.type, label: maskLegendLine(mask, step), d };
  });
}

/**
 * The masks the viewer is showing, read for the plane a figure is made from: the current list (a mask forgotten since it was shown is
 * not drawn) and each one's plane for the channel. The server checks again that each is the dataset's, fresh, and the one drawn.
 */
export async function fetchFigureMasks(datasetId: number, ids: number[], channel: number): Promise<FigureMaskInput[]> {
  if (ids.length === 0) return [];
  const list = await listMasks(datasetId);
  const wanted = ids.flatMap((id) => list.find((m) => m.id === id) ?? []);
  return Promise.all(wanted.map(async (mask) => ({ mask, plane: await getMaskPlane(mask.id, channel) })));
}
