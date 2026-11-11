import { useEffect, useRef, useState } from "react";
import { Bell } from "lucide-react";
import { Button, Icon, Pill } from "../ui";
import type { Notification } from "../api/notifications";
import s from "./NotificationBell.module.css";

const LABEL: Record<Notification["kind"], string> = {
  completed: "Finished", failed: "Failed", timeout: "Time limit", cancelled: "Cancelled", unknown: "Unknown", imported: "Imported", import_failed: "Import failed",
};
// "finished" is not "good": a completed job is accent, never the green that means a checked result
const TONE: Record<Notification["kind"], "neutral" | "ok" | "warn" | "err" | "accent"> = {
  completed: "accent", failed: "err", timeout: "warn", cancelled: "warn", unknown: "warn", imported: "accent", import_failed: "err",
};

export const whenText = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
};

/**
 * The bell in the title bar: unread count, and a list of the recent events with a way to the job or run. Opening the list
 * marks what it shows as read; events that were unread keep a "New" mark while it stays open.
 */
export function NotificationBell({ items, unread, onOpened, onOpenJob, onOpenRun }: {
  items: Notification[];
  unread: number;
  onOpened: () => Promise<number[]>;
  onOpenJob: (id: number) => void;
  onOpenRun: (id: number) => void;
}) {
  const [open, setOpen] = useState(false);
  const [fresh, setFresh] = useState<Set<number>>(new Set());
  const root = useRef<HTMLSpanElement>(null);

  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => {
      if (root.current && !root.current.contains(e.target as Node)) setOpen(false);
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", away);
    document.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("mousedown", away);
      document.removeEventListener("keydown", key);
    };
  }, [open]);

  async function toggle() {
    if (open) {
      setOpen(false);
      return;
    }
    setFresh(new Set(items.filter((n) => !n.read).map((n) => n.id)));
    setOpen(true);
    const ids = await onOpened();
    if (ids.length) setFresh((cur) => new Set([...cur, ...ids]));
  }
  const go = (f: () => void) => {
    setOpen(false);
    f();
  };

  return (
    <span className={s.root} ref={root}>
      <button type="button" className={s.bell} aria-expanded={open} aria-controls="notification-list" aria-label={unread > 0 ? `Notifications, ${unread} unread` : "Notifications"} title="Notifications" onClick={() => void toggle()}>
        <Icon icon={Bell} />
        {unread > 0 ? <span className={s.count}>{unread > 99 ? "99+" : unread}</span> : null}
      </button>
      {open ? (
        <section id="notification-list" className={s.panel} aria-label="Notifications">
          {items.length === 0 ? (
            <p className={s.empty}>Nothing yet. When a job on a host ends, it is listed here.</p>
          ) : (
            <ul className={s.list}>
              {items.map((n) => (
                <li key={n.id} className={s.item}>
                  <div className={s.head}>
                    <Pill tone={TONE[n.kind]}>{LABEL[n.kind]}</Pill>
                    {fresh.has(n.id) ? <Pill>New</Pill> : null}
                    <time className={s.when} dateTime={n.at}>{whenText(n.at)}</time>
                  </div>
                  <p className={s.title}>{n.title}</p>
                  <p className={s.body}>{n.body}</p>
                  <div className={s.actions}>
                    {n.jobId != null ? <Button onClick={() => go(() => onOpenJob(n.jobId!))}>Open job</Button> : null}
                    {n.runId != null ? <Button onClick={() => go(() => onOpenRun(n.runId!))}>Open run</Button> : null}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </section>
      ) : null}
    </span>
  );
}
