package mpvwebkaraoke

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

type cookieKey string

const (
	sessionKey cookieKey = "session"
)

type AuthHandler struct {
	sessions  *SessionStore
	adminCode string
}

func NewAuthHandler(sessions *SessionStore, adminCode string) *AuthHandler {
	return &AuthHandler{sessions: sessions, adminCode: adminCode}
}

func (h *AuthHandler) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session")
		if err != nil {
			http.Redirect(w, r, "/register", http.StatusSeeOther)
			return
		}

		session, err := h.sessions.Get(cookie.Value)

		if errors.Is(err, sql.ErrNoRows) {
			http.Redirect(w, r, "/register", http.StatusSeeOther)
			return
		}

		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		ctx := r.Context()
		ctx = context.WithValue(ctx, sessionKey, session)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *AuthHandler) HandleIndex(w http.ResponseWriter, r *http.Request) {
	registerPage().Render(r.Context(), w)
}

func (h *AuthHandler) HandleRegister(w http.ResponseWriter, r *http.Request) {
	user := r.FormValue("username")
	pass := r.FormValue("password")
	if user == "" {
		http.Error(w, "username required", http.StatusBadRequest)
		return
	}

	admin := false

	if pass != "" {
		if pass == h.adminCode {
			admin = true
		} else {
			http.Error(w, "invalid password", http.StatusUnauthorized)
			return
		}
	}

	sessionID, err := h.sessions.Create(user, admin)

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:  "session",
		Value: sessionID,
		Path:  "/",
	})

	http.Redirect(w, r, "/", http.StatusSeeOther)
}
