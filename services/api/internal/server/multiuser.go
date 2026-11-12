package server

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/accounts"
	"github.com/unsound-mint/vesqira/services/api/internal/authz"
)

// MultiUser makes the owner listener the group server (--multi-user, ADR 0050 slice 2) when PublicOrigin is set. It is a
// value, not a pointer, so a test fixture that fills every pointer field of Server does not switch modes by accident.
type MultiUser struct {
	// PublicOrigin is the https origin people reach the server at (--public-origin): the Host every request must name and the
	// Origin every browser write must carry.
	PublicOrigin string
	Accounts     *accounts.Store
	Limiter      *accounts.Limiter
	// ClientIPHeader names the header a trusted reverse proxy fills with the client address (--client-ip-header); without it
	// every client behind the proxy shares the proxy's address for the per-address limit.
	ClientIPHeader string
}

func (s *Server) multiUser() bool { return s.MultiUser.PublicOrigin != "" }

// SessionCookie is the session cookie's name. The __Host- prefix makes the browser refuse it unless it is Secure, has Path=/
// and no Domain, so no other host or path can set or shadow it.
const SessionCookie = "__Host-vesqira"

// setSession and clearSession are the only places a cookie is written. The attributes never change with the mode or a flag.
func setSession(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: id, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(accounts.AbsoluteTimeout.Seconds())})
}

func clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

// groupOnly is the group server's version of localOnly (ADR 0050 section 2, CSRF strategy):
//   - Host must be the host of --public-origin (the DNS-rebinding guard; forwarded headers are never consulted for it);
//   - a cross-site Sec-Fetch-Site is refused on every request, except a top-level navigation to the app shell (a link
//     clicked in a mail); /api is never a navigation exception;
//   - every request other than GET and HEAD must carry Origin equal to --public-origin (a missing Origin is refused) and a
//     Sec-Fetch-Site of same-origin or none at all, unless it authenticates with a bearer token, which nothing ambient carries;
//   - a body is application/json only, so a cross-origin write would need a preflight, which is never granted.
func (s *Server) groupOnly(next http.Handler) http.Handler {
	u, err := url.Parse(s.MultiUser.PublicOrigin)
	if err != nil || u.Host == "" {
		panic("server: MultiUser.PublicOrigin is not an origin: " + s.MultiUser.PublicOrigin)
	}
	origin := u.Scheme + "://" + u.Host
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Host, u.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		site := r.Header.Get("Sec-Fetch-Site")
		crossSite := site != "" && site != "same-origin" && site != "none"
		safe := r.Method == http.MethodGet || r.Method == http.MethodHead
		if safe {
			navigation := r.Header.Get("Sec-Fetch-Mode") == "navigate" && !isAPIPath(r.URL.Path)
			if crossSite && !navigation {
				http.Error(w, "cross-site request refused", http.StatusForbidden)
				return
			}
		} else if r.Header.Get("Authorization") == "" {
			if r.Header.Get("Origin") != origin {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
			if site != "" && site != "same-origin" {
				http.Error(w, "cross-site request refused", http.StatusForbidden)
				return
			}
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isAPIPath(p string) bool { return p == "/api" || strings.HasPrefix(p, "/api/") }

type pendingKey struct{}

// pendingSession is the session of a person who gave the right password and still owes the second factor (or its enrolment).
// It is no actor: it reaches only the sign-in steps.
func pendingSession(ctx context.Context) (accounts.Session, bool) {
	se, ok := ctx.Value(pendingKey{}).(accounts.Session)
	return se, ok
}

type sessionKey struct{}

// currentSession is the full session the request's actor came through, for the routes that act on it (sign out, confirm).
func currentSession(ctx context.Context) (accounts.Session, bool) {
	se, ok := ctx.Value(sessionKey{}).(accounts.Session)
	return se, ok
}

// withAccount is the group server's actor middleware, in place of withLocalOwner: it resolves a bearer token or the session
// cookie to an authz.Actor. A request with both is refused (it could only be a mistake or a confusion attack). A bearer value
// that is not a live token is refused outright rather than treated as anonymous. Without either, the request carries no
// actor and every route that needs one answers 401.
func (s *Server) withAccount(next http.Handler) http.Handler {
	st := s.MultiUser.Accounts
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), signInKey{}, true)
		bearer := r.Header.Get("Authorization")
		cookie, _ := r.Cookie(SessionCookie)
		if bearer != "" && cookie != nil {
			fail(w, http.StatusBadRequest, "send a session cookie or an access token, not both")
			return
		}
		switch {
		case bearer != "":
			raw, ok := strings.CutPrefix(bearer, "Bearer ")
			var tok accounts.Token
			var acct accounts.Account
			var err error
			if ok {
				if tok, err = st.LookupToken(strings.TrimSpace(raw)); err == nil {
					acct, err = st.Get(tok.AccountID)
				}
			}
			if !ok || err != nil {
				if err != nil && !errors.Is(err, accounts.ErrNotFound) {
					log.Printf("token lookup: %v", err)
				}
				failCode(w, http.StatusUnauthorized, "token_invalid", "this access token is not valid, was revoked, or has expired", nil)
				return
			}
			scopes := make([]authz.Scope, len(tok.Scopes))
			for i, sc := range tok.Scopes {
				scopes[i] = authz.Scope(sc)
			}
			ctx = authz.WithActor(ctx, authz.Actor{ID: acct.ID, Name: acct.Name, Via: authz.ViaToken, Admin: acct.Admin, Scopes: scopes})
		case cookie != nil:
			se, err := st.Session(cookie.Value)
			if err != nil {
				if !errors.Is(err, accounts.ErrNotFound) {
					log.Printf("session lookup: %v", err)
				}
				break // an unknown or expired session is no session
			}
			if se.Stage != accounts.StageFull {
				ctx = context.WithValue(ctx, pendingKey{}, se)
				break
			}
			acct, err := st.Get(se.AccountID)
			if err != nil {
				break
			}
			ctx = context.WithValue(ctx, sessionKey{}, se)
			ctx = authz.WithActor(ctx, authz.Actor{ID: acct.ID, Name: acct.Name, Via: authz.ViaSession, Admin: acct.Admin, Recent: se.Recent(st.Now())})
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// clientAddr is the address failures are counted against: the connection's peer, or the last entry of the trusted proxy's
// header when one is configured; IPv6 is grouped by /64 so rotating addresses inside one allocation does not reset the count.
// The same rule as the review listener's.
func (s *Server) clientAddr(r *http.Request) string {
	addr := r.RemoteAddr
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}
	if h := s.MultiUser.ClientIPHeader; h != "" {
		if v := r.Header.Get(h); v != "" {
			parts := strings.Split(v, ",")
			addr = strings.TrimSpace(parts[len(parts)-1])
		}
	}
	if ip, err := netip.ParseAddr(addr); err == nil && ip.Is6() && !ip.Is4In6() {
		if pfx, err := ip.Prefix(64); err == nil {
			return pfx.String()
		}
	}
	return addr
}
