import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import type { ITheme } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { Modal } from "../ui/Modal";
import { Button, Pill, Select } from "../ui";
import { clampSize, connectTerminal, openTerminalSession, parseExit } from "../api/terminal";
import type { Host } from "../api/hosts";
import s from "./Terminal.module.css";

type State =
  | { kind: "idle" }
  | { kind: "connecting" }
  | { kind: "live" }
  | { kind: "ended"; reason: string; code: number | null }
  | { kind: "failed"; message: string };

/** The terminal's colours come from the design tokens, read when the session opens and again when the theme changes. */
export function termTheme(): ITheme {
  const cs = getComputedStyle(document.documentElement);
  const v = (name: string, fallback: string) => cs.getPropertyValue(name).trim() || fallback;
  const accent = v("--accent", "#3cc4e8");
  return {
    background: v("--bg-1", "#0f151c"),
    foreground: v("--text", "#e7ecf2"),
    cursor: accent,
    cursorAccent: v("--bg-1", "#0f151c"),
    selectionBackground: v("--accent-bg", "rgba(60,196,232,.14)"),
    selectionInactiveBackground: v("--bg-3", "#1a242f"),
    black: v("--bg-3", "#1a242f"),
    red: v("--err", "#ff7b7b"),
    green: v("--ok", "#4cc38a"),
    yellow: v("--warn", "#f0b04c"),
    blue: accent,
    magenta: v("--overlay-magenta", "#f07ad8"),
    cyan: accent,
    white: v("--text-2", "#a7b3c1"),
    brightBlack: v("--text-3", "#7f8c9b"),
    brightRed: v("--err", "#ff7b7b"),
    brightGreen: v("--ok", "#4cc38a"),
    brightYellow: v("--warn", "#f0b04c"),
    brightBlue: v("--accent-hover", "#6fd6f0"),
    brightMagenta: v("--overlay-magenta", "#f07ad8"),
    brightCyan: v("--accent-hover", "#6fd6f0"),
    brightWhite: v("--text", "#e7ecf2"),
  };
}

const CHUNK = 32 << 10; // bytes per input frame; the server accepts 64 KiB

/**
 * A shell on this computer or, through ssh, on a registered host (ADR 0029). A session is explicit: nothing connects
 * until "Open session", and nothing reconnects by itself, a dropped or ended session stays visible with its reason until
 * "New session" starts another. Closing the screen ends the shell. Loaded on demand (it brings xterm.js).
 */
