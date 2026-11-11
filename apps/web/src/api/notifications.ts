import { request as call } from "./http";
import type { components } from "./schema";

type Schemas = components["schemas"];

/** One event for the owner: a job ended, or its results were or were not imported. Never shown to a reviewer. */
export type Notification = Schemas["Notification"];
export type NotificationList = Schemas["NotificationList"];

export const listNotifications = (limit = 50) => call<NotificationList>(`/api/notifications?limit=${limit}`);
export const markNotificationsRead = (r: { ids: number[] } | { all: true }) =>
  call<NotificationList>("/api/notifications/read", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(r) });
