import { useState } from "react";
import type { FormEvent } from "react";
import { Button, Input } from "../ui";
import { runHostVpn, setHostVpn } from "../api/hosts";
import type { Host, VpnResult } from "../api/hosts";
import s from "./HostVpn.module.css";

const msg = (e: unknown, fallback: string) => (e instanceof Error ? e.message : fallback);

/**
 * Reads what was typed as a JSON array of strings. Nothing is split, trimmed or interpreted: the text between the quotes is
 * what the program receives, and the server stores it verbatim (ADR 0029). Empty means no command.
 */
export function parseArgv(text: string, what: string): string[] | undefined {
  const t = text.trim();
  if (!t) return undefined;
  let v: unknown;
  try {
    v = JSON.parse(t);
  } catch {
    throw new Error(`${what}: write it as a JSON list of strings, for example ["gpclient","connect","vpn.example.edu"].`);
  }
  if (!Array.isArray(v) || v.length === 0 || v.some((x) => typeof x !== "string")) {
    throw new Error(`${what}: write it as a JSON list of strings, for example ["gpclient","connect","vpn.example.edu"].`);
  }
  return v as string[];
}

export const showArgv = (a?: string[]) => (a && a.length ? JSON.stringify(a) : "");

/** The two VPN command fields of the host form. Shown only when the server was started with --enable-vpn. */
export function VpnFields({ connect, disconnect, onConnect, onDisconnect }: { connect: string; disconnect: string; onConnect: (v: string) => void; onDisconnect: (v: string) => void }) {
  return (
    <div className={s.fields}>
      <Input label="VPN connect command (optional)" value={connect} onChange={(e) => onConnect(e.target.value)} placeholder='["gpclient","connect","vpn.example.edu"]' spellCheck={false} autoComplete="off" />
      <Input label="VPN disconnect command (optional)" value={disconnect} onChange={(e) => onDisconnect(e.target.value)} placeholder='["gpclient","disconnect"]' spellCheck={false} autoComplete="off" />
      <p className={s.note}>
        A JSON list: the program, then its arguments, for example <span className="mono">["gpclient","connect","vpn.example.edu"]</span> or <span className="mono">["/usr/bin/openconnect","--protocol=gp","--background","vpn.example.edu"]</span>.
        It is stored exactly as written and run without a shell, so quotes, <span className="mono">;</span> and <span className="mono">$</span> are plain text. It runs as you on this computer. If your client needs privileges, write
        <span className="mono"> pkexec</span> as the first part or use your own polkit rule: the client asks for its password in its own window, and Vesqira never asks for or sees it.
        The command must finish once the tunnel is up (use the client's background option); Vesqira stops it after two minutes.
      </p>
    </div>
  );
}

/** What a host shows about its VPN: the stored command verbatim, the state, Connect and Disconnect, and what the command printed. */
export function HostVpn({ host, onChange }: { host: Host; onChange: () => Promise<void> }) {
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<VpnResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const [connect, setConnect] = useState(showArgv(host.vpnCommand));
  const [disconnect, setDisconnect] = useState(showArgv(host.vpnDisconnectCommand));

  const hasConnect = !!host.vpnCommand?.length;
  const hasDisconnect = !!host.vpnDisconnectCommand?.length;
  const state = host.reachable === "no" ? "not reachable" : host.reachable === "yes" ? "reachable" : "not checked yet";

  async function run(action: "connect" | "disconnect") {
    setBusy(true);
    setError(null);
    try {
      setResult(await runHostVpn(host.id, action));
      await onChange(); // the server learned whether the host answers; the list shows it
    } catch (x) {
      setResult(null);
      setError(msg(x, "That did not work."));
    }
    setBusy(false);
  }

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await setHostVpn(host.id, { vpnCommand: parseArgv(connect, "Connect command"), vpnDisconnectCommand: parseArgv(disconnect, "Disconnect command") });
      setEditing(false);
      setError(null);
      await onChange();
    } catch (x) {
      setError(msg(x, "Could not save the VPN command."));
    }
    setBusy(false);
  }

  return (
    <div className={s.vpn} role="group" aria-label={`VPN for ${host.name}`}>
      {hasConnect || hasDisconnect ? (
        <>
          <p className={s.state}>
            <strong>VPN: {state}</strong>
            {host.reachable === "no" ? <span className={s.hint}> The host did not answer.</span> : null}
          </p>
          <div className={s.row}>
            {hasConnect && host.reachable !== "yes" ? <Button type="button" variant="primary" disabled={busy} onClick={() => run("connect")}>Connect VPN</Button> : null}
            {hasConnect && host.reachable === "yes" ? <Button type="button" disabled={busy} onClick={() => run("connect")}>Connect VPN again</Button> : null}
            {hasDisconnect ? <Button type="button" disabled={busy} onClick={() => run("disconnect")}>Disconnect VPN</Button> : null}
          </div>
          {busy ? <p className={s.note} role="status">Running the VPN command…</p> : null}
          {result ? (
            <div role="status">
              <p className={result.reachable ? s.ok : s.warn}>{result.message}</p>
              <pre className={s.output} aria-label="VPN command output" tabIndex={0}>{result.output || "(no output)"}{result.outputTruncated ? "\n… (output cut)" : ""}</pre>
            </div>
          ) : null}
          <div className={s.cmds}>
            {hasConnect ? <p className={s.note}>Connect runs, as you, without a shell: <code className="mono">{showArgv(host.vpnCommand)}</code></p> : null}
            {hasDisconnect ? <p className={s.note}>Disconnect runs: <code className="mono">{showArgv(host.vpnDisconnectCommand)}</code></p> : null}
          </div>
        </>
      ) : null}
      {error ? <p role="alert" className={s.err}>{error}</p> : null}
      {editing ? (
        <form onSubmit={save} aria-label={`VPN command of ${host.name}`}>
          <VpnFields connect={connect} disconnect={disconnect} onConnect={setConnect} onDisconnect={setDisconnect} />
          <div className={s.row}>
            <Button type="submit" variant="primary" disabled={busy}>Save VPN command</Button>
            <Button type="button" disabled={busy} onClick={() => { setEditing(false); setConnect(showArgv(host.vpnCommand)); setDisconnect(showArgv(host.vpnDisconnectCommand)); }}>Cancel</Button>
          </div>
        </form>
      ) : (
        <div className={s.row}>
          <Button type="button" disabled={busy} onClick={() => setEditing(true)}>{hasConnect || hasDisconnect ? "Edit VPN command" : "Set a VPN command…"}</Button>
        </div>
      )}
    </div>
  );
}
