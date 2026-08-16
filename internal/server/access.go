package server

import (
	"context"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

// accessLevel is what a route demands.
//
// The zero value is accessAuth deliberately. Classification is a lookup that
// misses for anything unlisted, so a route nobody thought about demands a
// session rather than being published — forgetting to classify a new route
// makes it unreachable, never public.
type accessLevel int

const (
	accessAuth   accessLevel = iota // valid, approved session — the default
	accessPublic                    // no session; must be listed explicitly
	accessAdmin                     // approved session with admin privilege
)

// publicPatterns is the complete set of routes reachable without a session.
//
// Routes() wraps the entire mux, so this list — not the registration of a
// handler — is what makes something public. Adding a route without touching
// this list yields a protected route.
//
// TestPublicAllowlistIsExactlyThis keeps an independent copy of this set, so
// widening it fails a test until a human edits the test too.
var publicPatterns = []string{
	"GET /healthz",
	"GET /login",
	"POST /login",
	"GET /register",
	"POST /register",
	// Logout is public on purpose: it only revokes the token the caller
	// presents. Requiring an approved session to log out would strand a user
	// who was disabled mid-session with a cookie they cannot clear, and it
	// buys nothing — there is no state to protect. SameSite=Lax already
	// blocks the cross-site POST.
	"POST /logout",
	"GET /static/",
	"GET /favicon.ico",
}

// adminPatterns demand an administrator. The prefix is registered even though
// Stage 06 owns the handlers, so those routes are admin-only from the moment
// they are added rather than from the moment someone remembers to protect them.
var adminPatterns = []string{
	"GET /admin",
	"/admin/",
}

// levelHandler carries an accessLevel through a ServeMux. The mux is used only
// as a pattern matcher — these handlers are never served.
type levelHandler accessLevel

func (levelHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}

// newClassifier builds the route→level matcher. It is a ServeMux so it shares
// exact pattern-matching semantics with the mux that actually dispatches;
// there is no second, subtly different path-matching implementation to drift.
func newClassifier() *http.ServeMux {
	m := http.NewServeMux()
	for _, p := range publicPatterns {
		m.Handle(p, levelHandler(accessPublic))
	}
	for _, p := range adminPatterns {
		m.Handle(p, levelHandler(accessAdmin))
	}
	return m
}

// levelFor classifies a request. Anything the classifier does not match —
// including a method mismatch or a path-cleaning redirect — falls through to
// accessAuth, which is the fail-closed answer.
func (s *Server) levelFor(r *http.Request) accessLevel {
	h, _ := s.classifier.Handler(r)
	if lh, ok := h.(levelHandler); ok {
		return accessLevel(lh)
	}
	return accessAuth
}

// UserContext is the authenticated caller, injected into every request that
// clears the middleware. PendingCount is populated for administrators only —
// it drives the approval badge and is meaningless to anyone else.
type UserContext struct {
	UserID       string
	Username     string
	IsAdmin      bool
	ConfigAdmin  bool
	Status       string
	PendingCount int
}

type userCtxKey struct{}

func withUser(ctx context.Context, uc *UserContext) context.Context {
	return context.WithValue(ctx, userCtxKey{}, uc)
}

// userFrom returns the caller for a request that cleared the middleware.
func userFrom(ctx context.Context) (*UserContext, bool) {
	uc, ok := ctx.Value(userCtxKey{}).(*UserContext)
	return uc, ok && uc != nil
}

// protect is the whole access-control surface: every request passes through
// it, and only an explicitly public pattern skips authentication.
func (s *Server) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.levelFor(r) == accessPublic {
			next.ServeHTTP(w, r)
			return
		}
		uc, ok := s.authenticate(w, r)
		if !ok {
			return // authenticate has already written the denial
		}
		if s.levelFor(r) == accessAdmin && !uc.IsAdmin {
			s.deny(w, r, http.StatusForbidden, "forbidden",
				"Administrator access is required.", "")
			return
		}
		next.ServeHTTP(w, r.WithContext(withUser(r.Context(), uc)))
	})
}

// authenticate resolves the session or writes a denial and returns false.
//
// Every branch that is not a fully approved session denies. There is no path
// that continues with a nil user "as anonymous" — including the error path,
// where an unreadable session is treated as no session at all rather than as
// permission to proceed.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (*UserContext, bool) {
	token := sessionToken(r)
	if token == "" {
		s.denySignIn(w, r, noticeKeySignIn)
		return nil, false
	}
	sess, err := s.st.GetSession(token)
	if err != nil {
		s.log.Error("session lookup", "err", err)
		s.deny(w, r, http.StatusInternalServerError, "internal",
			"Could not verify your session.", "")
		return nil, false
	}
	if sess == nil {
		// Unknown, expired, or orphaned. Drop the dead cookie so the browser
		// stops presenting it.
		s.clearSessionCookie(w, r)
		s.denySignIn(w, r, noticeKeySignIn)
		return nil, false
	}

	// Status comes from the store's live join, never from the session row, so
	// a user disabled mid-session is denied on their very next request.
	if sess.Status != store.StatusApproved {
		key := noticeKeyPending
		if sess.Status == store.StatusDisabled {
			key = noticeKeyDisabled
		}
		s.deny(w, r, http.StatusForbidden, "account-not-approved",
			"Your account is not approved.", s.loginURL(r, key))
		return nil, false
	}

	uc := &UserContext{
		UserID: sess.UserID, Username: sess.Username,
		IsAdmin: sess.IsAdmin, ConfigAdmin: sess.ConfigAdmin, Status: sess.Status,
	}
	if uc.IsAdmin {
		n, err := s.st.CountPendingUsers()
		if err != nil {
			s.log.Error("pending count", "err", err) // badge only; not fatal
		}
		uc.PendingCount = n
	}
	return uc, true
}

