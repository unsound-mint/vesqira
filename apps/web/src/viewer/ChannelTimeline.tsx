import type { KeyboardEvent } from "react";
import { Pill, Slider } from "../ui";
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
      <div className={s.head}>
        <strong className={s.big}>{channel + 1}</strong>
        <span className={s.of}>/ {channels}</span>
        <span className={s.spec}>
          {spec.name} <span className="mono">{spec.resolved ? spectralText(here, spec.unit) : "unresolved"}</span>
        </span>
        <Pill tone={flag.tone}>{flag.text}</Pill>
      </div>
      <Slider
        label="Channel"
        min={0}
        max={channels - 1}
        step={1}
        value={channel}
        valueText={channelLabel(channel, channels, spec, here)}
        hideValue
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
      {view.flags.declared && flagged.length > 0 ? (
        <p className={s.line}>
          <span className={s.flagMark} aria-hidden="true">▲</span> Flagged in the file: {flagged.slice(0, LISTED_FLAGS).map((c) => c + 1).join(", ")}
          {flagged.length > LISTED_FLAGS ? ` and ${flagged.length - LISTED_FLAGS} more` : ""} (data kept)
        </p>
      ) : null}
      {view.flags.note ? <p className={s.note}>{view.flags.note}</p> : null}
      <p className={s.hint}>
        <kbd>←</kbd> <kbd>→</kbd> step one channel · <kbd>Page Up</kbd> <kbd>Page Down</kbd> step ten · <kbd>Home</kbd> <kbd>End</kbd> first and last
      </p>
    </div>
  );
}
