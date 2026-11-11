import { Basis, Button } from "../ui";
import { bytes } from "../catalog/format";
import type { JobUsage as Usage } from "../api/jobs";
import { formatDuration, parseTime } from "../ui/duration";
import { parseMem, usageLines } from "./usageText";
import s from "./JobUsage.module.css";

/** What the scheduler recorded a finished job used, in words. Nothing here says the result is valid. */
export function UsageSummary({ usage }: { usage: Usage }) {
  const [first, ...rest] = usageLines(usage);
  return (
    <section className={s.box} aria-label="What this job used">
      <p className={s.lead}>{first}</p>
      {rest.map((t) => <p key={t} className={s.note}>{t}</p>)}
      <p className={s.src}>As recorded by sacct when the job ended; memory is the largest peak of any single step, in GiB (powers of 1024).</p>
    </section>
  );
}

/**
 * The previous job's measured peak memory and elapsed time next to what is being requested now, and a suggested request that
 * the person applies with a click. Nothing is applied by itself and nothing is submitted here.
 */
export function SizingHint({ jobId, jobName, usage, mem, time, onUseMem, onUseTime }: {
  jobId: number;
  jobName: string;
  usage: Usage;
  /** What the form currently asks for. */
  mem: string;
  time: string;
  onUseMem: (mem: string) => void;
  onUseTime: (time: string) => void;
}) {
  const sg = usage.suggest;
  const wantMem = parseMem(mem);
  const wantTime = parseTime(time);
  const memShort = usage.maxRssBytes !== null && wantMem !== null && wantMem < usage.maxRssBytes;
  const timeShort = usage.state === "COMPLETED" && wantTime !== null && wantTime < usage.elapsedSeconds;
  return (
    <section className={s.box} aria-label={`Measured use of job ${jobId}`}>
      <p className={s.lead}>
        Measured in job {jobId} <span className="mono">{jobName}</span>:{" "}
        {usage.maxRssBytes === null ? "memory use not recorded" : <>peak memory <strong>{bytes(usage.maxRssBytes)}</strong>{usage.reqMemBytes !== null ? ` (requested ${bytes(usage.reqMemBytes)})` : ""}</>}
        {"; "}ran <strong>{formatDuration(usage.elapsedSeconds)}</strong>{usage.timelimitSeconds !== null ? ` (limit ${formatDuration(usage.timelimitSeconds)})` : ""}.
        {" "}You are asking for {mem.trim() || "no memory"} and {time.trim() || "no time limit"}.
      </p>
      {memShort ? <p className={s.warn} role="note">This memory request is smaller than the {bytes(usage.maxRssBytes!)} that job reached.</p> : null}
      {timeShort ? <p className={s.warn} role="note">This time limit is shorter than the {formatDuration(usage.elapsedSeconds)} that job needed.</p> : null}
      {usage.oomKilled ? <p className={s.warn} role="note">That job was stopped for exceeding its memory, so its peak understates what the work needs.</p> : null}
      {sg ? (
        <>
          <Basis summary={<>Suggested from job {jobId}&rsquo;s measured use: {sg.mem ? <>memory <span className="mono">{sg.mem}</span></> : "no memory suggestion"}{sg.time ? <>, time <span className="mono">{sg.time}</span></> : ""}.</>}>
            {sg.basis ? <p>{sg.basis}.</p> : null}
            <p>The partition&rsquo;s own maximum time is not known here; check it.</p>
          </Basis>
          <div className={s.row}>
            {sg.mem ? <Button type="button" onClick={() => onUseMem(sg.mem)} disabled={mem.trim() === sg.mem}>Use memory {sg.mem}</Button> : null}
            {sg.time ? <Button type="button" onClick={() => onUseTime(sg.time)} disabled={time.trim() === sg.time}>Use time {sg.time}</Button> : null}
          </div>
        </>
      ) : (
        <p className={s.note}>No suggestion: that job did not run to its end or its limit, so its use says little about the next one.</p>
      )}
    </section>
  );
}
