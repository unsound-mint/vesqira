import type { ParentComparison as Cmp, RerunMode } from "../api/runs";
import { Pill } from "../ui";
import { EM } from "../catalog/format";
import s from "./RunPage.module.css";

export const MODE_TEXT: Record<RerunMode, { name: string; what: string }> = {
  strict: { name: "Strict", what: "the same code revision and the same recorded environment; the draft was refused unless the code folder matched the parent's" },
  best_effort: { name: "Best effort", what: "the code as it was when submitted; each difference from the parent was listed before submitting" },
  adapted: { name: "Adapted", what: "changed settings; the settings that differ are marked in Settings and code" },
};

const RESULT: Record<string, { text: string; tone: "ok" | "warn" | "neutral" }> = {
  matched: { text: "matched", tone: "ok" },
  differed: { text: "differed", tone: "warn" },
  unknown: { text: "not comparable", tone: "neutral" },
};

/** Which facts both runs observed matched and which differed. A fact one side did not observe is "not comparable", never "matched". Shown only for a run that repeats another. */
export function ParentComparison({ cmp, parentName }: { cmp: Cmp; parentName: string }) {
  const mode = cmp.mode ? MODE_TEXT[cmp.mode] : null;
  const matched = cmp.facts.filter((f) => f.result === "matched").length;
  const differed = cmp.facts.filter((f) => f.result === "differed");
  return (
    <>
      <p className={s.block}>
        {mode ? <><strong>{mode.name} rerun:</strong> {mode.what}. </> : <>The draft did not record how closely it repeats its parent. </>}
        Each line compares what the two jobs observed themselves when they ran (verified), not what either run declared. This says nothing about whether the results agree.
      </p>
      {!cmp.thisObserved || !cmp.parentObserved ? (
        <p className={s.block} role="note">
          {!cmp.thisObserved ? "This run's job did not leave an environment record. " : ""}
          {!cmp.parentObserved ? `${parentName} has no environment record. ` : ""}
          Facts that one side lacks are not comparable.
        </p>
      ) : null}
      <div className={s.block}>
        <div className={s.scrollX}>
          <table className={s.table}>
            <thead>
              <tr><th scope="col">Fact</th><th scope="col">Compared</th><th scope="col">{parentName}</th><th scope="col">This run</th></tr>
            </thead>
            <tbody>
              {cmp.facts.map((f) => (
                <tr key={f.key}>
                  <th scope="row">{f.label}</th>
                  <td>
                    <Pill tone={RESULT[f.result]?.tone ?? "neutral"}>{RESULT[f.result]?.text ?? f.result}</Pill>
                    {f.result === "differed" && f.blocking ? <span className={s.note}> a strict rerun requires this to match</span> : null}
                    {f.result === "unknown" && f.why ? <span className={s.note}> {f.why}</span> : null}
                  </td>
                  <td className="mono">{f.parent || EM}</td>
                  <td className="mono">{f.this || EM}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p className={s.note}>{matched} matched · {differed.length} differed · {cmp.facts.length - matched - differed.length} not comparable</p>
      </div>
    </>
  );
}
