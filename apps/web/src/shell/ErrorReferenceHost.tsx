import { useEffect, useState } from "react";
import { onApiError } from "../api/http";
import { Button, ErrorReference } from "../ui";
import s from "./ErrorReferenceHost.module.css";

/** The last failed request, quietly, with the reference the server logged for it. A newer error replaces it. */
export function ErrorReferenceHost() {
  const [last, setLast] = useState<{ message: string; id: string } | null>(null);
  useEffect(() => onApiError((e) => setLast({ message: e.message, id: e.requestId })), []);
  if (!last) return null;
  return (
    <p role="status" aria-live="polite" className={s.line}>
      <span className={s.msg} title={last.message}>Last error: {last.message}</span>
      <ErrorReference id={last.id} />
      <Button variant="ghost" onClick={() => setLast(null)}>Dismiss</Button>
    </p>
  );
}
