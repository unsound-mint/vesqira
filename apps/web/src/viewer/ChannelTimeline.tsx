import type { KeyboardEvent } from "react";
import { Slider } from "../ui";
import type { Viewer } from "./useViewer";
import { channelLabel, flagState, spectralText } from "./format";
import s from "./ChannelTimeline.module.css";

const LISTED_FLAGS = 12;

/** Channel scrubber. Arrows step 1, Page Up/Down step 10, Home/End jump to the ends; flagged channels are marked. */
export function ChannelTimeline({ v }: { v: Viewer }) {
  const { view, channels, channel } = v;
  if (!view) return <p className={s.note}>{v.viewError ?? "Reading the cube layout…"}</p>;
  if (view.spectral === null || channels <= 1) {
    return <p className={s.note}>This file has a single plane: there is no channel axis to scrub.</p>;
  }
  const flagged = view.flags.channels;
  const flag = flagState(view, v.plane);
  const spec = view.spectral;
  const here = v.plane && v.plane.head.channel === channel ? v.plane.head.spectral_value : null;

  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (v.onChannelKey(e.key)) e.preventDefault();
  }

  return (
    <div className={s.wrap}>
      <Slider
        label="Channel"
        min={0}
        max={channels - 1}
        step={1}
        value={channel}
        valueText={channelLabel(channel, channels, spec, here)}
        onChange={(e) => v.setChannel(Number(e.target.value))}
        onKeyDown={onKeyDown}
      />
      {view.flags.declared && flagged.length > 0 ? (
        <div className={s.strip} role="img" aria-label={`Flagged channels: ${flagged.map((c) => c + 1).join(", ")}`}>
          {flagged.map((c) => (
            <span key={c} className={s.tick} style={{ left: `${((c + 0.5) / channels) * 100}%` }} />
          ))}
        </div>
      ) : null}
      <p className={s.line}>
        <span>
          {spec.name} <span className="mono">{spec.resolved ? spectralText(here, spec.unit) : "unresolved"}</span>
        </span>
        <span>{flag.text}</span>
        {view.flags.declared && flagged.length > 0 ? (
          <span>
            Provider-flagged: {flagged.slice(0, LISTED_FLAGS).map((c) => c + 1).join(", ")}
            {flagged.length > LISTED_FLAGS ? ` and ${flagged.length - LISTED_FLAGS} more` : ""} (data kept)
          </span>
        ) : null}
      </p>
      {view.flags.note ? <p className={s.note}>{view.flags.note}</p> : null}
      <p className={s.hint}>
        <kbd>←</kbd> <kbd>→</kbd> step one channel · <kbd>Page Up</kbd> <kbd>Page Down</kbd> step ten · <kbd>Home</kbd> <kbd>End</kbd> first and last
      </p>
    </div>
  );
}
