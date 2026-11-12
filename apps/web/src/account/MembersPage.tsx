import { useCallback, useEffect, useState } from "react";
import type { FormEvent } from "react";
import { ApiError } from "../api/http";
import { ROLES, createInvitation, listMembers, removeMember, revokeInvitation, roleName, setMemberRole } from "../api/workspaces";
import type { InvitationCreated, MemberList, Role } from "../api/workspaces";
import { getAccessOverview, setBlockedLabels } from "../api/access";
import { Button, Checkbox, Input, LinkButton, Select } from "../ui";
import { useCan } from "./permissions";
import s from "./Account.module.css";

const message = (e: unknown) => (e instanceof ApiError || e instanceof Error ? e.message : "Something went wrong.");
const day = (iso: string) => iso.slice(0, 10);

/**
 * The current workspace's members (ADR 0050 slice 3): everyone sees who is in it and with which role; a Maintainer invites
 * Viewers and Researchers; an Owner invites any role, changes roles and removes members (never the last Owner). Plain on
 * purpose: the owner will redesign it.
 */
export function MembersPage({ myRole }: { myRole: string }) {
  const can = useCan();
  const [list, setList] = useState<MemberList | null>(null);
  const [error, setError] = useState("");
  const refresh = useCallback(() => listMembers().then(setList, (e) => setError(message(e))), []);
  useEffect(() => {
    void refresh();
  }, [refresh]);
  const act = (p: Promise<unknown>) => {
    setError("");
    void p.then(refresh, (e) => setError(message(e)));
  };
  const manage = can("member.manage");
  const invitable = ROLES.filter((r) => myRole === "owner" || r.role === "viewer" || r.role === "researcher");
  if (!list) return <main className={s.page}>{error ? <p className={s.error} role="alert">{error}</p> : null}</main>;
  return (
    <main className={`${s.page} ${s.wide}`}>
      <div className={s.row}>
        <h1 className={s.h1}>{list.workspace.name}</h1>
        <span className={s.note}>you are {roleName(myRole)}</span>
      </div>
      <div className={s.row}>
        <LinkButton href="#/">Back to the workbench</LinkButton>
      </div>
      {error ? <p className={s.error} role="alert">{error}</p> : null}
      <section className={s.card} aria-labelledby="members-h">
        <h2 id="members-h" className={s.h2}>Members</h2>
        <table className={s.table}>
          <thead>
            <tr><th scope="col">Name</th><th scope="col">Username</th><th scope="col">Role</th><th scope="col">Since</th>{manage ? <th scope="col"><span className={s.note}>Actions</span></th> : null}</tr>
          </thead>
          <tbody>
            {list.members.map((m) => (
              <tr key={m.accountId}>
                <td>{m.name}{m.you ? " (you)" : ""}</td>
                <td>{m.username}</td>
                <td>
                  {manage ? (
                    <Select label={`Role of ${m.name}`} value={m.role} onChange={(e) => act(setMemberRole(m.accountId, e.target.value as Role))}>
                      {ROLES.map((r) => <option key={r.role} value={r.role}>{roleName(r.role)}</option>)}
                    </Select>
                  ) : roleName(m.role)}
                </td>
                <td>{day(m.since)}</td>
                {manage ? (
                  <td>
                    <Button variant="ghost" onClick={() => { if (confirm(`Remove ${m.name} from ${list.workspace.name}? What they made stays, under their name.`)) act(removeMember(m.accountId)); }}>Remove</Button>
                  </td>
                ) : null}
              </tr>
            ))}
          </tbody>
        </table>
        {manage ? <p className={s.note}>Changing a role or removing someone needs a sign-in within the last 15 minutes (confirm it on the account page). A workspace always keeps one Owner.</p> : null}
      </section>
      {can("member.invite") ? <Invite roles={invitable} list={list} onChange={refresh} onError={setError} /> : null}
      {can("workspace.admin") ? <PublishingPolicy workspaceId={list.workspace.id} onError={setError} /> : null}
      <section className={s.card} aria-labelledby="roots-h">
        <h2 id="roots-h" className={s.h2}>Data folders</h2>
        {list.roots.length ? (
          <ul>{list.roots.map((r) => <li key={r}><code>{r}</code></li>)}</ul>
        ) : (
          <p className={s.note}>No data folder is declared for this workspace yet: ask the server's admin to declare one before registering files.</p>
        )}
      </section>
    </main>
  );
}

