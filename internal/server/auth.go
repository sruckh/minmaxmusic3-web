package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

// newUserID returns a 32-character lowercase hex id. The alphabet matters:
// ConfigAdminUserID relies on hex being unable to produce a colon.
func newUserID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

const (
	// sessionCookie is the only place a raw session token ever lives on the
	// client. The store keeps a SHA-256 of it and nothing more.
	sessionCookie = "mm3_session"
	sessionTTL    = 7 * 24 * time.Hour

	minUsernameLen = 3
	maxUsernameLen = 64
	minPasswordLen = 8
	// maxPasswordLen is bcrypt's hard limit — it silently ignores bytes past
	// 72, so a longer password must be rejected rather than truncated.
	maxPasswordLen = 72
)

// ConfigAdminUserID is the user_id carried by the static administrator's
// session. The administrator has no users row, so this value must never
// collide with a real one: generated ids are 32 lowercase hex characters, and
// a colon cannot appear in hex. TestConfigAdminIDCannotCollide pins that.
const ConfigAdminUserID = "config:admin"

// Login throttling. A password endpoint without a limit is brute-forceable no
// matter how good the hashing is.
const (
	loginLimitPerWindow    = 10
	loginWindow            = 15 * time.Minute
	registerLimitPerWindow = 5
	registerWindow         = time.Hour
)

// Notices rendered on the login page. They are keys, not free text from the
// query string, so nothing a caller supplies is ever reflected into the page.
const (
	noticeInvalid    = "Incorrect username or password."
	noticePending    = "Your account is awaiting administrator approval."
	noticeDisabled   = "This account has been disabled. Contact an administrator."
	noticeRegistered = "Registration received. An administrator must approve your account before you can sign in."
	noticeSignedOut  = "You have been signed out."
)

// invalidCredentials is the single answer to every failed credential check:
// unknown username and wrong password are indistinguishable in status, body,
// and — thanks to the decoy comparison below — in time.
const invalidCredentials = noticeInvalid

func (s *Server) registerAuth(mux *http.ServeMux) {
	s.loginLimiter = newLimiter(loginLimitPerWindow, loginWindow)
	s.registerLimiter = newLimiter(registerLimitPerWindow, registerWindow)

	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("GET /register", s.handleRegisterForm)
	mux.HandleFunc("POST /register", s.handleRegister)
	mux.HandleFunc("POST /logout", s.handleLogout)
}

// ProductionBcryptCost is the work factor this application ships with; the
// conventions require at least 12.
const ProductionBcryptCost = 12

// bcryptCost is the work factor for every password hashed or verified here,
// including the decoy comparison. Raising it is safe — existing hashes carry
// their own cost and keep verifying. It is a var solely so tests can lower it
// to keep the suite fast; nothing outside a test may change it.
var bcryptCost = ProductionBcryptCost

// hashPassword returns a bcrypt hash at bcryptCost.
func hashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// checkPassword reports whether password matches hash. Any error — malformed
// hash included — is a failure, never a pass.
func checkPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// The decoy is a real bcrypt hash at the current cost, built once per cost.
// It exists so the unknown-user path can spend the same time as the
// known-user path instead of returning immediately.
var (
	decoyMu   sync.Mutex
	decoyHash []byte
	decoyCost int
)

// burnPasswordCheck runs one bcrypt comparison that is certain to fail. Every
// login path performs exactly one comparison at bcryptCost, so response time
// does not reveal whether the username exists or which branch was taken.
func burnPasswordCheck() {
	decoyMu.Lock()
	if decoyHash == nil || decoyCost != bcryptCost {
		h, err := bcrypt.GenerateFromPassword([]byte("decoy-password-for-timing"), bcryptCost)
		if err != nil {
			decoyMu.Unlock()
			return
		}
		decoyHash, decoyCost = h, bcryptCost
	}
	h := decoyHash
	decoyMu.Unlock()
	_ = bcrypt.CompareHashAndPassword(h, []byte("not-the-password"))
}

