import { useOfflineView } from "../api/offlineState";
import s from "./OfflineBanner.module.css";

const when = (iso: string) => new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });

/**
 * Said whenever the plane, view or spectrum on screen came from an offline copy (ADR 0059): that it is a copy, when it was taken,
 * and the file it was taken from. It is a calm status line, not an alarm: the copy is exactly what was kept, and it is not the
 * host. When something the copy does not hold was asked for, the server's sentence naming it follows.
 */
export function OfflineBanner({ datasetId }: { datasetId: number | null }) {
  const { copy, missing } = useOfflineView(datasetId);
  if (!copy) return null;
  const sig = copy.signature;
  return (
    <div className={s.banner} role="status" data-testid="offline-banner">
      <strong>Offline copy</strong>
      <span>
        {" "}The host is not being reached. This is what Vesqira kept on {when(copy.takenAt)}, from the file as it was then ({sig.size.toLocaleString()} bytes
        {sig.identity ? `, ${sig.identity.startsWith("sha256:") ? "SHA-256" : "sampled fingerprint"} ${sig.identity.replace(/^[a-z0-9]+:/, "").slice(0, 12)}…` : ""}). It cannot show anything newer.
      </span>
      {missing ? <span className={s.missing}> {missing}</span> : null}
    </div>
  );
}
