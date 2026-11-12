package server

import (
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/accounts"
	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/authz"
)

// The group server's sign-in, second factor, sessions and personal access tokens (ADR 0050 section 2, slice 2). Mounted only
// with --multi-user. Signing in is two steps: the password, which yields a pending session good only for the second step for
// ten minutes, then a TOTP code or a recovery code (or, the first time, enrolling TOTP), which replaces it with a full session
// under a new id. Every outcome is audited with the real actor; no password, code, token or hash is ever written to the log.
func (s *Server) accountRoutes(mux *http.ServeMux) {
	s.route(mux, "GET /api/auth/state", authz.Anonymous, s.authState)
	s.route(mux, "POST /api/auth/setup", authz.Anonymous, s.authSetup)
	s.route(mux, "POST /api/auth/sign-in", authz.Anonymous, s.signIn)
	s.route(mux, "POST /api/auth/second-factor", authz.Anonymous, s.secondFactor)
	s.route(mux, "POST /api/auth/enrolment", authz.Anonymous, s.pendingEnrolment)
	s.route(mux, "POST /api/auth/enrolment/confirm", authz.Anonymous, s.pendingEnrolmentConfirm)
	s.route(mux, "POST /api/auth/sign-out", authz.Anonymous, s.signOut)
	s.route(mux, "POST /api/auth/sign-out-everywhere", authz.AccountSelf, s.signOutEverywhere)
	s.route(mux, "POST /api/auth/confirm", authz.AccountSelf, s.confirmSignIn)
	s.route(mux, "POST /api/account/second-factor", authz.AccountCredentials, s.reenrol)
	s.route(mux, "POST /api/account/second-factor/confirm", authz.AccountCredentials, s.reenrolConfirm)
	s.route(mux, "POST /api/account/recovery-codes", authz.AccountCredentials, s.newRecoveryCodes)
	s.route(mux, "GET /api/account/tokens", authz.AccountSelf, s.listTokens)
	s.route(mux, "POST /api/account/tokens", authz.AccountCredentials, s.createToken)
	s.route(mux, "POST /api/account/tokens/{id}/revoke", authz.AccountSelf, s.revokeToken)
	s.route(mux, "GET /api/accounts", authz.AccountAdmin, s.listAccounts)
	s.route(mux, "POST /api/accounts/{id}/reset-second-factor", authz.AccountAdmin, s.resetSecondFactor)
}

// The answer to a wrong username or password, identical for an unknown name and a known one (status and body).
const wrongCredentials = "wrong username or password"

var anonymous = audit.Actor{ID: "anonymous", Via: "none"}

func (s *Server) accountAudit(a audit.Actor, action, detail string) {
	s.Audit.RecordAs(a, action, "account", 0, detail)
}

func sessionActor(id string) audit.Actor { return audit.Actor{ID: id, Via: string(authz.ViaSession)} }

func decodeSmall(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		fail(w, 400, "invalid request body")
		return false
	}
	return true
}

type authStateBody struct {
	Stage         string            `json:"stage"` // none, second_factor, enrol, full
	SetupNeeded   bool              `json:"setupNeeded"`
	Account       *accounts.Account `json:"account"`
	Recent        bool              `json:"recent"`
	RecoveryLeft  int               `json:"recoveryCodesLeft"`
	RecentMinutes int               `json:"recentMinutes"`
}

func (s *Server) stateFor(acctID, stage string, recent bool) authStateBody {
	st := s.MultiUser.Accounts
	out := authStateBody{Stage: stage, Recent: recent, RecentMinutes: int(accounts.RecentWindow.Minutes())}
	if has, err := st.HasAdmin(); err == nil && !has {
		out.SetupNeeded = true
	}
	if acctID != "" {
		if a, err := st.Get(acctID); err == nil {
			out.Account = &a
			out.RecoveryLeft, _ = st.RecoveryCodesLeft(acctID)
		}
	}
	return out
}

// authState says where the browser is in signing in. It needs no actor: the page shows sign-in, the second step or the app
// from it, and "run the bootstrap command" on a server with no admin.
func (s *Server) authState(w http.ResponseWriter, r *http.Request) {
	if a, ok := authz.From(r.Context()); ok {
		writeJSON(w, 200, s.stateFor(a.ID, accounts.StageFull, a.Recent))
		return
	}
	if se, ok := pendingSession(r.Context()); ok {
		writeJSON(w, 200, s.stateFor(se.AccountID, se.Stage, false))
		return
	}
	writeJSON(w, 200, s.stateFor("", "none", false))
}

