import { useState } from "react";
import type { FormEvent } from "react";
import { Button, Input, More } from "../ui";
import { findHostPython, setHostPython } from "../api/hosts";
import type { Host, HostCheck, PythonCandidate } from "../api/hosts";
import s from "./HostPython.module.css";

const msg = (e: unknown, fallback: string) => (e instanceof Error ? e.message : fallback);

/** The worker starts with `python -P`, which Python 3.11 introduced (ADR 0015). */
export const MIN_PYTHON = "3.11";

/** The check failure codes that mean ssh worked and the Python command is the problem. */
export const isPythonFailure = (code: string | null) => code === "python_missing" || code === "python_failed" || code === "python_old" || code === "python_import";

/** The text to type in the Python command field of a host that has no astropy in its system Python. */
export function PythonHelp() {
  return (
    <>
      The host needs Python {MIN_PYTHON} or newer with numpy and astropy. On a cluster the system <span className="mono">python3</span> usually has neither and you cannot install packages
      there: load an environment module (for example Anaconda) or use a virtual environment in your home folder, and give its full path as the Python command. Vesqira can look for one.
    </>
  );
}

/**
 * The Python command of a host (collapsed, opened when the check failed because of it) and the search for a Python that has astropy.
 * Saving the command checks the host again and hands the result up.
 */
export function HostPython({ host, failure, onChecked, onChange }: { host: Host; failure: string | null; onChecked: (c: HostCheck) => void; onChange: () => Promise<void> }) {
  const [python, setPython] = useState(host.python);
  const [busy, setBusy] = useState(false);
  const [finding, setFinding] = useState(false);
  const [cands, setCands] = useState<PythonCandidate[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  async function save(command: string) {
    setBusy(true);
    try {
      const c = await setHostPython(host.id, command.trim());
      setErr(null);
      setPython(command.trim());
      onChecked(c);
    } catch (x) {
      setPython(command.trim()); // it was saved when the check, not the command, failed; the host row shows that failure
      setErr(msg(x, "Could not set the Python command."));
    }
    await onChange();
    setBusy(false);
  }
  async function find() {
    setFinding(true);
    setErr(null);
    try {
      setCands((await findHostPython(host.id)).candidates);
    } catch (x) {
      setCands(null);
      setErr(msg(x, "Could not search the host."));
    }
    setFinding(false);
  }
  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (python.trim()) void save(python);
  };

  return (
    <More summary={`Python command: ${host.python}`} open={isPythonFailure(failure)}>
      <form className={s.form} onSubmit={onSubmit} aria-label={`Python command for ${host.name}`}>
        <Input label="Python command" value={python} onChange={(e) => setPython(e.target.value)} spellCheck={false} autoComplete="off" placeholder="python3 or /full/path/to/python" />
        <div className={s.row}>
          <Button type="submit" disabled={busy || !python.trim() || python.trim() === host.python}>Save and check</Button>
          <Button type="button" disabled={finding || busy} onClick={find}>{finding ? "Looking…" : "Find a Python with astropy"}</Button>
        </div>
      </form>
      <p className={s.note}><PythonHelp /></p>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      {cands ? (
        cands.length === 0 ? (
          <p className={s.note} role="status">No Python was found in the usual places. Load a module in a terminal on the host, run <span className="mono">which python3</span>, and paste that path above.</p>
        ) : (
          <ul className={s.list} aria-label={`Pythons found on ${host.name}`}>
            {cands.map((c) => (
              <li key={c.path} className={s.cand}>
                <span className={`mono ${s.path}`}>{c.path}</span>
                <span className={s.meta}>
                  {c.python ? `Python ${c.python}` : "did not run"}
                  {c.astropy ? `, astropy ${c.astropy}` : ""}
                  {c.numpy ? `, numpy ${c.numpy}` : ""}
                </span>
                {c.suitable ? (
                  <Button type="button" disabled={busy} onClick={() => void save(c.path)}>Use this one</Button>
                ) : (
                  <span className={s.problem}>Not usable: {c.problem}</span>
                )}
              </li>
            ))}
          </ul>
        )
      ) : null}
    </More>
  );
}
