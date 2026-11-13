import type { Interpretation } from "../api/catalog";
import { ruleOf } from "../api/declared";

/** A declaration the file states itself: its own header cards, or a tool profile reading them (ADR 0068). */
export const isFileHeader = (by: string) => by === "vgt-file-header" || by.startsWith("profile:");

/**
 * Who stated an orientation declaration, in words. The viewer speaks to the researcher ("you"); a figure is read by others, so it
 * says "the author". A proposal by a run import or an analysis is never presented as the file's own statement.
 */
export function declaredByPhrase(by: string, voice: "you" | "author"): string {
  if (isFileHeader(by)) return "as the file’s header declares it";
  if (by === "user") return voice === "you" ? "as you declared it" : "as declared by the author";
  const rule = ruleOf(by);
  if (rule !== null) return voice === "you" ? `as your import rule ${rule} declares it` : `as the author’s import rule ${rule} declares it`;
  return `as proposed by ${by === "vgt-run-adapter" ? "the run import" : "the analysis that made this map"}, not by the file`;
}

/** The angle convention a legend or a figure states: the one declared for the map, never an assumed one (ADR 0068, slice 2). */
export function angleConvention(interp: Interpretation | null, voice: "you" | "author" = "you"): string {
  if (!interp) return "";
  const by = declaredByPhrase(interp.declaredBy, voice);
  const drawn = voice === "you" ? "on screen" : "as drawn";
  if (interp.frame === "pixel") return `Angles run from the image +x axis towards +y (counter-clockwise ${drawn}), ${by}.`;
  if (interp.frame === "sky") return `Angles are position angles east of north in the file’s celestial frame, ${by}.`;
  return voice === "you"
    ? "The frame the angles are measured in is not declared: declare it under Interpretation before reading directions from this map."
    : "The frame the angles are measured in is not declared, so directions on this figure cannot be read.";
}

/** The first caption line of an axial figure: what the values are, in which unit, and on whose word. */
export function axialCaption(interp: Interpretation | null): string {
  if (!interp) return "Axial orientation (0° = 180°). How it was declared is not known to this figure, so directions on it cannot be read.";
  const unit = interp.unit === "deg" ? "degrees" : "radians";
  return `Axial orientation (0° = 180°) in ${unit}, ${declaredByPhrase(interp.declaredBy, "author")}. ${angleConvention(interp, "author")}`;
}
