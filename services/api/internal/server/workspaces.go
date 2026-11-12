package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/accounts"
	"github.com/unsound-mint/vesqira/services/api/internal/authz"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Workspaces on the group server (ADR 0050 slice 3). Every request acts in one workspace with the actor's role there:
//
//   - a route whose path names an object ({id} and the like) acts in that object's workspace: route looks the object up
//     (one indexed query, scopeKinds below), and answers 404, exactly as for a missing id, unless the actor is a member of
//     it; the role is the actor's role there. So a link to a figure or a picture embedded in a report works from any of the
//     actor's workspaces, and an id is never an existence oracle;
//   - any other route acts in the workspace the client names in the X-Vesqira-Workspace header (it must be one of the
//     actor's), or else the actor's oldest membership.
//
// Stores take that workspace (workspace.ID) on every query (internal/arch TestEveryScopedQueryNamesTheWorkspace), so an id read
// from a body or a query string that belongs elsewhere is simply not found. Local mode skips all of it: one workspace, one
// actor who owns it.

// WorkspaceHeader names the workspace a request acts in when its path names no object.
const WorkspaceHeader = "X-Vesqira-Workspace"

// scopeKinds are the "<first path segment after /api/>/<parameter>" keys whose value names an object of a workspace; scopeLookup
// says how to find its workspace. A route the group server mounts with a parameter that is in none of scopeKinds, childParams
// and unscopedParams does not start (route panics), so a new route is a decision about scope; TestNoRouteLeaksAcrossWorkspaces
// requests every one of them across workspaces.
var scopeKinds = map[string]bool{
	"datasets/id": true, "figures/id": true, "boards/id": true, "reports/id": true, "runs/id": true, "run-views/id": true,
	"regions/id": true, "masks/id": true, "subsets/sid": true, "history/id": true, "projects/id": true, "attachments/sha": true,
	"discussion/id": true, "invitations/id": true, "snapshots/id": true, "comments/id": true,
}

// scopeLookup is the query that finds the workspace and creator of the object a path parameter names: rows of (workspace_id,
// created_by), one for an id, possibly several for a content address (the same picture attached in two workspaces), the
// selected workspace first.
func scopeLookup(kind, value string, selected workspace.ID) (string, []any) {
	switch kind {
	case "snapshots/id": // a snapshot belongs to its board, and "own" (publish, revoke, reply, resolve) means own board
		return `SELECT s.workspace_id, COALESCE(b.created_by,'') FROM snapshots s LEFT JOIN boards b ON b.id=s.board_id
			WHERE s.id=? ORDER BY s.workspace_id=? DESC`, []any{value, selected}
	case "comments/id": // a reviewer's comment, likewise
		return `SELECT s.workspace_id, COALESCE(b.created_by,'') FROM comments c JOIN snapshots s ON s.id=c.snapshot_id
			LEFT JOIN boards b ON b.id=s.board_id WHERE c.id=? ORDER BY s.workspace_id=? DESC`, []any{value, selected}
	}
	table := scopeTables[kind]
	if table == "" {
		return "", nil
	}
	key := "id"
	if kind == "attachments/sha" {
		key = "sha"
	}
	return "SELECT workspace_id, created_by FROM " + table + " WHERE " + key + "=? ORDER BY workspace_id=? DESC", []any{value, selected}
}

// scopeTables names the table of each kind that carries its own workspace_id and created_by.
var scopeTables = map[string]string{
	"datasets/id": "datasets", "figures/id": "figures", "boards/id": "boards", "reports/id": "reports", "runs/id": "runs",
	"run-views/id": "run_views", "regions/id": "saved_regions", "masks/id": "masks", "subsets/sid": "subsets",
	"history/id": "exploration_steps", "projects/id": "projects", "attachments/sha": "attachments",
	"discussion/id": "member_comments", "invitations/id": "invitations",
}

// childParams name a part of the object of the route's first parameter (a location of a dataset, an item of a board, a revision
// of a report). They are only valid beside it, and the store looks them up under it, inside its workspace.
var childParams = map[string]string{"datasets/lid": "datasets/id", "boards/pos": "boards/id", "reports/rev": "reports/id"}

