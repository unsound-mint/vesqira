import { request as call, postJSON as post, putJSON as put } from "./http";
import type { components } from "./schema";

/** One project in the recent list. hostId 0 is this computer. */
export type ProjectListItem = {
  id: number;
  hostId: number;
  path: string;
  name: string;
  openedAt: string;
  lastScanAt: string | null;
  runs: number;
  data: number;
  scanState: ScanState;
};

export type ScanState = "idle" | "scanning" | "registering" | "done" | "canceled" | "error";

/** What the scan is doing now: walking the folder, then registering FITS headers in the background. */
export type ScanProgress = {
  state: ScanState;
  entries: number;
  toRegister: number;
  registered: number;
  failed: number;
  startedAt: string | null;
  finishedAt: string | null;
  error: string;
};

export type HealthItem = {
  id: "version_control" | "run_records" | "raw_apart" | "units_declared" | "project_file";
  label: string;
  /** ok: fine; warn: partly; todo: not done; na: nothing to judge yet. */
  state: "ok" | "warn" | "todo" | "na";
  detail: string;
  /** Why it matters, in a sentence. */
  why: string;
};

export type ProjectSummary = {
  id: number;
  hostId: number;
  path: string;
  name: string;
  openedAt: string;
  lastScanAt: string | null;
  scan: ScanProgress;
  /** False until the first scan has finished; every count below is then zero and means nothing. */
  scanned: boolean;
  scannedAt: string;
  counts: {
    runs: number;
    /** Runs that have no run record: marked by you. */
    manualRuns: number;
    data: number;
    configs: number;
    jobs: number;
    runOutputs: number;
    dataFolders: number;
    byFormat: Record<string, number>;
    /** Data files in a format Vesqira cannot show yet. */
    noViewer: number;
    otherFiles: number;
    archives: { count: number; bytes: number };
    skippedDirs: number;
    /** Names marked as not to be opened: never read. */
    neverRead: number;
    /** Data files in a raw or external folder (read-only inputs). */
    roleRaw: number;
  };
  git: { present: boolean; commit: string; dirty?: number };
  truncated: boolean;
  truncatedWhy: string;
  entries: number;
  notes: string[];
  links: { outside: number; dangling: number; inside: number; examples: { path: string; state: "outside" | "dangling" | "inside" }[] };
  resultLike: string[];
  /** Compared with the previous scan by size and modification time; null on the first scan. */
  changes: { added: number; removed: number; changed: number } | null;
  projectFile: { exists: boolean; error: string; name: string; hostRepos: Record<string, string> };
  /** The proposed .vesqira/project.toml text when none exists; null when it does. Nothing is written until you confirm. */
  proposedProjectFile: string | null;
  registration: { registered: number; failed: number; errors: string[]; capped: boolean };
  /** What the saved import rules select, counted by the last scan; null when the project has no rules. A scan never registers these. */
  importRules: ImportSummary | null;
  /** The files of .vesqira/profiles/: a loaded one names its profile (name@version), a refused one says why. */
  profiles?: { file: string; profile: string; why: string }[];
  health: HealthItem[];
  needsCount: number;
};

export type TreeGroup = "runs" | "data" | "configs" | "jobs";

/** A value in a run and who says so: a run record, a folder name (inferred, never promoted) or you. */
export type RunField = { key: string; value: string; status: "declared" | "inferred" | "you"; source: string };

export type TreeFile = {
  size: number;
  mtimeNs: number;
  /** FITS, TIFF, OME-TIFF, CZI, ND, ND2, HDF5, Zarr, or "other" in a folder you declared as data. */
  format: string;
  /** Vesqira can show it. False means "no viewer yet". */
  viewer: boolean;
  /** raw, interim, processed or external, from a data/<role> layout; empty otherwise. Never a unit or a meaning. */
  role: string;
  readOnly: boolean;
  /** The registered dataset for a FITS file that is up to date, else null. */
  datasetId: number | null;
};

export type TreeRun = {
  /** The file that made this folder a run, relative to it; empty for a run you marked by hand. */
  record: string;
  /** vgt, rohsa, or the kind a tool profile gives its runs; provenance, run-json, params, mlflow or manual otherwise. */
  recordKind: string;
  fields: RunField[];
  settings: { key: string; value: string }[];
  /** The settings of this run that differ from its siblings in the same folder. */
  differs: { key: string; value: string }[];
  notes: string[];
  outputCount: number;
  outputBytes: number;
  /** The imported Vesqira run, else null. */
  runId: number | null;
  /** The tool profile (name@version) that recognised the folder, "" for a record no profile claims. */
  profile?: string;
  /** The file in the project's .vesqira/profiles/ that declared that profile; "" for a profile of this program. */
  profileFile?: string;
  /** The Import button can read this run. A run of a project profile is imported by that button and never by a scan. */
  importable?: boolean;
};

