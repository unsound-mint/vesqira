import type { Provenance } from "../api/boards";

/**
 * The channel a figure was drawn from, in words, numbered from 1 as the viewer and FITS number it (the provenance stores it from 0).
 * Empty for a source with a single plane: "channel 1" of a 2-D map says nothing and reads as if there were others.
 */
export function channelText(p: Provenance): string {
  const ch = p.view.channel;
  if (ch === undefined || ch === null) return "";
  const axes = p.source.headerDeclaredByFile?.axes ?? [];
  const planes = axes.slice(2).reduce((n, a) => n * (a.length ?? 1), 1);
  if (axes.length > 0 && planes <= 1) return "";
  return `channel ${ch + 1}`;
}
