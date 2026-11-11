import type { Notification as Event } from "../api/notifications";

// Desktop notifications use the browser's Notification API, in a tab and in the desktop window alike. Permission is asked
// only from a click (enableDesktop), never on load. The choice and the last event shown are per-viewer conveniences in
// localStorage; every read and write is guarded, because storage can be blocked or empty.

const WANT = "vesqira.notify.jobs";
const LAST = "vesqira.notify.lastId";
/** An event older than this is not announced on the desktop: it happened while the window was closed. */
export const STALE_MS = 10 * 60 * 1000;

export type Permission = NotificationPermission | "unsupported";

export const desktopSupported = () => typeof Notification !== "undefined";
export const desktopPermission = (): Permission => (desktopSupported() ? Notification.permission : "unsupported");

function read(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}
function write(key: string, v: string) {
  try {
    localStorage.setItem(key, v);
  } catch {
    /* the choice just does not survive a reload */
  }
}

/** True when the person turned desktop notifications on and the browser allows them. */
export const desktopWanted = () => read(WANT) === "1" && desktopPermission() === "granted";
export const desktopChosen = () => read(WANT) === "1";
export const setDesktopChosen = (on: boolean) => write(WANT, on ? "1" : "0");

export const lastShownId = (): number | null => {
  const v = Number(read(LAST));
  return read(LAST) !== null && Number.isFinite(v) ? v : null;
};
export const setLastShownId = (id: number) => write(LAST, String(id));

/**
 * Turns the feature on. Call it only from a click handler: browsers refuse (or ignore) a permission prompt that is not
 * the answer to a user gesture. Resolves with the resulting permission; "unsupported" when the window has no Notification.
 */
export async function enableDesktop(): Promise<Permission> {
  if (!desktopSupported()) return "unsupported";
  let p: NotificationPermission;
  try {
    p = await Notification.requestPermission();
  } catch {
    p = Notification.permission;
  }
  setDesktopChosen(p === "granted");
  return p;
}

export function showDesktop(n: Event, now = Date.now()) {
  if (!desktopWanted() || now - new Date(n.at).getTime() > STALE_MS) return;
  try {
    new Notification(n.title, { body: n.body, tag: `vesqira-${n.id}` });
  } catch {
    /* some webviews expose Notification but cannot show it; the bell still lists the event */
  }
}
