/** A calendar day from a server timestamp, as written in the data: "2026-11-10" (the UTC day, the same one the run query's date words use). */
export const formatDay = (iso: string): string => iso.slice(0, 10);

/** A moment from a server timestamp, in the reader's own time zone and locale: "Nov 10, 2026, 10:15 AM". */
export const formatWhen = (iso: string): string => new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
