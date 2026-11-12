import { useOfflineView } from "../api/offlineState";
import s from "./OfflineBanner.module.css";

const when = (iso: string) => new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });

/**
 * Said whenever the plane, view or spectrum on screen came from an offline copy (ADR 0059): that it is a copy, when it was taken,
 * and, one click away, the file it was taken from. It is a calm status strip, not an alarm: the copy is exactly what was kept, and
 * it is not the host. When something the copy does not hold was asked for, the server's sentence naming it follows on its own line.
 */
export function OfflineBanner({ datasetId }: { datasetId: number | null }) {
  const { copy, missing } = useOfflineView(datasetId);
  if (!copy) return null;
  const sig = copy.signature;
  const id = sig.identity ? `${sig.identity.startsWith("sha256:") ? "SHA-256" : "sampled fingerprint"} ${sig.identity.replace(/^[a-z0-9]+:/, "").slice(0, 12)}…` : "";
  return (
    <div className={s.banner} role="status" data-testid="offline-banner">
      <p className={s.line}>
        <strong>Offline copy</strong> · taken {when(copy.takenAt)} · the host is not being reached
      </p>
      {missing ? <p className={s.missing}>{missing}</p> : null}
      <details className={s.more}>
        <summary>Which file</summary>
        <p>
          This is what Vesqira kept, from the file as it was then ({sig.size.toLocaleString()} bytes{id ? `, ${id}` : ""}). It cannot show anything newer.
        </p>
      </details>
    </div>
  );
}
