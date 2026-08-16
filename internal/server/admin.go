package server

import (
	"database/sql"
	"errors"
	"net/http"
	"os"

	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

// Admin notice keys. As on the login page, a key travels in the URL and the
// message is chosen server-side, so nothing a caller supplies is reflected
// into the page.
const (
	adminNoticeApproved    = "approved"
	adminNoticeDisabled    = "disabled"
	adminNoticeDeleted     = "deleted"
	adminNoticeDeletedPart = "deleted-partial"
	adminNoticeGone        = "gone"
	adminNoticeLastAdmin   = "last-admin"
	adminNoticeSelf        = "self"
	adminNoticeConfigAdmin = "config-admin"
)

// adminNoticeFor maps a key to its message; an unknown key yields none.
func adminNoticeFor(key string) string {
	switch key {
	case adminNoticeApproved:
		return "Account approved."
	case adminNoticeDisabled:
		return "Account disabled and signed out."
	case adminNoticeDeleted:
		return "Account deleted, along with its songs."
	case adminNoticeDeletedPart:
		return "Account deleted; some audio files could not be removed."
	case adminNoticeGone:
		return "That account no longer exists."
	case adminNoticeLastAdmin:
		return "That is the only administrator account left — promote another before removing it."
	case adminNoticeSelf:
		return "You cannot do that to your own account."
	case adminNoticeConfigAdmin:
		return "The configured administrator is not a database account and cannot be changed here."
	}
	return ""
}

func (s *Server) registerAdmin(rt *router) {
	rt.handleFunc("GET /admin", s.handleAdminDashboard)
	rt.handleFunc("POST /admin/users/{id}/approve", s.handleApproveUser)
	rt.handleFunc("POST /admin/users/{id}/disable", s.handleDisableUser)
	rt.handleFunc("POST /admin/users/{id}/delete", s.handleDeleteUser)
}

// requireAdmin re-derives administrator status inside the handler.
//
// The /admin prefix is already gated by the access middleware, and that is the
// wall that matters. This is the second one: these handlers delete accounts and
// audio, so they do not assume the routing that reaches them. If a future
// change registers one of them outside the /admin prefix, or the prefix
// classification is edited, the handler still refuses.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) (*UserContext, bool) {
	uc, ok := userFrom(r.Context())
	if !ok || uc == nil || !uc.IsAdmin {
		s.log.Warn("admin handler reached without administrator privilege",
			"path", r.URL.Path)
		s.deny(w, r, http.StatusForbidden, "forbidden",
			"Administrator access is required.", "")
		return nil, false
	}
	return uc, true
}

// handleAdminDashboard lists every account, pending requests first.
func (s *Server) handleAdminDashboard(w http.ResponseWriter, r *http.Request) {
	uc, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	users, err := s.st.ListUsers()
	if err != nil {
		s.log.Error("admin dashboard", "err", err)
		http.Error(w, "Could not load the user list.", http.StatusInternalServerError)
		return
	}
	var pending, rest []*store.User
	for _, u := range users {
		if u.Status == store.StatusPending {
			pending = append(pending, u)
		} else {
			rest = append(rest, u)
		}
	}
	s.execute(w, "admin.html", s.pageData(r, map[string]any{
		"Page": "admin", "Pending": pending, "Users": rest,
		"Notice": adminNoticeFor(r.URL.Query().Get("notice")),
		// Self is how the template hides the controls an admin must not aim
		// at their own account. The handlers refuse regardless.
		"Self": uc.UserID,
	}))
}

// adminAction is the shared shape of approve, disable, and delete: identify
// the target, refuse the self-inflicted cases, act, and report.
//
// act returns a notice key for the redirect on success.
func (s *Server) adminAction(w http.ResponseWriter, r *http.Request, what string,
	allowSelf bool, act func(id string) (string, error)) {

	uc, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "No user specified.", http.StatusBadRequest)
		return
	}

	// The static administrator has no users row, so every action on it is a
	// no-op. Checked before the self guard because for the config admin both
	// apply and this is the true reason. Nothing is written either way, and it
	// never falls through to a bare "not found" that would read like a bug.
	if id == ConfigAdminUserID {
		s.adminRedirect(w, r, adminNoticeConfigAdmin)
		return
	}
	// An administrator must not be able to lock themselves — and possibly
	// everyone — out with one click.
	if !allowSelf && id == uc.UserID {
		s.adminRedirect(w, r, adminNoticeSelf)
		return
	}

	notice, err := act(id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		s.adminRedirect(w, r, adminNoticeGone)
		return
	case errors.Is(err, store.ErrLastAdmin):
		s.adminRedirect(w, r, adminNoticeLastAdmin)
		return
	case err != nil:
		s.log.Error("admin action", "what", what, "id", id, "err", err)
		http.Error(w, "Could not complete that action.", http.StatusInternalServerError)
		return
	}
	s.log.Info("admin action", "what", what, "target", id, "by", uc.Username)
	s.adminRedirect(w, r, notice)
}

// adminRedirect returns to the dashboard carrying a notice key. htmx callers
// get a redirect header so the whole page refreshes with the new state.
func (s *Server) adminRedirect(w http.ResponseWriter, r *http.Request, noticeKey string) {
	dest := "/admin"
	if noticeKey != "" {
		dest += "?notice=" + urlQueryEscape(noticeKey)
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", dest)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// handleApproveUser moves an account to approved. Approving an already
// approved account is a no-op that succeeds — the endpoint sets a state, it
// does not toggle one.
func (s *Server) handleApproveUser(w http.ResponseWriter, r *http.Request) {
	s.adminAction(w, r, "approve", true, func(id string) (string, error) {
		if err := s.st.UpdateUserStatus(id, store.StatusApproved); err != nil {
			return "", err
		}
		return adminNoticeApproved, nil
	})
}

// handleDisableUser moves an account to disabled. UpdateUserStatus revokes the
// account's sessions in the same transaction, so the user is signed out
// immediately rather than at cookie expiry — and Stage 01's live status join
// would refuse them on their next request even if a session survived.
func (s *Server) handleDisableUser(w http.ResponseWriter, r *http.Request) {
	s.adminAction(w, r, "disable", false, func(id string) (string, error) {
		if err := s.st.UpdateUserStatus(id, store.StatusDisabled); err != nil {
			return "", err
		}
		return adminNoticeDisabled, nil
	})
}

// handleDeleteUser removes an account and everything belonging to it.
//
// The database transaction commits first and the audio files are unlinked
// afterwards: an unlink cannot join the transaction, and a leftover file is
// recoverable garbage where a row pointing at a missing file is not.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	s.adminAction(w, r, "delete", false, func(id string) (string, error) {
		paths, err := s.st.DeleteUser(id)
		if err != nil {
			return "", err
		}
		var failed int
		for _, p := range paths {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				s.log.Warn("removing audio for a deleted user", "path", p, "err", err)
				failed++
			}
		}
		if failed > 0 {
			return adminNoticeDeletedPart, nil
		}
		return adminNoticeDeleted, nil
	})
}
