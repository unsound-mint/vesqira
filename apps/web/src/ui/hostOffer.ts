/**
 * "Add this host" from a file-manager mount (ADR 0031). A path field that is handed a folder on a host that is not
 * registered yet offers it; the Hosts panel, which owns the add-host form, picks the offer up and pre-fills the form.
 * Only the name, user and port of the mount travel here. Nothing is added until the person presses Add host.
 */
export type HostOffer = { host: string; user?: string; port?: string };

let current: { offer: HostOffer; serial: number } | null = null;
let serial = 0;
const listeners = new Set<() => void>();

export function offerHost(o: HostOffer): void {
  current = { offer: o, serial: ++serial };
  listeners.forEach((l) => l());
}

export const subscribeHostOffer = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};
export const getHostOffer = () => current;

/** The ssh destination the offer proposes, or null when it cannot be one (an IPv6 literal is not accepted as a destination). */
export function offerDest(o: HostOffer): string | null {
  const dest = `${o.user ? `${o.user}@` : ""}${o.host}`;
  return /^[A-Za-z0-9][A-Za-z0-9._@-]{0,99}$/.test(dest) ? dest : null;
}
