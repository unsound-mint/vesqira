import { useState } from "react";
import { Button } from "../ui";
import { desktopChosen, desktopPermission, desktopSupported, enableDesktop, setDesktopChosen } from "./desktop";
import type { Permission } from "./desktop";

/**
 * "Notify me when jobs finish" for the Jobs screen. The browser's permission prompt is shown only from the click on this
 * button. Without a Notification API (some webviews) it says so; the bell in the title bar still lists every event.
 */
export function NotifyToggle({ className }: { className?: string }) {
  const [perm, setPerm] = useState<Permission>(desktopPermission());
  const [on, setOn] = useState(desktopChosen() && perm === "granted");

  if (!desktopSupported()) {
    return <p className={className}>This window cannot show desktop notifications. The bell in the title bar still lists every job that ends.</p>;
  }
  if (perm === "denied") {
    return <p className={className}>Desktop notifications are blocked for this window. Allow them in the browser or system settings to be told when a job ends; the bell in the title bar works regardless.</p>;
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
