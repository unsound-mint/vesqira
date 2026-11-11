import { useCallback, useEffect, useRef, useState } from "react";
import { listNotifications, markNotificationsRead } from "../api/notifications";
import type { Notification } from "../api/notifications";
import { lastShownId, setLastShownId, showDesktop } from "./desktop";

/** How often the list is asked for. Events are rare and the server's own job watcher runs a minute apart, so a stream would add nothing. */
export const POLL_MS = 30_000;

/**
 * The owner's notifications: the recent list and the unread count, refreshed every POLL_MS, when the window comes back and on
 * demand. Events that appeared since the last one shown are announced on the desktop (when chosen and allowed) and passed to
 * onNew, so the app can refresh jobs and runs before a link to them is followed. The first run with nothing stored only
 * records where things stand: the backlog is not replayed.
 */
export function useNotifications(onNew?: (fresh: Notification[]) => void) {
  const [items, setItems] = useState<Notification[]>([]);
  const [unread, setUnread] = useState(0);
  const onNewRef = useRef(onNew);
  onNewRef.current = onNew;

  const apply = useCallback((items: Notification[], unread: number) => {
    setItems(items);
    setUnread(unread);
    const top = items.reduce((m, n) => Math.max(m, n.id), 0);
    const last = lastShownId();
    if (last === null) {
      setLastShownId(top);
      return;
    }
    const fresh = items.filter((n) => n.id > last).sort((a, b) => a.id - b.id);
    if (fresh.length === 0) return;
    setLastShownId(top);
    for (const n of fresh) showDesktop(n);
    onNewRef.current?.(fresh);
  }, []);

  const refresh = useCallback(async () => {
    try {
      const r = await listNotifications();
      apply(Array.isArray(r.notifications) ? r.notifications : [], typeof r.unread === "number" ? r.unread : 0);
    } catch {
      /* the API is unreachable for now; the next poll tries again */
    }
  }, [apply]);

  useEffect(() => {
    void refresh();
    const t = setInterval(() => void refresh(), POLL_MS);
    const back = () => {
      if (document.visibilityState !== "hidden") void refresh();
    };
    document.addEventListener("visibilitychange", back);
    window.addEventListener("focus", back);
    return () => {
      clearInterval(t);
      document.removeEventListener("visibilitychange", back);
      window.removeEventListener("focus", back);
    };
  }, [refresh]);

  /** Marks everything shown as read (opening the list is reading it). Returns the ids that were unread, for a "new" marker. */
  const markAllRead = useCallback(async (): Promise<number[]> => {
    const ids = items.filter((n) => !n.read).map((n) => n.id);
    if (ids.length === 0) return [];
    try {
      const r = await markNotificationsRead({ ids });
      setItems(r.notifications);
      setUnread(r.unread);
    } catch {
      /* stays unread; opening again retries */
    }
    return ids;
  }, [items]);

  return { items, unread, refresh, markAllRead };
}
