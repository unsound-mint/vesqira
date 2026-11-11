import { ApiError } from "./catalog";
import type { components } from "./schema";

type Schemas = components["schemas"];

/** One event for the owner: a job ended, or its results were or were not imported. Never shown to a reviewer. */
export type Notification = Schemas["Notification"];
export type NotificationList = Schemas["NotificationList"];

async function call<T>(url: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(url, init);
  } catch {
    throw new ApiError("Cannot reach the Vesqira API.");
  }
  const body = (await res.json().catch(() => null)) as { error?: string } | null;
  if (!res.ok) throw new ApiError(body?.error ?? `Request failed (${res.status})`);
  return body as T;
}

export const listNotifications = (limit = 50) => call<NotificationList>(`/api/notifications?limit=${limit}`);
export const markNotificationsRead = (r: { ids: number[] } | { all: true }) =>
  call<NotificationList>("/api/notifications/read", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(r) });
