import { Button, Checkbox, Input, Pill, Select } from "../ui";
import type { Compat, Dataset } from "../api/catalog";
import { FACETS } from "../scene/link";
import type { LinkFacet, LinkGroup, LinkPrefs, Pill as PillSpec } from "../scene/link";
import { datasetLabels } from "./datasetLabels";
import s from "./CompareBar.module.css";

/** Links only maps that are known to share a grid. Otherwise they sit side by side, unlinked, with the reasons. */
export function linkState(compat: Compat | null, loading: boolean): { linked: boolean; text: string; tone: "ok" | "warn" | "neutral" } {
  if (loading || !compat) return { linked: false, text: "Checking the grids…", tone: "neutral" };
  return compat.compatible
    ? { linked: true, text: "Same grid: cursor and channel are linked", tone: "ok" }
    : { linked: false, text: "Different or unconfirmed grids: not linked, nothing is resampled", tone: "warn" };
}

/** Everything the link controls show and change (ADR 0042, section 4): the group (null when not linked), what the person chose, the pill's statements. */
export type LinkControls = {
  group: LinkGroup | null;
  prefs: LinkPrefs;
  onPrefs: (patch: Partial<LinkPrefs>) => void;
  pills: PillSpec[];
  /** one sentence about the active pane's channel, for example "B channel 41 is 0.12 km/s from A's 8.00 km/s" */
  channelLine: string | null;
};

export function CompareBar({ hosts = [], datasets, selectedId, compareId, onChange, compat, loading, error, shared = false, canShare = false, shareReason = null, onShared, link }: {
  hosts?: { id: number; name: string }[];
  datasets: Dataset[];
  selectedId: number;
  compareId: number | null;
  onChange: (id: number | null) => void;
  compat: Compat | null;
  loading: boolean;
  error: string | null;
  shared?: boolean;
  canShare?: boolean;
  shareReason?: string | null;
  onShared?: (on: boolean) => void;
  link?: LinkControls;
}) {
  const others = datasets.filter((d) => d.id !== selectedId);
  const labels = datasetLabels(others, (id) => hosts.find((h) => h.id === id)?.name ?? "host", { hostAlways: true });
  const st = linkState(compat, loading);
  const both = !!link && link.prefs.members.a && link.prefs.members.b;
  // the pill: the group's own statements when linked; otherwise why they are not (different grids, or a pane left)
  const pills = link?.group
    ? link.pills
    : link && !both && (link.prefs.wcs || compat?.compatible)
      ? [{ text: "Not linked: a pane left the group", tone: "neutral" as const }]
      : [{ text: st.text, tone: st.tone }];
  return (
    <div className={s.bar}>
      <Select label="Compare with" value={compareId ?? ""} onChange={(e) => onChange(e.target.value ? Number(e.target.value) : null)}>
        <option value="">Nothing</option>
        {others.map((d) => <option key={d.id} value={d.id}>{labels.get(d.id)}</option>)}
      </Select>
      {compareId !== null && onShared ? (
        <div className={s.state}>
          <Checkbox label="Same colour scale for both maps" checked={shared} disabled={!canShare && !shared} onChange={(e) => onShared(e.target.checked)} />
          {shareReason ? <span className={s.why}>{shareReason}</span> : null}
        </div>
      ) : null}
      {compareId !== null ? (
        <div className={s.state}>
          {error ? <span role="alert" className={s.err}>{error}</span> : pills.map((p) => <Pill key={p.text} tone={p.tone}>{p.text}</Pill>)}
          {link && !error && !link.group && compat && !compat.compatible && !link.prefs.wcs ? (
            <Button onClick={() => link.onPrefs({ wcs: true, members: { a: true, b: true } })}>Link by WCS</Button>
          ) : null}
          {link && !error && !link.group && !both && (link.prefs.wcs || compat?.compatible) ? (
            <Button onClick={() => link.onPrefs({ members: { a: true, b: true } })}>Link again</Button>
          ) : null}
          {compat && !compat.compatible && !link?.group ? (
            <details className={s.why}>
              <summary>Why ({compat.reasons.length})</summary>
              <ul>{compat.reasons.map((r) => <li key={r}>{r}</li>)}</ul>
            </details>
          ) : null}
        </div>
      ) : null}
      {link?.group ? (
        <div className={s.state} role="group" aria-label="Link group">
          <Input label="Group name" value={link.prefs.name} maxLength={60} onChange={(e) => link.onPrefs({ name: e.target.value })} />
          {FACETS.map((f) => (
            <Checkbox key={f.id} label={f.label} checked={link.group!.facets[f.id as LinkFacet] && link.prefs.facets[f.id as LinkFacet]} disabled={f.id === "channel" && !!link.group!.channelRefusal} onChange={(e) => link.onPrefs({ facets: { ...link.prefs.facets, [f.id]: e.target.checked } })} />
          ))}
          <Button onClick={() => link.onPrefs({ members: { ...link.prefs.members, a: false } })}>Unlink map A</Button>
          <Button onClick={() => link.onPrefs({ members: { ...link.prefs.members, b: false } })}>Unlink map B</Button>
        </div>
      ) : null}
      {link?.group && link.channelLine ? <span className={s.why} role="status">{link.channelLine}</span> : null}
    </div>
  );
}