// unscopedParams name something that is not workspace data: an account (the admin's routes, the actor's own tokens), a
// workspace (the admin's routes), a member of the actor's current workspace (the handler scopes it), a support bundle held in
// memory for the admin who made it.
var unscopedParams = map[string]bool{"accounts/id": true, "account/id": true, "workspaces/id": true, "members/id": true, "support/id": true}

// patternParams returns the "<segment>/<param>" keys of a route pattern.
func patternParams(pattern string) []string {
	_, path, _ := strings.Cut(pattern, " ")
	parts := strings.Split(strings.TrimPrefix(path, "/api/"), "/")
	var out []string
	for _, p := range parts {
		if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "}") {
			out = append(out, parts[0]+"/"+strings.Trim(p, "{}"))
		}
	}
	return out
}

// scopeOf classifies a pattern's parameters: the one that decides the workspace (or ""), and an error for a parameter nobody
// classified.
func scopeOf(pattern string) (string, error) {
	scoped := ""
	for _, k := range patternParams(pattern) {
		switch {
		case scopeKinds[k]:
			if scoped != "" {
				return "", fmt.Errorf("%s: two parameters decide its workspace", pattern)
			}
			scoped = k
		case childParams[k] != "":
			parent := childParams[k]
			found := false
			for _, q := range patternParams(pattern) {
				found = found || q == parent
			}
			if !found {
				return "", fmt.Errorf("%s: %s is only valid beside %s", pattern, k, parent)
			}
		case unscopedParams[k]:
		default:
			return "", fmt.Errorf("%s: path parameter %s is not classified (scopeKinds, childParams or unscopedParams in workspaces.go)", pattern, k)
		}
	}
	return scoped, nil
}

// objectWorkspace finds the workspace and creator of the object a path parameter names, among the workspaces the account is a
// member of, preferring selected. found is false for a missing object and for one in a workspace the account cannot see, alike.
func (s *Server) objectWorkspace(kind string, value string, accountID string, selected workspace.ID) (ws workspace.ID, role authz.Role, createdBy string, found bool) {
	q, args := scopeLookup(kind, value, selected)
	if q == "" || s.Store == nil {
		return 0, "", "", false
	}
	rows, err := s.Store.DB().Query(q, args...)
	if err != nil {
		log.Printf("workspace of %s %q: %v", kind, value, err)
		return 0, "", "", false
	}
	type hit struct {
		ws workspace.ID
		by string
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.ws, &h.by); err == nil {
			hits = append(hits, h)
		}
	}
	rows.Close()
	for pass := 0; pass < 2; pass++ { // first the selected workspace, then any other the account belongs to
		for _, h := range hits {
			if (pass == 0) != (h.ws == selected) {
				continue
			}
			if r, err := s.MultiUser.Accounts.Role(accountID, h.ws); err == nil {
				return h.ws, authz.Role(r), h.by, true
			}
		}
	}
	return 0, "", "", false
}

// placeActor puts an account's actor in the workspace the request names (header) or its oldest membership, with its role there.
// A header naming a workspace the account is not a member of leaves it in none: every workspace route then answers 403, and
// nothing is read.
func (s *Server) placeActor(r *http.Request, a authz.Actor) authz.Actor {
	st := s.MultiUser.Accounts
	if h := r.Header.Get(WorkspaceHeader); h != "" {
		id, err := strconv.ParseInt(h, 10, 64)
		if err != nil || id <= 0 {
			return a
		}
		if role, err := st.Role(a.ID, workspace.ID(id)); err == nil {
			a.Workspace, a.Role = workspace.ID(id), authz.Role(role)
		}
		return a
	}
	ms, err := st.Memberships(a.ID)
	if err != nil {
		log.Printf("memberships of %s: %v", a.ID, err)
		return a
	}
	if len(ms) > 0 {
		a.Workspace, a.Role = ms[0].Workspace, authz.Role(ms[0].Role)
	}
	return a
}

// ---- data roots per workspace ----