// startPending ends whatever session the request named and starts a pending one: the id the browser had, whoever chose it,
// is never carried into a sign-in.
func (s *Server) startPending(w http.ResponseWriter, r *http.Request, a accounts.Account) (string, bool) {
	st := s.MultiUser.Accounts
	if c, err := r.Cookie(SessionCookie); err == nil {
		_ = st.EndSession(accounts.Hash(c.Value))
	}
	stage := accounts.StageSecondFactor
	if !a.SecondFactor {
		stage = accounts.StageEnrol
	}
	id, err := st.CreateSession(a.ID, stage, s.clientAddr(r), r.UserAgent())
	if err != nil {
		log.Printf("sign-in: %v", err)
		fail(w, 500, "internal error")
		return "", false
	}
	setSession(w, id)
	return stage, true
}

// authSetup spends the one-time setup link the bootstrap command printed and creates that admin, who then enrols TOTP.
func (s *Server) authSetup(w http.ResponseWriter, r *http.Request) {
	var in struct{ Token, Name, Username, Password string }
	if !decodeSmall(w, r, &in) {
		return
	}
	client := s.clientAddr(r)
	if ok, wait := s.MultiUser.Limiter.Allow("setup:"+client, client); !ok {
		tooMany(w, wait)
		return
	}
	a, err := s.MultiUser.Accounts.CreateAdminFromSetup(in.Token, in.Name, in.Username, in.Password)
	var weak accounts.ErrWeakPassword
	switch {
	case errors.Is(err, accounts.ErrSetupInvalid):
		s.MultiUser.Limiter.Fail("setup:"+client, client)
		s.accountAudit(anonymous, "setup link refused", "from "+client)
		fail(w, 400, err.Error())
		return
	case errors.As(err, &weak), errors.Is(err, accounts.ErrBadName), errors.Is(err, accounts.ErrBadUsername), errors.Is(err, accounts.ErrUsernameTaken):
		fail(w, 422, err.Error())
		return
	case err != nil:
		log.Printf("setup: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.accountAudit(sessionActor(a.ID), "admin account created from the setup link", a.Username+" from "+client)
	stage, ok := s.startPending(w, r, a)
	if ok {
		writeJSON(w, 201, s.stateFor(a.ID, stage, false))
	}
}

func tooMany(w http.ResponseWriter, wait time.Duration) {
	secs := int(wait.Round(time.Second).Seconds())
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", fmt.Sprint(secs))
	fail(w, http.StatusTooManyRequests, fmt.Sprintf("too many attempts; try again in %s", wait.Round(time.Second)))
}

// limitKey is the name failures are counted against: the username as typed, normalised and bounded, whether or not an
// account has it.
func limitKey(username string) string {
	k := accounts.NormalizeUsername(username)
	if len(k) > 64 {
		k = k[:64]
	}
	return "user:" + k
}

// signIn checks the password. The answer for an unknown username, a disabled account and a wrong password is the same 401
// with the same body, after the same argon2 work; the backoff is counted against the name typed either way, so it says
// nothing about whether the name exists either.
func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password string }
	if !decodeSmall(w, r, &in) {
		return
	}
	st, lim := s.MultiUser.Accounts, s.MultiUser.Limiter
	key, client := limitKey(in.Username), s.clientAddr(r)
	if ok, wait := lim.Allow(key, client); !ok {
		s.accountAudit(anonymous, "sign-in refused: too many attempts", "from "+client)
		tooMany(w, wait)
		return
	}
	a, ok, err := st.CheckPassword(in.Username, in.Password)
	if err != nil {
		log.Printf("sign-in: %v", err)
		fail(w, 500, "internal error")
		return
	}
	if !ok {
		lim.Fail(key, client)
		if known, err := st.ByUsername(in.Username); err == nil {
			s.accountAudit(sessionActor(known.ID), "sign-in failed", "wrong password, from "+client)
		} else { // the name typed is not recorded: it may be a password typed into the wrong field
			s.accountAudit(anonymous, "sign-in failed", "unknown username, from "+client)
		}
		fail(w, http.StatusUnauthorized, wrongCredentials)
		return
	}
	stage, ok := s.startPending(w, r, a)
	if !ok {
		return
	}
	s.accountAudit(sessionActor(a.ID), "password accepted", "second factor owed ("+stage+"), from "+client)
	writeJSON(w, 200, s.stateFor(a.ID, stage, false))
}