export default function TerminalScreen({ hosts, onClose }: { hosts: Host[]; onClose: () => void }) {
  const [hostId, setHostId] = useState(0);
  const [state, setState] = useState<State>({ kind: "idle" });
  const [selected, setSelected] = useState(false);
  const [confirmClose, setConfirmClose] = useState(false);
  const [note, setNote] = useState<string | null>(null);
  const box = useRef<HTMLDivElement>(null);
  const term = useRef<Terminal | null>(null);
  const fit = useRef<FitAddon | null>(null);
  const sock = useRef<WebSocket | null>(null);
  const leave = useRef<HTMLButtonElement>(null);
  const open = state.kind === "connecting" || state.kind === "live";

  const sendSize = () => {
    const t = term.current;
    if (t && sock.current?.readyState === WebSocket.OPEN) sock.current.send(JSON.stringify({ type: "resize", ...clampSize(t.cols, t.rows) }));
  };

  useEffect(() => {
    if (!box.current) return;
    const t = new Terminal({
      fontFamily: "'Geist Mono Variable','Geist Mono',ui-monospace,monospace",
      fontSize: 13,
      cursorBlink: true,
      scrollback: 5000,
      theme: termTheme(),
      allowProposedApi: false,
    });
    const f = new FitAddon();
    t.loadAddon(f);
    t.open(box.current);
    term.current = t;
    fit.current = f;
    const enc = new TextEncoder();
    const send = (bytes: Uint8Array<ArrayBuffer>) => {
      const ws = sock.current;
      if (ws?.readyState !== WebSocket.OPEN) return;
      for (let i = 0; i < bytes.length; i += CHUNK) ws.send(bytes.subarray(i, i + CHUNK));
    };
    const subs = [
      t.onData((d) => send(enc.encode(d))),
      t.onBinary((d) => send(Uint8Array.from(d, (c) => c.charCodeAt(0) & 0xff))),
      t.onResize(() => sendSize()),
      t.onSelectionChange(() => setSelected(t.hasSelection())),
    ];
    t.attachCustomKeyEventHandler((e) => {
      if (e.type !== "keydown" || !e.ctrlKey || !e.shiftKey) return true;
      if (e.key === "M" || e.key === "m") { // the way out for keyboard users: Tab and Esc belong to the shell
        e.preventDefault();
        leave.current?.focus();
        return false;
      }
      if ((e.key === "C" || e.key === "c") && t.hasSelection()) {
        e.preventDefault();
        void navigator.clipboard?.writeText(t.getSelection());
        return false;
      }
      return true;
    });
    let frame = 0;
    const refit = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        try { f.fit(); } catch { /* not laid out yet */ }
      });
    };
    const ro = new ResizeObserver(refit);
    ro.observe(box.current);
    refit();
    const retheme = () => { t.options.theme = termTheme(); };
    const mo = new MutationObserver(retheme);
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    const mq = matchMedia?.("(prefers-color-scheme: light)");
    mq?.addEventListener?.("change", retheme);
    const hangUp = () => sock.current?.close(1000);
    window.addEventListener("pagehide", hangUp); // closing the tab ends the session
    return () => {
      window.removeEventListener("pagehide", hangUp);
      mq?.removeEventListener?.("change", retheme);
      mo.disconnect();
      ro.disconnect();
      cancelAnimationFrame(frame);
      subs.forEach((x) => x.dispose());
      const ws = sock.current;
      sock.current = null;
      ws?.close(1000);
      t.dispose();
    };
  }, []);

  async function start() {
    const t = term.current;
    if (!t) return;
    const old = sock.current;
    sock.current = null;
    old?.close(1000);
    t.reset();
    t.options.theme = termTheme();
    setNote(null);
    setState({ kind: "connecting" });
    let session;
    try {
      session = await openTerminalSession(hostId);
    } catch (e) {
      setState({ kind: "failed", message: e instanceof Error ? e.message : "Could not ask for a session." });
      return;
    }
    const ws = connectTerminal(session);
    sock.current = ws;
    ws.onopen = () => {
      if (sock.current !== ws) return;
      setState({ kind: "live" });
      try { fit.current?.fit(); } catch { /* not laid out yet */ }
      sendSize();
      t.focus();
    };
    ws.onmessage = (ev: MessageEvent) => {
      if (sock.current !== ws) return;
      if (typeof ev.data === "string") {
        const x = parseExit(ev.data);
        if (x) setState({ kind: "ended", reason: x.reason, code: x.code });
        return;
      }
      t.write(new Uint8Array(ev.data as ArrayBuffer));
    };
    ws.onclose = () => {
      if (sock.current !== ws) return;
      setState((cur) =>
        cur.kind === "ended" ? cur : cur.kind === "connecting"
          ? { kind: "failed", message: "The server refused the connection or the session token expired. Nothing was started." }
          : { kind: "ended", reason: "the connection closed", code: null },
      );
    };
  }

  function stop() {
    const ws = sock.current;
    ws?.close(1000);
    if (ws) setState({ kind: "ended", reason: "you ended the session", code: null });
  }

  async function paste() {
    try {
      const text = await navigator.clipboard.readText();
      if (text) term.current?.paste(text);
      term.current?.focus();
    } catch {
      setNote("The browser did not allow reading the clipboard. Use Ctrl+Shift+V in the terminal.");
    }
  }

  function close() {
    if (state.kind === "live" && !confirmClose) setConfirmClose(true);
    else onClose();
  }

  const where = hostId === 0 ? "this computer" : hosts.find((h) => h.id === hostId)?.name ?? "host";
  const tone = state.kind === "live" ? "ok" : state.kind === "failed" ? "err" : state.kind === "ended" ? "warn" : "neutral";
  const label = { idle: "No session", connecting: "Connecting…", live: "Live", ended: "Ended", failed: "Not started" }[state.kind];

  return (
    <Modal label="Terminal" fullscreen keepEscape onClose={close} className={s.dialog}>
      <div className={s.screen}>
        <header className={s.bar}>
          <div className={s.title}>
            <p className={s.name}>Terminal</p>
            <p className={s.where}>{state.kind === "idle" ? "A shell runs as you, with your own files and ssh agent." : `On ${where}`}</p>
          </div>
          <Select label="Where" value={hostId} disabled={open} onChange={(e) => setHostId(Number(e.target.value))}>
            <option value={0}>This computer</option>
            {hosts.map((h) => <option key={h.id} value={h.id}>{h.name} ({h.dest})</option>)}
          </Select>
          <Pill tone={tone}>{label}</Pill>
          {open ? (
            <Button onClick={stop}>End session</Button>
          ) : (
            <Button variant="primary" onClick={() => void start()}>{state.kind === "idle" ? "Open session" : "New session"}</Button>
          )}
          <Button onClick={() => void paste()} disabled={state.kind !== "live"}>Paste</Button>
          <Button onClick={() => void navigator.clipboard?.writeText(term.current?.getSelection() ?? "")} disabled={!selected}>Copy</Button>
          <button ref={leave} type="button" className={s.close} onClick={close}>Close</button>
        </header>
        {confirmClose ? (
          <div role="alert" className={s.notice}>
            <p>Closing ends the shell and anything running in it.</p>
            <Button variant="danger" onClick={onClose}>End and close</Button>
            <Button onClick={() => setConfirmClose(false)}>Keep it open</Button>
          </div>
        ) : null}
        {state.kind === "failed" ? <p role="alert" className={s.error}>{state.message}</p> : null}
        {state.kind === "ended" ? (
          <p role="status" className={s.notice}>
            Session ended: {state.reason}{state.code !== null ? ` (exit code ${state.code})` : ""}. The output stays below. Nothing reconnects by itself; New session starts a fresh shell.
          </p>
        ) : null}
        {note ? <p role="status" className={s.notice}>{note}</p> : null}
        <div className={s.surface}>
          <div ref={box} className={s.term} aria-label={`Terminal on ${where}`} />
          {state.kind === "idle" ? <p className={s.empty}>No session yet. Choose where, then Open session.</p> : null}
        </div>
        <p className={s.hint}>Ctrl+Shift+C copies, Ctrl+Shift+V pastes. Esc and Tab go to the shell; Ctrl+Shift+M moves focus to Close.</p>
      </div>
    </Modal>
  );
}