/**
 * Which access labels block publishing in this workspace (ADR 0069). A blocked label stops a board or report that uses data with
 * it from being published or pushed here, whatever its author confirms. Only an admin sees it; the server checks the permission.
 */
function PublishingPolicy({ workspaceId, onError }: { workspaceId: number; onError: (m: string) => void }) {
  const [blocked, setBlocked] = useState<string[] | null>(null);
  useEffect(() => {
    getAccessOverview().then((o) => setBlocked(o.blockedLabels), (e) => onError(message(e)));
  }, [onError]);
  if (!blocked) return null;
  const toggle = (l: "collaboration" | "unreleased", on: boolean) => {
    const next = (["collaboration", "unreleased"] as const).filter((x) => (x === l ? on : blocked.includes(x)));
    setBlockedLabels(workspaceId, [...next]).then((r) => setBlocked(r.blockedLabels), (e) => onError(message(e)));
  };
  return (
    <section className={s.card} aria-labelledby="policy-h">
      <h2 id="policy-h" className={s.h2}>Publishing policy</h2>
      <p className={s.note}>Datasets can carry an access label. Choose the labels that block publishing: a board or report using such data cannot be published or pushed to this workspace, and its author cannot confirm past it. Other labelled work asks its author to confirm.</p>
      <Checkbox label="Block collaboration-only data" checked={blocked.includes("collaboration")} onChange={(e) => toggle("collaboration", e.target.checked)} />
      <Checkbox label="Block unreleased data" checked={blocked.includes("unreleased")} onChange={(e) => toggle("unreleased", e.target.checked)} />
      <p className={s.note}>Changing this needs a sign-in within the last 15 minutes. Labels are what researchers declare; the server enforces them but cannot check them against the files.</p>
    </section>
  );
}

function Invite({ roles, list, onChange, onError }: { roles: { role: Role; label: string }[]; list: MemberList; onChange: () => void; onError: (m: string) => void }) {
  const [role, setRole] = useState<Role>("researcher");
  const [note, setNote] = useState("");
  const [made, setMade] = useState<InvitationCreated | null>(null);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    onError("");
    createInvitation(role, note).then((c) => {
      setMade(c);
      setNote("");
      onChange();
    }, (err) => onError(message(err)));
  };
  return (
    <section className={s.card} aria-labelledby="invite-h">
      <h2 id="invite-h" className={s.h2}>Invite someone</h2>
      <form className={s.form} onSubmit={submit}>
        <Select label="Role" value={role} onChange={(e) => setRole(e.target.value as Role)}>
          {roles.map((r) => <option key={r.role} value={r.role}>{r.label}</option>)}
        </Select>
        <Input label="Note (who it is for; only members see it)" value={note} maxLength={200} onChange={(e) => setNote(e.target.value)} />
        <div className={s.row}><Button type="submit">Create the invitation link</Button></div>
      </form>
      {made ? (
        <div role="status">
          <p className={s.note}>Send this link to the person yourself. It works once, for 7 days, and is shown only now:</p>
          <p><code>{made.link}</code></p>
        </div>
      ) : null}
      {list.invitations.length ? (
        <table className={s.table}>
          <caption className={s.note}>Waiting to be accepted</caption>
          <thead><tr><th scope="col">Role</th><th scope="col">Note</th><th scope="col">Expires</th><th scope="col"><span className={s.note}>Actions</span></th></tr></thead>
          <tbody>
            {list.invitations.map((i) => (
              <tr key={i.id}>
                <td>{roleName(i.role)}</td>
                <td>{i.note}</td>
                <td>{day(i.expiresAt)}</td>
                <td><Button variant="ghost" onClick={() => void revokeInvitation(i.id).then(onChange, (e) => onError(message(e)))}>Revoke</Button></td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}
    </section>
  );
}