// isTLS reports whether the client's connection is HTTPS.
//
// Behind the reverse proxy r.TLS is nil even for HTTPS, so X-Forwarded-Proto
// is consulted. Unlike the client IP in ratelimit.go, trusting this header is
// safe: a forged value can only *add* the Secure attribute. It cannot remove
// one, so the worst an attacker achieves by lying is that their own cookie
// stops being sent over plain HTTP.
func isTLS(r *http.Request) bool {
	return r.TLS != nil ||
		strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isTLS(r),
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isTLS(r),
		MaxAge:   -1,
	})
}

// sessionToken returns the raw token the client presented, if any.
func sessionToken(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

func (s *Server) renderLogin(w http.ResponseWriter, code int, notice, username string) {
	s.executeStatus(w, code, "login.html", map[string]any{
		"Page": "login", "Notice": notice, "Username": username,
	})
}

func (s *Server) renderRegister(w http.ResponseWriter, code int, notice, username string) {
	s.executeStatus(w, code, "register.html", map[string]any{
		"Page": "register", "Notice": notice, "Username": username,
	})
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	var notice string
	switch r.URL.Query().Get("notice") {
	case "registered":
		notice = noticeRegistered
	case "signed-out":
		notice = noticeSignedOut
	}
	s.renderLogin(w, http.StatusOK, notice, "")
}

func (s *Server) handleRegisterForm(w http.ResponseWriter, r *http.Request) {
	s.renderRegister(w, http.StatusOK, "", "")
}

// handleRegister validates a signup and stores the account as pending. It
// never signs the new user in — approval is an administrator's decision.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.authAllowed(w, r, s.registerLimiter, "register") {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderRegister(w, http.StatusBadRequest, "Could not read that form.", "")
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	confirm := r.FormValue("confirm_password")

	if msg := validateCredentials(username, password); msg != "" {
		s.renderRegister(w, http.StatusBadRequest, msg, username)
		return
	}
	if password != confirm {
		s.renderRegister(w, http.StatusBadRequest, "The two passwords do not match.", username)
		return
	}
	// A signup must not shadow the static administrator: the config path wins
	// at login, so such an account could never be used and the collision
	// would only ever confuse.
	if s.cfg.AdminUser != "" && strings.EqualFold(username, s.cfg.AdminUser) {
		s.renderRegister(w, http.StatusConflict, "That username is not available.", "")
		return
	}

	hash, err := hashPassword(password)
	if err != nil {
		s.log.Error("hashing password", "err", err) // never the password itself
		s.renderRegister(w, http.StatusInternalServerError, "Could not create the account.", username)
		return
	}
	err = s.st.CreateUser(&store.User{
		ID: newUserID(), Username: username, PasswordHash: hash,
		Status: store.StatusPending, Role: store.RoleUser,
	})
	if errors.Is(err, store.ErrUsernameTaken) {
		s.renderRegister(w, http.StatusConflict, "That username is already taken.", "")
		return
	}
	if err != nil {
		s.log.Error("creating user", "err", err)
		s.renderRegister(w, http.StatusInternalServerError, "Could not create the account.", username)
		return
	}
	s.log.Info("user registered", "username", username, "status", store.StatusPending)
	http.Redirect(w, r, "/login?notice=registered", http.StatusSeeOther)
}

// validateCredentials returns "" when the pair is acceptable, else the message
// to show. Length limits are enforced here so bcrypt never silently truncates.
func validateCredentials(username, password string) string {
	switch {
	case len([]rune(username)) < minUsernameLen:
		return "Choose a username of at least 3 characters."
	case len([]rune(username)) > maxUsernameLen:
		return "That username is too long."
	case strings.ContainsAny(username, " \t\r\n"):
		return "Usernames cannot contain spaces."
	case len(password) < minPasswordLen:
		return "Choose a password of at least 8 characters."
	case len(password) > maxPasswordLen:
		return "Passwords must be 72 characters or fewer."
	}
	return ""
}

