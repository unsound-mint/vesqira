const KEY = "vesqira.reviewBase";
const DEFAULT = "http://127.0.0.1:8788";

/** The address review links are built on (where the review listener is reachable), as the author last set it. */
export const readBase = (): string => {
  try {
    return localStorage.getItem(KEY) ?? DEFAULT;
  } catch {
    return DEFAULT;
  }
};

export const saveBase = (base: string): void => {
  try {
    localStorage.setItem(KEY, base);
  } catch {
    /* storage may be unavailable; the link is still shown */
  }
};

export const reviewLink = (base: string, reviewPath: string): string => `${base.replace(/\/$/, "")}${reviewPath}`;