// roots is every folder a request in ws may read. Local mode: the roots the API was started with and the opened projects'
// folders (roots). Group server: only the folders the admin declared for that workspace, canonical, that still exist; never a
// global list, never a folder someone opened.
func (s *Server) roots(ws workspace.ID) sourceroot.Roots {
	if !s.multiUser() {
		return s.localRoots(ws)
	}
	paths, err := s.MultiUser.Accounts.Roots(ws)
	if err != nil {
		log.Printf("roots of workspace %d: %v", ws, err)
		return nil
	}
	var out sourceroot.Roots
	for _, p := range paths {
		if real, err := filepath.EvalSymlinks(p); err == nil && real == p {
			if fi, err := os.Stat(real); err == nil && fi.IsDir() {
				out = append(out, real)
			}
		}
	}
	return out
}

// checkDataRoot says why a folder may not be declared a data root ("" when it may): it must exist, be given canonically (no
// link anywhere in it, so what was declared is what is read), and pass the rules for opening a project folder (not the
// whole disk, a home folder or a folder holding one, a credential folder). It must not hold the catalog, its secrets or the
// managed folders either: a member could read them through the file routes.
func (s *Server) checkDataRoot(p string) string {
	if !filepath.IsAbs(p) {
		return "give the folder as an absolute path"
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "that folder does not exist"
	}
	if real != filepath.Clean(p) {
		return "give the folder's real path (" + real + "): a data root must not pass through a symbolic link"
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return "that is not a folder"
	}
	home, _ := os.UserHomeDir()
	if msg := refuseBroad(real, home); msg != "" {
		return msg
	}
	for _, private := range []string{s.ManagedDir, s.CacheDir, filepath.Dir(s.PublishKeyFile), s.MultiUser.DataDir} {
		if private == "" {
			continue
		}
		if pr, err := filepath.EvalSymlinks(private); err == nil {
			if rel, err := filepath.Rel(real, pr); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
				return "that folder holds the server's own data (" + pr + "): declare a folder of research data instead"
			}
		}
	}
	return ""
}

// ---- members and invitations ----

type memberBody struct {
	accounts.Membership
	You bool `json:"you"`
}

type membersBody struct {
	Workspace   accounts.Workspace    `json:"workspace"`
	Members     []memberBody          `json:"members"`
	Invitations []accounts.Invitation `json:"invitations"` // only for those who may invite
	Roots       []string              `json:"roots"`
}

func (s *Server) memberRoutes(mux *http.ServeMux) {
	s.route(mux, "GET /api/members", authz.MemberRead, s.listMembers)
	s.route(mux, "POST /api/members/{id}/role", authz.MemberManage, s.setMemberRole)
	s.route(mux, "POST /api/members/{id}/remove", authz.MemberManage, s.removeMember)
	s.route(mux, "POST /api/invitations", authz.MemberInvite, s.createInvitation)
	s.route(mux, "POST /api/invitations/{id}/revoke", authz.MemberInvite, s.revokeInvitation)
	s.route(mux, "POST /api/invitations/accept", authz.AccountSelf, s.acceptInvitationSignedIn)
	s.route(mux, "POST /api/auth/invitation", authz.Anonymous, s.peekInvitation)
	s.route(mux, "POST /api/auth/invitation/accept", authz.Anonymous, s.acceptInvitation)
	s.route(mux, "GET /api/workspaces", authz.WorkspaceAdmin, s.listWorkspaces)
	s.route(mux, "POST /api/workspaces", authz.WorkspaceAdmin, s.createWorkspace)
	s.route(mux, "POST /api/workspaces/{id}/members", authz.WorkspaceAdmin, s.adminAddMember)
	s.route(mux, "POST /api/workspaces/{id}/roots", authz.WorkspaceAdmin, s.addWorkspaceRoot)
	s.route(mux, "POST /api/workspaces/{id}/roots/remove", authz.WorkspaceAdmin, s.removeWorkspaceRoot)
	s.route(mux, "POST /api/workspaces/{id}/quota", authz.WorkspaceAdmin, s.setWorkspaceQuota)
}

func actorOf(r *http.Request) authz.Actor { a, _ := authz.From(r.Context()); return a }

