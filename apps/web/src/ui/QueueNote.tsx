import { useEffect, useState } from "react";
import { getWorkload } from "../api/workload";
import s from "./QueueNote.module.css";

export const QUEUE_DELAY_MS = 700;
export const QUEUE_POLL_MS = 1000;

/**
 * Why a slow request is waiting. Once `active` has held for 700 ms it asks the API about request `id` every second until
 * `active` ends, and returns the server's reason only while the request is waiting. A failed poll is ignored: the note is
 * a courtesy and must never become an error of its own.
 */
export function useQueueReason(active: boolean, id: string | null): string | null {
  const [reason, setReason] = useState<string | null>(null);
  useEffect(() => {
    if (!active || !id) {
      setReason(null);
      return;
    }
    let live = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const ac = new AbortController();
    const poll = () => {
      getWorkload(id, ac.signal)
        .then((w) => { if (live) setReason(w?.request?.state === "waiting" ? (w.request.reason ?? null) : null); })
        .catch(() => { /* a poll that fails says nothing */ })
        .finally(() => { if (live) timer = setTimeout(poll, QUEUE_POLL_MS); });
    };
    timer = setTimeout(poll, QUEUE_DELAY_MS);
    return () => {
      live = false;
      clearTimeout(timer);
      ac.abort();
      setReason(null);
    };
  }, [active, id]);
  return reason;
}

/** The waiting reason as a quiet status line, or nothing. */
export function QueueNote({ reason, className }: { reason: string | null; className?: string }) {
  return reason ? <p role="status" className={[s.note, className].filter(Boolean).join(" ")}>{reason}</p> : null;
}