// secondFactor finishes signing in with a TOTP code or a recovery code; the session id changes.
func (s *Server) secondFactor(w http.ResponseWriter, r *http.Request) {
	se, ok := pendingSession(r.Context())
	if !ok || se.Stage != accounts.StageSecondFactor {
		failCode(w, http.StatusUnauthorized, "signin_required", "sign in with your password first", nil)
		return
	}
	var in struct {
		Code         string `json:"code"`
		RecoveryCode string `json:"recoveryCode"`
	}
	if !decodeSmall(w, r, &in) {
		return
	}
	st, lim := s.MultiUser.Accounts, s.MultiUser.Limiter
	a, err := st.Get(se.AccountID)
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	key, client := limitKey(a.Username), s.clientAddr(r)
	if ok, wait := lim.Allow(key, client); !ok {
		tooMany(w, wait)
		return
	}
	how, good := "", false
	switch {
	case in.RecoveryCode != "":
		how = "a recovery code"
		good, err = st.UseRecoveryCode(a.ID, in.RecoveryCode)
	default:
		how = "TOTP"
		good, err = st.VerifyTOTP(a.ID, strings.TrimSpace(in.Code))
	}
	if err != nil {
		log.Printf("second factor: %v", err)
		fail(w, 500, "internal error")
		return
	}
	if !good {
		lim.Fail(key, client)
		s.accountAudit(sessionActor(a.ID), "sign-in failed", "wrong or already used "+how+", from "+client)
		fail(w, http.StatusUnauthorized, "wrong or already used code")
		return
	}
	lim.Succeed(key)
	id, err := st.Promote(se, client, r.UserAgent(), false)
	if err != nil {
		failCode(w, http.StatusUnauthorized, "signin_required", "this sign-in has ended; start again", nil)
		return
	}
	setSession(w, id)
	detail := "with " + how + ", from " + client
	if how != "TOTP" {
		left, _ := st.RecoveryCodesLeft(a.ID)
		detail += fmt.Sprintf("; %d recovery codes left", left)
	}
	s.accountAudit(sessionActor(a.ID), "signed in", detail)
	writeJSON(w, 200, s.stateFor(a.ID, accounts.StageFull, true))
}

type enrolmentBody struct {
	Secret string `json:"secret"` // base32, for typing into an app
	URI    string `json:"uri"`    // otpauth://, for a QR code or a link
}

func (s *Server) issuer() string {
	host := s.MultiUser.PublicOrigin
	if u, err := url.Parse(host); err == nil {
		host = u.Host
	}
	return "Vesqira (" + host + ")"
}

func (s *Server) startEnrolment(w http.ResponseWriter, a accounts.Account) {
	secret, err := s.MultiUser.Accounts.StartEnrolment(a.ID)
	if err != nil {
		log.Printf("enrolment: %v", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, enrolmentBody{Secret: base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret), URI: accounts.OTPAuthURI(s.issuer(), a.Username, secret)})
}

// confirmEnrolment checks the first code from the new secret and returns the recovery codes, once. ok reports success.
func (s *Server) confirmEnrolment(w http.ResponseWriter, r *http.Request, a accounts.Account, first bool) ([]string, bool) {
	var in struct {
		Code string `json:"code"`
	}
	if !decodeSmall(w, r, &in) {
		return nil, false
	}
	st, lim := s.MultiUser.Accounts, s.MultiUser.Limiter
	key, client := limitKey(a.Username), s.clientAddr(r)
	if ok, wait := lim.Allow(key, client); !ok {
		tooMany(w, wait)
		return nil, false
	}
	confirm := st.ConfirmEnrolment
	if first {
		confirm = st.ConfirmFirstEnrolment
	}
	codes, ok, err := confirm(a.ID, strings.TrimSpace(in.Code))
	switch {
	case errors.Is(err, accounts.ErrNoEnrolment):
		fail(w, 409, "start the enrolment first")
		return nil, false
	case err != nil:
		log.Printf("enrolment: %v", err)
		fail(w, 500, "internal error")
		return nil, false
	case !ok:
		lim.Fail(key, client)
		fail(w, http.StatusUnauthorized, "wrong code: check the app shows this account and the phone's clock is right")
		return nil, false
	}
	lim.Succeed(key)
	s.accountAudit(sessionActor(a.ID), "second factor enrolled", "TOTP; 10 new recovery codes, from "+client)
	return codes, true
}

func (s *Server) pendingEnrolAccount(w http.ResponseWriter, r *http.Request) (accounts.Session, accounts.Account, bool) {
	se, ok := pendingSession(r.Context())
	if !ok || se.Stage != accounts.StageEnrol {
		failCode(w, http.StatusUnauthorized, "signin_required", "sign in with your password first", nil)
		return se, accounts.Account{}, false
	}
	a, err := s.MultiUser.Accounts.Get(se.AccountID)
	if err != nil {
		fail(w, 500, "internal error")
		return se, a, false
	}
	if a.SecondFactor { // enrolled meanwhile (another sign-in): this session owes a code now, never a new secret
		_ = s.MultiUser.Accounts.SetStage(se, accounts.StageSecondFactor)
		failCode(w, http.StatusConflict, "second_factor_owed", "the second factor is already set up: enter a code from the app", nil)
		return se, a, false
	}
	return se, a, true
}