// handleLogin verifies credentials and starts a session.
//
// Two properties are load-bearing and easy to lose in a refactor:
//
//   - Every path performs exactly one bcrypt-cost comparison, so a wrong
//     password, an unknown username, and a wrong administrator password are
//     indistinguishable in time as well as in response.
//   - Status is checked only *after* the password verifies. The pending and
//     disabled notices confirm an account exists, so they must never be
//     reachable by someone who does not already hold the password.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.authAllowed(w, r, s.loginLimiter, "login") {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderLogin(w, http.StatusBadRequest, invalidCredentials, "")
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	if username == "" || password == "" || len(password) > maxPasswordLen {
		burnPasswordCheck()
		s.renderLogin(w, http.StatusUnauthorized, invalidCredentials, username)
		return
	}

	adminAttempt := s.cfg.AdminLoginEnabled() &&
		subtle.ConstantTimeCompare([]byte(username), []byte(s.cfg.AdminUser)) == 1

	var u *store.User
	if !adminAttempt {
		var err error
		if u, err = s.st.GetUserByUsername(username); err != nil {
			s.log.Error("login lookup", "err", err)
			s.renderLogin(w, http.StatusInternalServerError,
				"Could not sign you in — try again.", username)
			return
		}
	}

	var ok bool
	switch {
	case adminAttempt:
		ok = subtle.ConstantTimeCompare([]byte(password), []byte(s.cfg.AdminPassword)) == 1
		// The constant-time compare above is far cheaper than bcrypt; without
		// this the administrator's username would stand out by response time.
		burnPasswordCheck()
	case u != nil:
		ok = checkPassword(u.PasswordHash, password)
	default:
		burnPasswordCheck()
	}
	if !ok {
		s.log.Warn("login failed", "ip", clientIP(r)) // never the credentials
		s.renderLogin(w, http.StatusUnauthorized, invalidCredentials, username)
		return
	}

	// Password is correct from here on, so status may safely be disclosed.
	userID, displayName := ConfigAdminUserID, username
	if !adminAttempt {
		switch u.Status {
		case store.StatusApproved:
		case store.StatusPending:
			s.renderLogin(w, http.StatusForbidden, noticePending, username)
			return
		case store.StatusDisabled:
			s.renderLogin(w, http.StatusForbidden, noticeDisabled, username)
			return
		default:
			s.log.Error("unknown user status", "status", u.Status)
			s.renderLogin(w, http.StatusForbidden, noticeDisabled, username)
			return
		}
		userID, displayName = u.ID, u.Username
	}

	if err := s.startSession(w, r, userID, displayName, adminAttempt); err != nil {
		s.log.Error("starting session", "err", err)
		s.renderLogin(w, http.StatusInternalServerError,
			"Could not sign you in — try again.", username)
		return
	}
	s.log.Info("login", "username", displayName, "config_admin", adminAttempt)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// startSession issues a brand-new session and destroys whatever the client
// arrived with.
//
// The token is always freshly generated and never taken from the request, and
// any pre-existing session is revoked server-side rather than merely
// overwritten in the browser. Together those close session fixation: a token
// planted before login is dead, not promoted.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID, username string, configAdmin bool) error {
	if old := sessionToken(r); old != "" {
		if err := s.st.DeleteSession(old); err != nil {
			return err
		}
	}
	token, err := store.NewSessionToken()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	sess := &store.Session{
		UserID: userID, Username: username, ConfigAdmin: configAdmin,
		CreatedAt: now, ExpiresAt: now.Add(sessionTTL),
	}
	if err := s.st.CreateSession(token, sess); err != nil {
		return err
	}
	s.setSessionCookie(w, r, token, sess.ExpiresAt)
	return nil
}

// handleLogout revokes the session server-side and clears the cookie. Dropping
// only the cookie would leave a token that still authenticates if it leaked.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token := sessionToken(r); token != "" {
		if err := s.st.DeleteSession(token); err != nil {
			s.log.Error("revoking session", "err", err)
		}
	}
	s.clearSessionCookie(w, r)
	http.Redirect(w, r, "/login?notice=signed-out", http.StatusSeeOther)
}

// authAllowed throttles a credential endpoint per IP. It answers with the
// login page rather than a bare error so the browser flow stays intact.
func (s *Server) authAllowed(w http.ResponseWriter, r *http.Request, l *limiter, what string) bool {
	if l.allow(clientIP(r)) {
		return true
	}
	s.log.Warn("rate limited", "what", what, "ip", clientIP(r))
	w.Header().Set("Retry-After", "900")
	s.renderLogin(w, http.StatusTooManyRequests,
		"Too many attempts. Wait a few minutes and try again.", "")
	return false
}
