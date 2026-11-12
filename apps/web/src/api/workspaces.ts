import { getJSON, postJSON } from "./http";
import type { components } from "./schema";

type Schemas = components["schemas"];

/** The group server's workspaces, members and invitations (ADR 0050 slice 3). None of this exists in local mode. */
export type Role = Schemas["Membership"]["role"];
export type Membership = Schemas["Membership"];
export type MemberList = Schemas["MemberList"];
export type Invitation = Schemas["Invitation"];
export type InvitationCreated = Schemas["InvitationCreated"];
export type InvitationPeek = Schemas["InvitationPeek"];
export type WorkspaceContext = NonNullable<Schemas["Capabilities"]["workspace"]>;

export const ROLES: { role: Role; label: string }[] = [
  { role: "viewer", label: "Viewer: reads and comments" },
  { role: "researcher", label: "Researcher: adds data, analyses, edits own boards, reports and figures" },
  { role: "maintainer", label: "Maintainer: edits anything, deletes, invites Viewers and Researchers" },
  { role: "owner", label: "Owner: everything, including roles and removing members" },
];

export const roleName = (r: string) => r.charAt(0).toUpperCase() + r.slice(1);

/** The workspace context of the capabilities answer: where this request acts, the actor's workspaces and what they may do. */
export const workspaceContext = () => getJSON<Schemas["Capabilities"]>("/api/capabilities").then((c) => c.workspace ?? null);

export const listMembers = () => getJSON<MemberList>("/api/members");
export const setMemberRole = (accountId: string, role: Role) => postJSON<null>(`/api/members/${encodeURIComponent(accountId)}/role`, { role });
export const removeMember = (accountId: string) => postJSON<null>(`/api/members/${encodeURIComponent(accountId)}/remove`);
export const createInvitation = (role: Role, note: string) => postJSON<InvitationCreated>("/api/invitations", { role, note });
export const revokeInvitation = (id: number) => postJSON<null>(`/api/invitations/${id}/revoke`);
export const peekInvitation = (token: string) => postJSON<InvitationPeek>("/api/auth/invitation", { token });
export const acceptInvitation = (b: { token: string; name: string; username: string; password: string }) => postJSON<Schemas["AuthState"]>("/api/auth/invitation/accept", b);
export const joinWithInvitation = (token: string) => postJSON<Schemas["InvitationJoined"]>("/api/invitations/accept", { token });