// pendingEnrolment starts TOTP enrolment for an account that signed in with its password and has no second factor yet.
func (s *Server) pendingEnrolment(w http.ResponseWriter, r *http.Request) {
	if _, a, ok := s.pendingEnrolAccount(w, r); ok {
		s.startEnrolment(w, a)
	}
}

func (s *Server) pendingEnrolmentConfirm(w http.ResponseWriter, r *http.Request) {
	se, a, ok := s.pendingEnrolAccount(w, r)
	if !ok {
		return
	}
	codes, ok := s.confirmEnrolment(w, r, a, true)
	if !ok {
		return
	}
	id, err := s.MultiUser.Accounts.Promote(se, s.clientAddr(r), r.UserAgent(), false)
	if err != nil {
		failCode(w, http.StatusUnauthorized, "signin_required", "this sign-in has ended; start again", nil)
		return
	}
	setSession(w, id)
	s.accountAudit(sessionActor(a.ID), "signed in", "after enrolling TOTP, from "+s.clientAddr(r))
	writeJSON(w, 200, map[string]any{"recoveryCodes": codes, "state": s.stateFor(a.ID, accounts.StageFull, true)})
}

// signOut ends the session the cookie names, pending or full, and clears the cookie.
func (s *Server) signOut(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil {
		_ = s.MultiUser.Accounts.EndSession(accounts.Hash(c.Value))
	}
	clearSession(w)
	if a, ok := authz.From(r.Context()); ok {
		s.accountAudit(sessionActor(a.ID), "signed out", "from "+s.clientAddr(r))
	} else if se, ok := pendingSession(r.Context()); ok {
		s.accountAudit(sessionActor(se.AccountID), "signed out", "before the second factor, from "+s.clientAddr(r))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) signOutEverywhere(w http.ResponseWriter, r *http.Request) {
	a, _ := authz.From(r.Context())
	n, err := s.MultiUser.Accounts.EndSessions(a.ID)
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	clearSession(w)
	kept, _ := s.MultiUser.Accounts.LiveTokens(a.ID)
	s.accountAudit(sessionActor(a.ID), "signed out everywhere", fmt.Sprintf("%d sessions ended, %d access tokens kept, from %s", n, kept, s.clientAddr(r)))
	// tokens are for programs, not browsers: signing out does not revoke them, and the answer says so
	writeJSON(w, 200, map[string]any{"sessionsEnded": n, "tokensKept": kept, "note": "access tokens stay valid; revoke them under Tokens"})
}

// confirmSignIn proves the password and the second factor again for a recent sign-in (needed to create a token, delete,
// publish outside the group and for admin actions). The session id changes; its absolute timeout does not move.
func (s *Server) confirmSignIn(w http.ResponseWriter, r *http.Request) {
	se, ok := currentSession(r.Context())
	if !ok {
		fail(w, http.StatusForbidden, "only a signed-in browser session can confirm")
		return
	}
	var in struct {
		Password     string `json:"password"`
		Code         string `json:"code"`
		RecoveryCode string `json:"recoveryCode"`
	}
	if !decodeSmall(w, r, &in) {
		return
	}
	st, lim := s.MultiUser.Accounts, s.MultiUser.Limiter
	a, err := st.Get(se.AccountID)
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	key, client := limitKey(a.Username), s.clientAddr(r)
	if ok, wait := lim.Allow(key, client); !ok {
		tooMany(w, wait)
		return
	}
	_, good, err := st.CheckPassword(a.Username, in.Password)
	if err == nil && good {
		if in.RecoveryCode != "" {
			good, err = st.UseRecoveryCode(a.ID, in.RecoveryCode)
		} else {
			good, err = st.VerifyTOTP(a.ID, strings.TrimSpace(in.Code))
		}
	}
	if err != nil {
		log.Printf("confirm: %v", err)
		fail(w, 500, "internal error")
		return
	}
	if !good {
		lim.Fail(key, client)
		s.accountAudit(sessionActor(a.ID), "sign-in confirmation failed", "from "+client)
		fail(w, http.StatusUnauthorized, "wrong password or code")
		return
	}
	lim.Succeed(key)
	id, err := st.Promote(se, client, r.UserAgent(), true)
	if err != nil {
		failCode(w, http.StatusUnauthorized, "signin_required", "this session has ended; sign in again", nil)
		return
	}
	setSession(w, id)
	s.accountAudit(sessionActor(a.ID), "sign-in confirmed", "from "+client)
	writeJSON(w, 200, s.stateFor(a.ID, accounts.StageFull, true))
}

func (s *Server) actorAccount(w http.ResponseWriter, r *http.Request) (accounts.Account, bool) {
	a, _ := authz.From(r.Context())
	acct, err := s.MultiUser.Accounts.Get(a.ID)
	if err != nil {
		fail(w, 500, "internal error")
		return acct, false
	}
	return acct, true
}

// reenrol replaces the second factor of a signed-in account (a new phone). The old secret stays in force until a code from
// the new one confirms it.
func (s *Server) reenrol(w http.ResponseWriter, r *http.Request) {
	if a, ok := s.actorAccount(w, r); ok {
		s.startEnrolment(w, a)
	}
}

func (s *Server) reenrolConfirm(w http.ResponseWriter, r *http.Request) {
	a, ok := s.actorAccount(w, r)
	if !ok {
		return
	}
	if codes, ok := s.confirmEnrolment(w, r, a, false); ok {
		writeJSON(w, 200, map[string]any{"recoveryCodes": codes})
	}
}

func (s *Server) newRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	a, _ := authz.From(r.Context())
	codes, err := s.MultiUser.Accounts.NewRecoveryCodes(a.ID)
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	s.accountAudit(sessionActor(a.ID), "recovery codes replaced", "10 new codes, from "+s.clientAddr(r))
	writeJSON(w, 200, map[string]any{"recoveryCodes": codes})
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	a, _ := authz.From(r.Context())
	ts, err := s.MultiUser.Accounts.Tokens(a.ID)
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, map[string]any{"tokens": ts, "scopes": authz.Scopes()})
}

