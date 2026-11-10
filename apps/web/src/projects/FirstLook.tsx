import { useEffect, useState } from "react";
import type { ProjectSummary } from "../api/projects";
import { Button } from "../ui";
import { bytes } from "../catalog/format";
import { num } from "./format";
import { ScanStatus } from "./ScanStatus";
import s from "./Projects.module.css";

type Line = { label: string; value: string; from: string; warn?: boolean };

/** Every line says what was found and where it came from: found in the folder, never a guess about what it means. */
export function summaryLines(p: ProjectSummary): Line[] {
  const c = p.counts;
  const lines: Line[] = [];
  lines.push({
    label: "Runs",
    value: c.runs === 0 ? "none found" : `${num(c.runs)}${c.manualRuns > 0 ? `, ${num(c.manualRuns)} marked by you` : ""}`,
    from: "folders with a run record (run_metadata.toml, provenance.json, run.json, params.yaml, MLflow) · found by file names",
  });
  const formats = Object.entries(c.byFormat).map(([f, n]) => `${f} ${num(n)}`).join(" · ");
  lines.push({
    label: "Data",
    value: c.data === 0 ? "none found" : `${num(c.data)}${formats ? ` (${formats})` : ""}${c.noViewer > 0 ? ` · ${num(c.noViewer)} with no viewer yet` : ""}`,
    from: "data/raw, interim, processed, external, and files whose format carries its own header",
  });
  lines.push({ label: "Configs", value: c.configs === 0 ? "none found" : num(c.configs), from: "files in configs/" });
  lines.push({ label: "Job scripts", value: c.jobs === 0 ? "none found" : num(c.jobs), from: "files in hpc/, slurm/, jobs/" });
  lines.push(
    p.git.present
      ? { label: "Code", value: `git, commit ${p.git.commit.slice(0, 8) || "unknown"}${p.git.dirty ? `, ${num(p.git.dirty)} changed file${p.git.dirty === 1 ? "" : "s"}` : ", no changed files"}`, from: "git, read-only", warn: !!p.git.dirty }
      : { label: "Code", value: "no version control", from: "no .git folder in the project", warn: true },
  );
  const skipped = [
    c.skippedDirs > 0 ? `${num(c.skippedDirs)} folder${c.skippedDirs === 1 ? "" : "s"}` : "",
    c.archives.count > 0 ? `${num(c.archives.count)} archive${c.archives.count === 1 ? "" : "s"} (${bytes(c.archives.bytes)})` : "",
    c.neverRead > 0 ? `${num(c.neverRead)} never-read name${c.neverRead === 1 ? "" : "s"}` : "",
  ].filter(Boolean);
  lines.push({ label: "Skipped", value: skipped.length ? skipped.join(" · ") : "nothing", from: "skip rules (.git, .venv, caches, your skip globs); archives are listed, not opened; never-read names are not read" });
  const l = p.links;
  if (l.outside > 0 || l.dangling > 0) {
    const eg = l.examples.filter((e) => e.state !== "inside").slice(0, 3).map((e) => `${e.path} (${e.state === "outside" ? "points outside" : "dangling"})`).join(", ");
    lines.push({
      label: "Links",
      value: `${num(l.outside)} point outside the project, ${num(l.dangling)} dangling${eg ? ` · ${eg}` : ""}`,
      from: "symbolic links, never followed",
      warn: true,
    });
  }
  return lines;
}

/** Screen 2: what the scan found, and the layout file it proposes. Nothing is written until the button is pressed. */
export function FirstLook({ project, onWrite, onUseWithout, onCancel }: { project: ProjectSummary; onWrite: (text: string) => Promise<string | null>; onUseWithout: () => void; onCancel: () => void }) {
  const proposal = project.proposedProjectFile;
  const [text, setText] = useState(proposal ?? "");
  const [edited, setEdited] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  // the proposal is recomputed by every scan; a text you have edited is never replaced under your hands
  useEffect(() => {
    if (!edited) setText(proposal ?? "");
  }, [proposal, edited]);

  const onHost = project.hostId !== 0;
  async function write() {
    setBusy(true);
    setErr(await onWrite(edited ? text : ""));
    setBusy(false);
  }

  const lines = project.scanned ? summaryLines(project) : [];
  return (
    <div className={s.root}>
      <div className={s.page}>
        <div className={s.head}>
          <h2 className={s.pageTitle}>{project.name}</h2>
          <span className={s.dim} title={project.path}>{project.hostId ? `host ${project.hostId} · ` : ""}{project.path}</span>
        </div>
        <ScanStatus scan={project.scan} onCancel={onCancel} />
        <div className={s.two}>
          <section className={s.card} aria-labelledby="found-h">
            <h3 id="found-h" className={s.cardTitle}>What is in this folder</h3>
            {!project.scanned ? (
              <p className={s.note}>Counts appear when the first scan has finished. Until then nothing is known about this folder.</p>
            ) : (
              <dl className={s.facts}>
                {lines.map((l) => (
                  <div key={l.label} className={s.fact}>
                    <dt>{l.label}</dt>
                    <dd>
                      <span className={l.warn ? s.warnText : undefined}>{l.value}</span>
                      <span className={s.from}>from: {l.from}</span>
                    </dd>
                  </div>
                ))}
              </dl>
            )}
            {project.truncated ? <p className={s.warnNote} role="status">The scan stopped early{project.truncatedWhy ? `: ${project.truncatedWhy}` : ""}. Counts are lower bounds.</p> : null}
            {project.resultLike.length > 0 ? <p className={s.note}>{num(project.resultLike.length)} folder{project.resultLike.length === 1 ? " looks" : "s look"} like results but {project.resultLike.length === 1 ? "has" : "have"} no run record: they are listed under "Needs your input".</p> : null}
            {project.registration.failed > 0 ? <p className={s.warnNote}>{num(project.registration.failed)} FITS header{project.registration.failed === 1 ? "" : "s"} could not be read{project.registration.errors[0] ? `: ${project.registration.errors[0]}` : ""}.</p> : null}
            {project.notes.map((n) => <p key={n} className={s.note}>{n}</p>)}
          </section>

          <section className={s.card} aria-labelledby="toml-h">
            <h3 id="toml-h" className={s.cardTitle}>{proposal !== null ? "Proposed .vesqira/project.toml" : ".vesqira/project.toml"}</h3>
            {proposal === null ? (
              project.projectFile.error ? (
                <p className={s.err} role="alert">The project file exists but could not be read: {project.projectFile.error}</p>
              ) : (
                <p className={s.note}>The project file exists{project.projectFile.name ? ` (project name: ${project.projectFile.name})` : ""}. Its layout and your answers are used as written; Vesqira only adds answers you give.</p>
              )
            ) : (
              <>
                <label className={s.areaLabel} htmlFor="toml-text">Editable before it is written</label>
                <textarea id="toml-text" className={s.area} spellCheck={false} rows={14} value={text} onChange={(e) => { setText(e.target.value); setEdited(true); }} />
                {err ? <p role="alert" className={s.err}>{err}</p> : null}
                <div className={s.actions}>
                  <Button variant="primary" disabled={busy || onHost || !project.scanned} onClick={() => void write()}>{busy ? "Writing…" : "Write it to the project"}</Button>
                  <Button onClick={onUseWithout}>Use without writing</Button>
                </div>
                <p className={s.note}>
                  {onHost ? "This project is on a host: writing there is not built yet, so answers stay in Vesqira's catalog. " : "Written to the folder, not committed. "}
                  Nothing is written until you press the button.
                </p>
              </>
            )}
          </section>
        </div>
      </div>
    </div>
  );
}