// recordMember writes a membership event to the workspace's audit log, with the acting account.
func (s *Server) recordMember(r *http.Request, action, target, detail string) {
	if s.Audit != nil {
		s.Audit.RecordCtx(r.Context(), action, "member", 0, target+": "+detail)
	}
}

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request) {
	a, st := actorOf(r), s.MultiUser.Accounts
	wsp, err := st.Workspace(a.Workspace)
	if err != nil {
		fail(w, 404, "not found")
		return
	}
	ms, err := st.Members(a.Workspace)
	if err != nil {
		log.Printf("members: %v", err)
		fail(w, 500, "internal error")
		return
	}
	out := membersBody{Workspace: wsp, Members: []memberBody{}, Invitations: []accounts.Invitation{}}
	for _, m := range ms {
		out.Members = append(out.Members, memberBody{Membership: m, You: m.AccountID == a.ID})
	}
	if a.Can(authz.MemberInvite) {
		if out.Invitations, err = st.Invitations(a.Workspace); err != nil {
			log.Printf("invitations: %v", err)
			fail(w, 500, "internal error")
			return
		}
	}
	out.Roots, _ = st.Roots(a.Workspace)
	writeJSON(w, 200, out)
}

func (s *Server) setMemberRole(w http.ResponseWriter, r *http.Request) {
	var in struct{ Role string }
	if !decodeSmall(w, r, &in) {
		return
	}
	a, id := actorOf(r), r.PathValue("id")
	if _, err := s.MultiUser.Accounts.Role(id, a.Workspace); err != nil { // not a member here: 404 before anything else is said
		fail(w, 404, "not found")
		return
	}
	old, err := s.MultiUser.Accounts.SetRole(a.Workspace, id, in.Role)
	switch {
	case errors.Is(err, accounts.ErrNotFound):
		fail(w, 404, "not found")
		return
	case errors.Is(err, accounts.ErrBadRole), errors.Is(err, accounts.ErrLastOwner):
		fail(w, 422, err.Error())
		return
	case err != nil:
		log.Printf("set role: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.recordMember(r, "role changed", id, old+" -> "+in.Role)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request) {
	a, id := actorOf(r), r.PathValue("id")
	old, err := s.MultiUser.Accounts.RemoveMember(a.Workspace, id)
	switch {
	case errors.Is(err, accounts.ErrNotFound):
		fail(w, 404, "not found")
		return
	case errors.Is(err, accounts.ErrLastOwner):
		fail(w, 422, err.Error())
		return
	case err != nil:
		log.Printf("remove member: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.recordMember(r, "member removed", id, "was "+old)
	w.WriteHeader(http.StatusNoContent)
}

type invitationCreated struct {
	accounts.Invitation
	// Link is shown once: the secret is in the URL fragment, so it never reaches a request line, a proxy log or a Referer.
	Link string `json:"link"`
}

func (s *Server) createInvitation(w http.ResponseWriter, r *http.Request) {
	var in struct{ Role, Note string }
	if !decodeSmall(w, r, &in) {
		return
	}
	a := actorOf(r)
	if !authz.Role(in.Role).Known() {
		fail(w, 422, accounts.ErrBadRole.Error())
		return
	}
	if !a.MayInvite(authz.Role(in.Role)) {
		fail(w, 403, "a Maintainer may invite Viewers and Researchers; ask an Owner to invite a Maintainer or an Owner")
		return
	}
	secret, inv, err := s.MultiUser.Accounts.CreateInvitation(a.Workspace, in.Role, in.Note, a.ID)
	if err != nil {
		log.Printf("invite: %v", err)
		fail(w, 422, err.Error())
		return
	}
	s.recordMember(r, "invitation created", strconv.FormatInt(inv.ID, 10), "as "+in.Role)
	writeJSON(w, 201, invitationCreated{Invitation: inv, Link: strings.TrimRight(s.MultiUser.PublicOrigin, "/") + "/#/invite/" + secret})
}

func (s *Server) revokeInvitation(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 404, "not found")
		return
	}
	a := actorOf(r)
	inv, err := s.MultiUser.Accounts.RevokeInvitation(a.Workspace, id)
	if errors.Is(err, accounts.ErrNotFound) {
		fail(w, 404, "not found")
		return
	} else if err != nil {
		log.Printf("revoke invitation: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.recordMember(r, "invitation revoked", strconv.FormatInt(inv.ID, 10), "as "+inv.Role)
	w.WriteHeader(http.StatusNoContent)
}

type invitationPeek struct {
	Workspace string `json:"workspace"`
	Role      string `json:"role"`
	Note      string `json:"note"`
	Expires   string `json:"expiresAt"`
}

// peekInvitation tells the invitation page what it is for. Limited like the setup link: the secret is 256 bits, the limiter
// stops a client that tries many.
func (s *Server) peekInvitation(w http.ResponseWriter, r *http.Request) {
	var in struct{ Token string }
	if !decodeSmall(w, r, &in) {
		return
	}
	att, ok, wait := s.MultiUser.Limiter.Begin("invite:"+s.clientAddr(r), s.limitAddr(r))
	if !ok {
		tooMany(w, wait)
		return
	}
	defer att.Done()
	inv, name, err := s.MultiUser.Accounts.PeekInvitation(in.Token)
	if errors.Is(err, accounts.ErrInvitationInvalid) {
		att.Fail()
		fail(w, 400, err.Error())
		return
	} else if err != nil {
		log.Printf("invitation: %v", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, invitationPeek{Workspace: name, Role: inv.Role, Note: inv.Note, Expires: inv.Expires.Format("2006-01-02T15:04:05Z")})
}

// acceptInvitation creates the invited person's account (name, username, password) with the invitation's role, then starts a
// pending session at the second-factor enrolment, as the setup link does. There is no other way to make an account.
func (s *Server) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	var in struct{ Token, Name, Username, Password string }
	if !decodeSmall(w, r, &in) {
		return
	}
	client := s.clientAddr(r)
	att, ok, wait := s.MultiUser.Limiter.Begin("invite:"+client, s.limitAddr(r))
	if !ok {
		tooMany(w, wait)
		return
	}
	defer att.Done()
	a, inv, err := s.MultiUser.Accounts.AcceptInvitation(in.Token, in.Name, in.Username, in.Password)
	var weak accounts.ErrWeakPassword
	switch {
	case errors.Is(err, accounts.ErrBusy):
		busy(w)
		return
	case errors.Is(err, accounts.ErrInvitationInvalid):
		att.Fail()
		s.accountAudit(anonymous, "invitation refused", "from "+client)
		fail(w, 400, err.Error())
		return
	case errors.As(err, &weak), errors.Is(err, accounts.ErrBadName), errors.Is(err, accounts.ErrBadUsername), errors.Is(err, accounts.ErrUsernameTaken):
		fail(w, 422, err.Error())
		return
	case err != nil:
		log.Printf("accept invitation: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.accountAudit(sessionActor(a.ID), "account created from an invitation", fmt.Sprintf("%s joined workspace %d as %s, from %s", a.Username, inv.Workspace, inv.Role, client))
	stage, ok := s.startPending(w, r, a)
	if ok {
		writeJSON(w, 201, s.stateFor(a.ID, stage, false))
	}
}

// acceptInvitationSignedIn lets an existing account join another workspace with an invitation.
func (s *Server) acceptInvitationSignedIn(w http.ResponseWriter, r *http.Request) {
	var in struct{ Token string }
	if !decodeSmall(w, r, &in) {
		return
	}
	a := actorOf(r)
	inv, err := s.MultiUser.Accounts.AcceptInvitationAs(in.Token, a.ID)
	switch {
	case errors.Is(err, accounts.ErrInvitationInvalid), errors.Is(err, accounts.ErrAlreadyMember):
		fail(w, 400, err.Error())
		return
	case err != nil:
		log.Printf("accept invitation: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.accountAudit(sessionActor(a.ID), "joined a workspace from an invitation", fmt.Sprintf("workspace %d as %s", inv.Workspace, inv.Role))
	writeJSON(w, 200, map[string]any{"workspaceId": inv.Workspace, "role": inv.Role})
}

// ---- the admin's workspace routes ----

func (s *Server) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	ws, err := s.MultiUser.Accounts.AllWorkspaces()
	if err != nil {
		log.Printf("workspaces: %v", err)
		fail(w, 500, "internal error")
		return
	}
	type row struct {
		accounts.Workspace
		Roots []string `json:"roots"`
	}
	out := []row{}
	for _, x := range ws {
		roots, _ := s.MultiUser.Accounts.Roots(x.ID)
		out = append(out, row{Workspace: x, Roots: roots})
	}
	writeJSON(w, 200, map[string]any{"workspaces": out})
}

func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name, Owner string } // Owner is a username
	if !decodeSmall(w, r, &in) {
		return
	}
	st := s.MultiUser.Accounts
	owner, err := st.ByUsername(in.Owner)
	if err != nil {
		fail(w, 422, "no account has that username")
		return
	}
	a := actorOf(r)
	wsp, err := st.CreateWorkspace(in.Name, owner.ID, a.ID)
	if errors.Is(err, accounts.ErrBadWorkspaceName) {
		fail(w, 422, err.Error())
		return
	} else if err != nil {
		log.Printf("create workspace: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.accountAudit(sessionActor(a.ID), "workspace created", fmt.Sprintf("workspace %d %q, Owner %s", wsp.ID, wsp.Name, owner.Username))
	writeJSON(w, 201, wsp)
}

func adminWorkspace(w http.ResponseWriter, r *http.Request) (workspace.ID, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(w, 404, "not found")
		return 0, false
	}
	return workspace.ID(id), true
}

func (s *Server) adminAddMember(w http.ResponseWriter, r *http.Request) {
	ws, ok := adminWorkspace(w, r)
	if !ok {
		return
	}
	var in struct{ Username, Role string }
	if !decodeSmall(w, r, &in) {
		return
	}
	st := s.MultiUser.Accounts
	acct, err := st.ByUsername(in.Username)
	if err != nil {
		fail(w, 422, "no account has that username")
		return
	}
	switch err := st.AddMember(ws, acct.ID, in.Role); {
	case errors.Is(err, accounts.ErrNotFound):
		fail(w, 404, "not found")
		return
	case errors.Is(err, accounts.ErrBadRole), errors.Is(err, accounts.ErrAlreadyMember):
		fail(w, 422, err.Error())
		return
	case err != nil:
		log.Printf("add member: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.accountAudit(sessionActor(actorOf(r).ID), "member added by the admin", fmt.Sprintf("%s to workspace %d as %s", acct.Username, ws, in.Role))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) addWorkspaceRoot(w http.ResponseWriter, r *http.Request) {
	ws, ok := adminWorkspace(w, r)
	if !ok {
		return
	}
	var in struct{ Path string }
	if !decodeSmall(w, r, &in) {
		return
	}
	if _, err := s.MultiUser.Accounts.Workspace(ws); err != nil {
		fail(w, 404, "not found")
		return
	}
	if msg := s.checkDataRoot(in.Path); msg != "" {
		fail(w, 422, msg)
		return
	}
	p := filepath.Clean(in.Path)
	if err := s.MultiUser.Accounts.AddRoot(ws, p, actorOf(r).ID); err != nil {
		log.Printf("add root: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.accountAudit(sessionActor(actorOf(r).ID), "data root declared", fmt.Sprintf("%s for workspace %d", p, ws))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeWorkspaceRoot(w http.ResponseWriter, r *http.Request) {
	ws, ok := adminWorkspace(w, r)
	if !ok {
		return
	}
	var in struct{ Path string }
	if !decodeSmall(w, r, &in) {
		return
	}
	if err := s.MultiUser.Accounts.RemoveRoot(ws, in.Path); errors.Is(err, accounts.ErrNotFound) {
		fail(w, 404, "not found")
		return
	} else if err != nil {
		log.Printf("remove root: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.accountAudit(sessionActor(actorOf(r).ID), "data root withdrawn", fmt.Sprintf("%s from workspace %d", in.Path, ws))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setWorkspaceQuota(w http.ResponseWriter, r *http.Request) {
	ws, ok := adminWorkspace(w, r)
	if !ok {
		return
	}
	var in struct {
		Bytes int64 `json:"bytes"`
	}
	if !decodeSmall(w, r, &in) {
		return
	}
	if err := s.MultiUser.Accounts.SetQuota(ws, in.Bytes); errors.Is(err, accounts.ErrNotFound) {
		fail(w, 404, "not found")
		return
	} else if err != nil {
		fail(w, 422, err.Error())
		return
	}
	s.accountAudit(sessionActor(actorOf(r).ID), "workspace quota set", fmt.Sprintf("workspace %d: %d bytes", ws, in.Bytes))
	w.WriteHeader(http.StatusNoContent)
}
