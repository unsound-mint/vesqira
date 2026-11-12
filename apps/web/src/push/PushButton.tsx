import { useEffect, useState } from "react";
import { ApiError } from "../api/http";
import { groupLink, pushPreflight, queuePush } from "../api/push";
import type { GroupLink, PushKind } from "../api/push";
import { Button } from "../ui";

const message = (e: unknown) => (e instanceof ApiError || e instanceof Error ? e.message : "Something went wrong.");

let linkOnce: Promise<GroupLink | null> | null = null;
/** Forget the remembered link, after linking or unlinking on the Group server page. */
export function forgetLink() {
  linkOnce = null;
}
/** The link is asked for once per page: a local instance that is not linked shows no push button at all, and the group server
 * (which has no link route) never does. */
function currentLink(): Promise<GroupLink | null> {
  linkOnce ??= groupLink().then((l) => (l.linked ? l : null), () => null);
  return linkOnce;
}

/**
 * "Push to <workspace>" for a board, a figure, a run or a report (ADR 0066 section 2). It shows the preflight first (what goes
 * out, settings left out, datasets the server can only match by fingerprint) and queues on confirmation; the queue sends it in
 * the background, so the app never waits on the network. The status is on the Group server page.
 */
export function PushButton({ kind, id }: { kind: PushKind; id: number }) {
  const [link, setLink] = useState<GroupLink | null>(null);
  const [status, setStatus] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let live = true;
    void currentLink().then((l) => { if (live) setLink(l); });
    return () => { live = false; };
  }, []);
  if (!link) return null;
  const name = link.workspace || "the group server";
  const push = async () => {
    setBusy(true);
    setStatus("");
    try {
      const p = await pushPreflight(kind, id);
      const lines = [
        `Push “${p.title}” to ${name} (${p.origin})?`,
        `${p.pictures} picture(s), ${Math.max(1, Math.round(p.bytes / 1024))} KiB in all.`,
        ...p.warnings,
        ...p.references.map((r) => `${r.name}: ${r.fingerprint === "none" ? "no fingerprint, so it will show as not on this server" : r.fingerprint === "sampled" ? "sampled fingerprint only, so at best “probably on this server”" : "full SHA-256, so it links if the server holds the same bytes"}`),
      ];
      if (!confirm(lines.join("\n\n"))) return;
      const e = await queuePush(kind, id, p.hash);
      setStatus(e.status === "waiting: queued" ? "queued" : e.status);
    } catch (e) {
      setStatus(message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <Button disabled={busy} onClick={() => void push()}>Push to {name}</Button>
      {status ? <span role="status">{status} · <a href="#/group">queue</a></span> : null}
    </>
  );
}
