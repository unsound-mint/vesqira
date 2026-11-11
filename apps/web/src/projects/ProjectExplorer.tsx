import { useCallback, useEffect, useState } from "react";
import { Database, File, FileCode, Folder, FolderOpen, GitBranch, Settings2 } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { projectTree } from "../api/projects";
import type { ProjectSummary, TreeEntry, TreeGroup } from "../api/projects";
import { looksEditable } from "../api/editor";
import { bytes } from "../catalog/format";
import { Button, Input, Section, TreeRow } from "../ui";
import { location, num } from "./format";
import { ScanStatus } from "./ScanStatus";
import { isScanning } from "./useProject";
import s from "./Projects.module.css";

const PAGE = 200;
const GROUPS: { group: TreeGroup; title: string; count: (c: ProjectSummary["counts"]) => number; icon: LucideIcon }[] = [
  { group: "runs", title: "Runs", count: (c) => c.runs, icon: GitBranch },
  { group: "data", title: "Data", count: (c) => c.data, icon: Database },
  { group: "configs", title: "Configs", count: (c) => c.configs, icon: Settings2 },
  { group: "jobs", title: "Job templates", count: (c) => c.jobs, icon: FileCode },
];

/** What the run shows instead of its folder name: the settings that differ from its siblings. */
export const runLabel = (e: TreeEntry): string => (e.run && e.run.differs.length > 0 ? e.run.differs.map((d) => `${d.key}=${d.value}`).join(" · ") : e.name);

/** Plain filter over the rows already loaded: every word must occur in the name or in a setting. */
export function matchEntry(e: TreeEntry, q: string): boolean {
  const words = q.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  const hay = [e.name, e.path, ...(e.run?.settings ?? []).map((x) => `${x.key}=${x.value}`)].join(" ").toLowerCase();
  return words.every((w) => hay.includes(w));
}

/** `onEditFile` is given only when the server has the editor on (ADR 0029); it receives the file's absolute path and the project's host (0: this computer). */
type Handlers = { selectedDatasetId: number | null; onOpenRun: (runId: number) => void; onSelectDataset: (id: number) => void; onEditFile?: (path: string, hostId: number) => void };

/** One page of a group, or of one folder in it; more pages on request. Children are fetched when the folder is opened. */
function Listing({ project, version, group, path, level, filter, ...h }: Handlers & { project: ProjectSummary; version: number; group: TreeGroup; path: string; level: number; filter: string }) {
  const [entries, setEntries] = useState<TreeEntry[]>([]);
  const [next, setNext] = useState<number | null>(null);
  const [state, setState] = useState<"loading" | "ready" | "error">("loading");
  const [error, setError] = useState("");

  const load = useCallback(
    async (offset: number, replace: boolean) => {
      setState("loading");
      try {
        const page = await projectTree(project.id, group, path, offset, PAGE);
        setEntries((cur) => (replace ? page.entries : [...cur, ...page.entries]));
        setNext(page.nextOffset);
        setState("ready");
      } catch (e) {
        setError(e instanceof Error ? e.message : "Could not list this folder.");
        setState("error");
      }
    },
    [project.id, group, path],
  );
  useEffect(() => {
    void load(0, true);
  }, [load, version]);

  const shown = filter ? entries.filter((e) => e.kind === "dir" || matchEntry(e, filter)) : entries;
  return (
    <div role="list" aria-label={path ? `${path}` : GROUPS.find((g) => g.group === group)?.title}>
      {shown.map((e) => (
        <div role="listitem" key={e.path}>
          <Entry project={project} version={version} group={group} entry={e} level={level} filter={filter} {...h} />
        </div>
      ))}
      {state === "loading" ? <p className={s.rowNote} style={{ paddingLeft: `calc(var(--sp-3) + ${level} * var(--sp-4))` }}>Loading…</p> : null}
      {state === "error" ? <p role="alert" className={s.rowErr}>{error} <button type="button" className={s.link} onClick={() => void load(next ?? 0, next === null)}>Try again</button></p> : null}
      {state === "ready" && next !== null ? <div style={{ paddingLeft: `calc(var(--sp-3) + ${level} * var(--sp-4))` }}><Button onClick={() => void load(next, false)}>Load more</Button></div> : null}
      {state === "ready" && entries.length === 0 ? <p className={s.rowNote} style={{ paddingLeft: `calc(var(--sp-3) + ${level} * var(--sp-4))` }}>Nothing here.</p> : null}
      {state === "ready" && entries.length > 0 && shown.length === 0 ? <p className={s.rowNote}>No loaded row matches{next !== null ? "; load more to search further" : ""}.</p> : null}
    </div>
  );
}

