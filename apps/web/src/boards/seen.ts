/** When the person last looked at something, kept in this browser only. It drives "new since your last visit"; it is a
 * convenience, so a browser that will not store it simply shows nothing as new. */
export function readSeen(key: string): number {
  try {
    const v = Number(localStorage.getItem(`vesqira.seen.${key}`));
    return Number.isFinite(v) ? v : 0;
  } catch {
    return 0;
  }
}

export function markSeen(key: string, at: number = Date.now()): void {
  try {
    localStorage.setItem(`vesqira.seen.${key}`, String(at));
  } catch {
    /* storage may be unavailable */
  }
}

export function readName(): string {
  try {
    return localStorage.getItem("vesqira.reviewer.name") ?? "";
  } catch {
    return "";
  }
}

export function saveName(name: string): void {
  try {
    localStorage.setItem("vesqira.reviewer.name", name);
  } catch {
    /* storage may be unavailable */
  }
}
