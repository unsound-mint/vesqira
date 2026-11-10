import type { HealthItem, ProjectSummary } from "../api/projects";
import { Pill } from "../ui";
import { healthScore } from "./format";
import s from "./Projects.module.css";

const STATE: Record<HealthItem["state"], { text: string; tone: "ok" | "warn" | "err" | "neutral" }> = {
  ok: { text: "done", tone: "ok" },
  warn: { text: "partly", tone: "warn" },
  todo: { text: "to do", tone: "warn" },
  na: { text: "nothing to judge yet", tone: "neutral" },
};

/** One imperative line per check, for the two states that need action. The facts (detail, why) come from the API. */
const TODO: Record<HealthItem["id"], string> = {
  version_control: "Put the code under version control (git init, then commit); runs can then name their commit.",
  run_records: "Write a run record into each run folder (run_metadata.toml or provenance.json), or mark the folder as a run under \"Needs your input\".",
  raw_apart: "Keep raw data in data/raw and write results somewhere else, so inputs stay read-only.",
  units_declared: "Declare the unit for each file without BUNIT under \"Needs your input\", or leave it unknown.",
  project_file: "Review the layout in \"What is in this folder\" and write .vesqira/project.toml.",
};

/** The project-health checklist (screen 3 and 5). It nudges; it never blocks and never grades the science. */
export function HealthView({ project }: { project: ProjectSummary }) {
  const { ok, of } = healthScore(project.health);
  return (
    <div className={s.root}>
      <div className={s.page}>
        <div className={s.head}>
          <h2 className={s.pageTitle}>Project health</h2>
          <Pill tone={of > 0 && ok === of ? "ok" : "warn"}>{`${ok} of ${of}`}</Pill>
        </div>
        <p className={s.note}>A short checklist of habits that make a project easier to trust and to share. It is about organisation, not about whether results are right.</p>
        <ul className={s.checks} aria-label="Health checklist">
          {project.health.map((h) => (
            <li key={h.id} className={s.check}>
              <Pill tone={STATE[h.state].tone}>{STATE[h.state].text}</Pill>
              <div>
                <p className={s.checkLabel}>{h.label}</p>
                {h.detail ? <p className={s.note}>{h.detail}</p> : null}
                {h.state === "todo" || h.state === "warn" ? <p className={s.todo}><strong>What to do:</strong> {TODO[h.id]}</p> : null}
                {h.why ? <p className={s.dim}>Why it matters: {h.why}</p> : null}
              </div>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}