export type TreeEntry = {
  name: string;
  /** Relative to the project folder. For a run this is its name in the list. */
  path: string;
  kind: "dir" | "run" | "output" | "data" | "config" | "job";
  /** Entries listed in a folder; null for anything else. */
  count: number | null;
  file: TreeFile | null;
  run: TreeRun | null;
};

export type TreePage = { group: TreeGroup; path: string; entries: TreeEntry[]; total: number; offset: number; limit: number; nextOffset: number | null };

export type NeedOption = { id: string; label: string; input: "" | "text" | "choice"; choices?: string[]; hint?: string };

export type ProjectAnswer = { need: string; answer: string; value: string; declaredBy: "you"; date: string; writtenToProject: boolean };

export type Need = {
  id: string;
  kind: "missing_bunit" | "axial_unconfirmed" | "result_folder" | "no_version_control" | "host_repo";
  /** At most 20 paths, and how many there are in all. */
  scope: { paths: string[]; count: number; folder?: string };
  message: string;
  /** What said so. */
  source: string;
  /** The ways to settle it; the last one is always "unknown". */
  answers: NeedOption[];
  state: "open" | "answered";
  answer: ProjectAnswer | null;
  /** What an earlier fact suggests (for example the folder an earlier job used); never applied unless chosen. */
  suggestion?: string;
  datasetId?: number;
};

export type AnswerResult = { need: Need | null; writtenToProject: boolean; note: string; open: number };

export const listProjects = () => call<{ projects: ProjectListItem[] }>("/api/projects").then((r) => r.projects);
/** Opens a folder as a project (hostId 0 or omitted: this computer) and starts its scan. The folder becomes readable. */
export const openProject = (path: string, hostId = 0) => post<ProjectSummary>("/api/projects/open", hostId ? { hostId, path } : { path });
export const getProject = (id: number) => call<ProjectSummary>(`/api/projects/${id}`);
export const rescanProject = (id: number) => post<{ started: boolean; scan: ScanProgress }>(`/api/projects/${id}/scan`);
export const cancelScan = (id: number) => post<{ canceled: boolean }>(`/api/projects/${id}/scan/cancel`);
export const projectTree = (id: number, group: TreeGroup, path = "", offset = 0, limit = 200) =>
  call<TreePage>(`/api/projects/${id}/tree?group=${group}&path=${encodeURIComponent(path)}&offset=${offset}&limit=${limit}`);
export const projectNeeds = (id: number) => call<{ needs: Need[]; open: number }>(`/api/projects/${id}/needs`);
/** Answer one question. `write` writes it to .vesqira/project.toml even if the file does not exist yet. */
export const answerNeed = (id: number, need: string, answer: string, value = "", write = false) =>
  post<AnswerResult>(`/api/projects/${id}/answers`, { need, answer, value, write });
/** Writes the proposed (or the given) .vesqira/project.toml. Only after the researcher confirms; never committed. */
export const writeProjectFile = (id: number, text = "") => post<{ written: boolean; path: string; replaced: boolean }>(`/api/projects/${id}/project-file`, { write: true, text });

// ---- generic import (ADR 0058) ----
type Schemas = components["schemas"];
export type ImportRule = Schemas["ImportRule"];
export type ImportSummary = Schemas["ImportSummary"];
export type ImportRulesState = Schemas["ImportRulesState"];
export type ImportPreview = Schemas["ImportPreview"];
export type ImportGroup = Schemas["ImportGroup"];
export type ImportProposal = Schemas["ImportProposal"];
export type ImportApplyReply = Schemas["ImportApplyReply"];
export type ImportApplyResult = Schemas["ImportApplyResult"];
export type RuleRole = ImportRule["role"];

export const importRules = (id: number) => call<ImportRulesState>(`/api/projects/${id}/import/rules`);
/** Saves the whole ordered list in the catalog (a 422 carries every reason). project.toml is not touched. */
export const saveImportRules = (id: number, rules: ImportRule[]) => put<ImportRulesState>(`/api/projects/${id}/import/rules`, { rules });
/** Writes the saved rules into .vesqira/project.toml. Call it only after the researcher confirms. */
export const writeImportRules = (id: number) => post<Schemas["ImportRulesWriteReply"]>(`/api/projects/${id}/import/rules/write`, { write: true });
/** Read-only. `rules` previews a draft; omit it for the saved rules. */
export const importPreview = (id: number, body: { folder?: string; rescan?: boolean; rules?: ImportRule[]; expand?: string }, signal?: AbortSignal) =>
  post<ImportPreview>(`/api/projects/${id}/import/preview`, body, signal);
/** Registers what the saved rules select. Call it only after the researcher confirms. */
export const importApply = (id: number, body: { folder?: string; paths?: string[]; asNew?: string[] }) => post<ImportApplyReply>(`/api/projects/${id}/import/apply`, { confirm: true, ...body });