func (s *Server) denySignIn(w http.ResponseWriter, r *http.Request, noticeKey string) {
	s.deny(w, r, http.StatusUnauthorized, "unauthenticated",
		"Sign in to continue.", s.loginURL(r, noticeKey))
}

// deny writes a refusal in the shape the caller can use: structured JSON for
// API clients, an HX-Redirect for htmx so the browser navigates instead of
// swapping a login page into a fragment, and an ordinary redirect otherwise.
func (s *Server) deny(w http.ResponseWriter, r *http.Request, code int, errCode, msg, redirect string) {
	switch {
	case wantsJSON(r):
		writeJSON(w, code, map[string]string{"error": errCode, "message": msg})
	case isHTMX(r):
		if redirect != "" {
			w.Header().Set("HX-Redirect", redirect)
		}
		w.WriteHeader(code)
	case redirect != "" && r.Method == http.MethodGet:
		http.Redirect(w, r, redirect, http.StatusSeeOther)
	default:
		// A non-GET browser request has nowhere useful to land; say so.
		http.Error(w, msg, code)
	}
}

// wantsJSON reports whether the caller is an API client rather than a browser
// following links.
func wantsJSON(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/") ||
		strings.Contains(r.Header.Get("Accept"), "application/json") ||
		strings.Contains(r.Header.Get("Content-Type"), "application/json")
}

func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// loginURL builds the sign-in destination, carrying a sanitised return path.
func (s *Server) loginURL(r *http.Request, noticeKey string) string {
	q := url.Values{}
	if noticeKey != "" {
		q.Set("notice", noticeKey)
	}
	if r.Method == http.MethodGet {
		if next := safeNext(r.URL.RequestURI()); next != "" {
			q.Set("next", next)
		}
	}
	if len(q) == 0 {
		return "/login"
	}
	return "/login?" + q.Encode()
}

// safeNext sanitises a post-login redirect target.
//
// A login redirect is a phishing primitive, so only a local path is accepted
// and every form of "somewhere else" is dropped: absolute URLs, scheme-relative
// "//evil.com", the backslash variants browsers normalise into it, anything
// carrying a scheme, host or userinfo, and percent-encodings that decode into
// one of those. Returns "" when the input cannot be trusted, and the caller
// falls back to "/".
func safeNext(raw string) string {
	if raw == "" || len(raw) > 512 {
		return ""
	}
	// Control characters are stripped or mangled by browsers, which would then
	// act on a different target than the one validated here.
	for _, c := range raw {
		if c < 0x20 || c == 0x7f {
			return ""
		}
	}
	if !strings.HasPrefix(raw, "/") {
		return "" // absolute URL, scheme, or a relative path
	}
	// Browsers treat a backslash as a slash, so "/\evil.com" is "//evil.com".
	if strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, `/\`) {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Scheme != "" || u.Host != "" || u.Opaque != "" || u.User != nil {
		return ""
	}
	// Re-check after decoding: "/%2f%2fevil.com" decodes to "//evil.com".
	if !strings.HasPrefix(u.Path, "/") ||
		strings.HasPrefix(u.Path, "//") || strings.HasPrefix(u.Path, `/\`) {
		return ""
	}
	// No legitimate path in this app contains an empty segment or a dot
	// segment, and both are the raw material of normalisation tricks such as
	// "/..//evil.com". Refusing them outright is cheaper than reasoning about
	// how each client resolves them.
	if strings.Contains(u.Path, "//") || strings.Contains(u.Path, "..") {
		return ""
	}
	if c := path.Clean(u.Path); !strings.HasPrefix(c, "/") || strings.HasPrefix(c, "//") {
		return ""
	}
	// Bouncing back to an auth page just loops.
	switch u.Path {
	case "/login", "/register", "/logout":
		return ""
	}
	out := u.EscapedPath()
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

// caller is the ownership scope for the current request — the seam Stage 01
// left and Stage 03 fills. Every scoped store call in the server goes through
// it, so this one function decides what the whole request may touch.
//
// The zero Access owns nothing, so a request that somehow reaches a handler
// without a user reads and deletes nothing instead of everything.
func (s *Server) caller(r *http.Request) store.Access {
	uc, ok := userFrom(r.Context())
	if !ok {
		return store.Access{}
	}
	if uc.IsAdmin {
		return store.AdminAccess(uc.UserID)
	}
	return store.UserAccess(uc.UserID)
}

// readableSong returns a song the caller is allowed to see: their own, any
// song if they are an administrator, or one explicitly shared as public.
func (s *Server) readableSong(r *http.Request, id string) (*store.Song, error) {
	g, err := s.st.Song(id, s.caller(r))
	if err != nil || g != nil {
		return g, err
	}
	return s.st.PublicSong(id)
}

// router registers handlers while recording every pattern, so the
// access-control test enumerates the real route table instead of a
// hand-maintained copy that silently drifts out of date.
type router struct {
	mux      *http.ServeMux
	patterns []string
}

func (rt *router) handleFunc(pattern string, h http.HandlerFunc) {
	rt.patterns = append(rt.patterns, pattern)
	rt.mux.HandleFunc(pattern, h)
}

func (rt *router) handle(pattern string, h http.Handler) {
	rt.patterns = append(rt.patterns, pattern)
	rt.mux.Handle(pattern, h)
}
