import { useEffect, useRef, useState } from "react";
import { Button } from "./Button";
import s from "./ErrorReference.module.css";

/** Copies text; falls back to select + execCommand where the async clipboard is missing or refused (an http page, an old browser). */
async function copyText(text: string): Promise<void> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return;
    }
  } catch {
    /* refused: try the selection route */
  }
  const area = document.createElement("textarea");
  area.value = text;
  area.setAttribute("readonly", "");
  area.style.position = "fixed";
  area.style.opacity = "0";
  document.body.appendChild(area);
  area.select();
  try {
    document.execCommand("copy");
  } finally {
    area.remove();
  }
}

/** "Reference: <id>" with a Copy button: the request id a person quotes when reporting a problem. */
export function ErrorReference({ id, className }: { id: string; className?: string }) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  async function copy() {
    await copyText(id);
    setCopied(true);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopied(false), 2000);
  }
  return (
    <span className={[s.ref, className].filter(Boolean).join(" ")}>
      <span>Reference: <span className={s.id}>{id}</span></span>
      <Button variant="ghost" aria-label={`Copy reference ${id}`} onClick={() => void copy()}>{copied ? "Copied" : "Copy"}</Button>
    </span>
  );
}
