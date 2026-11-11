import { lazy, Suspense, useState } from "react";
import { Button } from "../ui";
import type { Host } from "../api/hosts";
import s from "./HostStorage.module.css";

export { REASK_MS } from "./reask";

// the dialog (and React Aria behind it) loads when it is first opened
const HostStorageDialog = lazy(() => import("./HostStorageDialog"));

/**
 * Quota and disk usage of a host, and how big its run folders are, in a dialog wide enough to read paths and tool output.
 * Nothing is asked of the host until the dialog is opened. Read-only: there is no way to delete anything here.
 */
export function HostStorage({ host }: { host: Host }) {
  const [open, setOpen] = useState(false);
  return (
    <section className={s.root} aria-label={`Storage on ${host.name}`}>
      <div className={s.head}>
        <Button type="button" aria-expanded={open} aria-haspopup="dialog" onClick={() => setOpen(true)}>Disk and quota</Button>
      </div>
      {open ? (
        <Suspense fallback={null}>
          <HostStorageDialog host={host} onClose={() => setOpen(false)} />
        </Suspense>
      ) : null}
    </section>
  );
}