function Entry({ project, version, group, entry: e, level, filter, selectedDatasetId, onOpenRun, onSelectDataset, onEditFile }: Handlers & { project: ProjectSummary; version: number; group: TreeGroup; entry: TreeEntry; level: number; filter: string }) {
  const [open, setOpen] = useState(false);
  if (e.kind === "dir") {
    return (
      <>
        <TreeRow icon={open ? FolderOpen : Folder} level={level} expanded={open} onSelect={() => setOpen(!open)} trailing={e.count !== null ? num(e.count) : undefined}>{e.name}</TreeRow>
        {open ? <Listing project={project} version={version} group={group} path={e.path} level={level + 1} filter={filter} selectedDatasetId={selectedDatasetId} onOpenRun={onOpenRun} onSelectDataset={onSelectDataset} onEditFile={onEditFile} /> : null}
      </>
    );
  }
  if (e.kind === "run" && e.run) {
    const runId = e.run.runId;
    const full = `${e.path}${e.run.record ? ` · record: ${e.run.record}` : " · marked by you"}`;
    return (
      <TreeRow
        icon={GitBranch}
        level={level}
        title={runId === null ? `${full}\nNot imported as a Vesqira run (only VGT runs are).` : full}
        onSelect={runId !== null ? () => onOpenRun(runId) : undefined}
        trailing={e.run.recordKind === "manual" ? "by you" : e.run.recordKind}
      >
        {runLabel(e)}
      </TreeRow>
    );
  }
  const f = e.file;
  // Only a config or job file of a project (on this computer or on a host), that is not marked read-only, and that the server would open as text.
  const editPath = onEditFile && (e.kind === "config" || e.kind === "job") && !f?.readOnly && looksEditable(e.name) ? `${project.path.replace(/\/+$/, "")}/${e.path}` : null;
  const datasetId = f?.datasetId ?? null;
  const trailing = !f ? "" : !f.viewer ? "no viewer yet" : datasetId === null ? "header not read" : bytes(f.size);
  return (
    <TreeRow
      icon={e.kind === "data" ? Database : File}
      level={level}
      selected={datasetId !== null && datasetId === selectedDatasetId}
      title={`${e.path}${editPath ? " · opens in the editor" : ""}${f?.role ? ` · ${f.role}${f.readOnly ? ", read-only" : ""}` : ""}${f && f.viewer && datasetId === null ? "\nFITS header not registered yet or changed since the scan." : ""}`}
      onSelect={datasetId !== null ? () => onSelectDataset(datasetId) : editPath ? () => onEditFile?.(editPath, project.hostId) : undefined}
      trailing={e.kind === "data" ? trailing : f ? bytes(f.size) : undefined}
    >
      {e.name}
    </TreeRow>
  );
}

function Group({ project, version, def, filter, setFilter, ...h }: Handlers & { project: ProjectSummary; version: number; def: (typeof GROUPS)[number]; filter: string; setFilter: (v: string) => void }) {
  const n = def.count(project.counts);
  const [opened, setOpened] = useState(n > 0 && (def.group === "runs" || def.group === "data"));
  return (
    <Section title={`${def.title} · ${num(n)}`} defaultOpen={opened} onToggle={(o) => o && setOpened(true)}>
      {opened ? (
        <>
          {def.group === "runs" && n > 5 ? <Input label="Filter runs" placeholder="ker=8, 0.17, run name" value={filter} onChange={(e) => setFilter(e.target.value)} spellCheck={false} /> : null}
          {n === 0 && project.scanned ? <p className={s.rowNote}>None found. Anything else can be marked by hand.</p> : <Listing project={project} version={version} group={def.group} path="" level={0} filter={def.group === "runs" ? filter : ""} {...h} />}
        </>
      ) : null}
    </Section>
  );
}

/** Screen 3: the open project in the explorer, grouped, lazy and paged. */
export function ProjectExplorer({ project, version, hostName, selectedDatasetId, onOpenRun, onSelectDataset, onEditFile, onOverview, onImport, onCancel }: Handlers & { project: ProjectSummary; version: number; hostName: (id: number) => string; onOverview: () => void; onImport: () => void; onCancel: () => void }) {
  const [filter, setFilter] = useState("");
  const ch = project.changes;
  const rules = project.importRules;
  return (
    <div className={s.explorer}>
      <div className={s.projectHead}>
        <p className={s.projectName} title={project.path}>{project.name}</p>
        <p className={s.projectWhere} title={project.path}>{location(project, hostName)}</p>
        <ScanStatus scan={project.scan} onCancel={onCancel} />
        {ch && ch.added + ch.removed + ch.changed > 0 ? <p className={s.rowNote}>Since last scan: {num(ch.added)} added · {num(ch.changed)} changed · {num(ch.removed)} removed</p> : null}
        {!project.projectFile.exists ? <p className={s.rowNote}>No .vesqira/project.toml yet.</p> : null}
        {rules && rules.rules > 0 && (rules.new > 0 || rules.pending > 0) ? (
          <p className={s.rowNote} role="status">
            {rules.new > 0 ? `${num(rules.new)} new ${rules.new === 1 ? "file matches" : "files match"} your rules.` : `${num(rules.pending)} ${rules.pending === 1 ? "file matches" : "files match"} your rules, not registered yet.`}{" "}
            <button type="button" className={s.link} onClick={onImport}>Review</button>
          </p>
        ) : null}
        <div className={s.actions}>
          <Button onClick={onOverview}>What is in this folder</Button>
          <Button onClick={onImport}>Import rules…</Button>
        </div>
      </div>
      {!project.scanned ? <p className={s.rowNote}>{isScanning(project) ? "Reading the folder. The groups appear when the first scan has finished." : "Not scanned yet."}</p> : null}
      {project.scanned ? GROUPS.map((def) => <Group key={def.group} project={project} version={version} def={def} filter={filter} setFilter={setFilter} selectedDatasetId={selectedDatasetId} onOpenRun={onOpenRun} onSelectDataset={onSelectDataset} onEditFile={onEditFile} />) : null}
    </div>
  );
}
