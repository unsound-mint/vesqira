import { useState } from "react";
import { X } from "lucide-react";
import { Button, IconButton } from "../ui";
import { desktopChosen, desktopPermission, desktopSupported, enableDesktop, setDesktopChosen } from "./desktop";
import type { Permission } from "./desktop";
import s from "./NotifyToggle.module.css";

const HINT_KEY = "vesqira.hint.notifications-blocked";
const hintDismissed = () => { try { return localStorage.getItem(HINT_KEY) === "1"; } catch { return false; } };

/**
 * "Notify me when jobs finish" for the Jobs screen. The browser's permission prompt is shown only from the click on this
 * button. Without a Notification API (some webviews) it says so; the bell in the title bar still lists every event.
 */
export function NotifyToggle({ className }: { className?: string }) {
  const [perm, setPerm] = useState<Permission>(desktopPermission());
  const [on, setOn] = useState(desktopChosen() && perm === "granted");
  const [hidden, setHidden] = useState(hintDismissed);

  if (!desktopSupported()) {
    return <p className={className}>This window cannot show desktop notifications. The bell in the title bar still lists every job that ends.</p>;
  }
  if (perm === "denied") {
    // a hint, not a state of the page: once read it goes away (this browser remembers), and the bell works regardless
    if (hidden) return null;
    return (
      <div className={`${className ?? ""} ${s.hint}`} role="note">
        <span>Desktop notifications are blocked for this window. Allow them in the browser or system settings to be told when a job ends; the bell in the title bar works regardless.</span>
        <IconButton icon={X} label="Dismiss this hint" onClick={() => { setHidden(true); try { localStorage.setItem(HINT_KEY, "1"); } catch { /* the hint only comes back next time */ } }} />
      </div>
    );
  }
  async function turnOn() {
    const p = await enableDesktop();
    setPerm(p);
    setOn(p === "granted");
  }
  function turnOff() {
    setDesktopChosen(false);
    setOn(false);
  }
  return (
    <div className={className}>
      {on ? <Button onClick={turnOff}>Stop desktop notifications</Button> : <Button onClick={() => void turnOn()}>Notify me when jobs finish</Button>}
      <span> {on ? "A notice appears when a job ends. It says how the job ended, not whether the result is right." : "Asks your system for permission once."}</span>
    </div>
  );
}
