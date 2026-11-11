import { useEffect, useState } from "react";
import { Button } from "../ui";
import { rohsaSizing } from "../api/jobs";
import type { RohsaSizing } from "../api/jobs";
import { parseMem } from "./usageText";
import s from "./JobUsage.module.css";

/**
 * What the owner's own sizing table (the pipeline's hpc/irgetas/submit_rohsa.sh) says for this crop and number of Gaussians, next
 * to what the form requests now. It follows the two settings as they change; a click applies it, nothing applies it by itself.
 * It says where the numbers come from and which input it had to assume.
 */
export function RohsaSizingHint({ cropSize, nGauss, cpus, mem, time, onUse }: {
  cropSize: string;
  nGauss: string;
  cpus: string;
  mem: string;
  time: string;
  onUse: (r: { cpus: string; mem: string; time: string }) => void;
}) {
  const [z, setZ] = useState<RohsaSizing | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    setErr(null);
    rohsaSizing(cropSize, nGauss).then((r) => live && setZ(Array.isArray(r?.assumed) ? r : null)).catch((e) => { if (live) { setZ(null); setErr(e instanceof Error ? e.message : "Could not read the sizing table."); } });
    return () => { live = false; };
  }, [cropSize, nGauss]);

  if (err) return <p className={s.note} role="note">{err}</p>;
  if (!z) return null;
  const want = parseMem(mem);
  const have = parseMem(z.mem);
  const short = want !== null && have !== null && want < have;
  const same = cpus.trim() === String(z.cpus) && mem.trim() === z.mem && time.trim() === z.time;
  return (
    <section className={s.box} aria-label="Suggested size from your sizing table">
      <p className={s.lead}>
        For crop_size <strong>{z.cropSize}</strong> and n_gauss <strong>{z.nGauss}</strong> your table suggests <span className="mono">{z.cpus} CPUs, {z.mem}, {z.time}</span>.
        {" "}You are asking for {cpus.trim() || "no"} CPUs, {mem.trim() || "no memory"} and {time.trim() || "no time limit"}.
      </p>
      {z.assumed.length > 0 ? <p className={s.warn} role="note">Not given, so read the way the script reads it: {z.assumed.join("; ")}.</p> : null}
      {short ? <p className={s.warn} role="note">This memory request is smaller than the {z.mem} the table gives for this size.</p> : null}
      <p className={s.src}>Source: {z.basis}. A change of N or crop moves a row of that table; a measured run (above, when there is one) says more than the table.</p>
      <div className={s.row}>
        <Button type="button" onClick={() => onUse({ cpus: String(z.cpus), mem: z.mem, time: z.time })} disabled={same}>Use {z.cpus} CPUs, {z.mem}, {z.time}</Button>
      </div>
    </section>
  );
}