// createToken mints a personal access token. Its value is in this answer and nowhere else, ever.
func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
		Days   int      `json:"days"`
	}
	if !decodeSmall(w, r, &in) {
		return
	}
	if in.Days < 0 || in.Days > 365 {
		fail(w, 422, "a token lasts 1 to 365 days")
		return
	}
	a, _ := authz.From(r.Context())
	raw, tok, err := s.MultiUser.Accounts.CreateToken(a.ID, in.Name, in.Scopes, time.Duration(in.Days)*24*time.Hour)
	if err != nil {
		if errors.Is(err, accounts.ErrBadName) || errors.Is(err, accounts.ErrBadScopes) || strings.HasPrefix(err.Error(), "a token lasts") {
			fail(w, 422, err.Error())
			return
		}
		log.Printf("token: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.accountAudit(sessionActor(a.ID), "access token created", fmt.Sprintf("%s %q, scopes %s, expires %s", tok.ID, tok.Name, strings.Join(tok.Scopes, " "), tok.Expires.Format("2006-01-02")))
	writeJSON(w, 201, map[string]any{"token": raw, "info": tok})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	a, _ := authz.From(r.Context())
	tok, err := s.MultiUser.Accounts.RevokeToken(a.ID, r.PathValue("id"))
	if errors.Is(err, accounts.ErrNotFound) {
		fail(w, 404, "token not found")
		return
	}
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	s.accountAudit(sessionActor(a.ID), "access token revoked", fmt.Sprintf("%s %q", tok.ID, tok.Name))
	writeJSON(w, 200, tok)
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	as, err := s.MultiUser.Accounts.List()
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, map[string]any{"accounts": as})
}

// resetSecondFactor is the admin reset path: the account's TOTP secret and recovery codes are removed, its sessions end and
// its access tokens are revoked, so its next sign-in enrols again.
func (s *Server) resetSecondFactor(w http.ResponseWriter, r *http.Request) {
	a, _ := authz.From(r.Context())
	target, err := s.MultiUser.Accounts.Get(r.PathValue("id"))
	if errors.Is(err, accounts.ErrNotFound) {
		fail(w, 404, "account not found")
		return
	}
	var sessions, tokens int64
	if err == nil {
		sessions, tokens, err = s.MultiUser.Accounts.ResetSecondFactor(target.ID)
	}
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	s.accountAudit(sessionActor(a.ID), "second factor reset", "for "+target.ID+" ("+target.Username+")")
	s.accountAudit(sessionActor(a.ID), "sessions ended by the reset", fmt.Sprintf("%d sessions of %s", sessions, target.ID))
	s.accountAudit(sessionActor(a.ID), "access tokens revoked by the reset", fmt.Sprintf("%d tokens of %s", tokens, target.ID))
	w.WriteHeader(http.StatusNoContent)
}
