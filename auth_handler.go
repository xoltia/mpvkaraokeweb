package mpvwebkaraoke

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gorilla/sessions"
	"golang.org/x/oauth2"
)

var userKey = "user"

type User struct {
	ID     string `json:"id"`
	Avatar string `json:"avatar"`
	Name   string `json:"username"`
	Admin  bool   `json:"-"`
}

type guildMember struct {
	User        User    `json:"user"`
	Nick        *string `json:"nick"`
	Permissions int     `json:"permissions"`
}

type AuthHandler struct {
	store   sessions.Store
	conf    *oauth2.Config
	guildID string
}

func NewAuthHandler(store sessions.Store, config *oauth2.Config, guildID string) *AuthHandler {
	return &AuthHandler{store: store, conf: config, guildID: guildID}
}

func (h *AuthHandler) HandleIndex(w http.ResponseWriter, r *http.Request) {
	var state string

	b := make([]byte, 16)
	rand.Read(b)
	state = hex.EncodeToString(b)

	session, _ := h.store.Get(r, "auth")
	session.Values["state"] = state
	session.Save(r, w)

	url := h.conf.AuthCodeURL(state)
	w.Header().Set("HX-Redirect", url)
	http.Redirect(w, r, url, http.StatusFound)
}

func (h *AuthHandler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	session, _ := h.store.Get(r, "auth")
	state := session.Values["state"].(string)

	if r.FormValue("state") != state {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}

	token, err := h.conf.Exchange(r.Context(), r.FormValue("code"))
	if err != nil {
		http.Error(w, "failed to exchange token", http.StatusInternalServerError)
		return
	}

	client := h.conf.Client(r.Context(), token)
	resp, err := client.Get("https://discord.com/api/users/@me/guilds/" + h.guildID + "/member")

	if resp.StatusCode != http.StatusOK {
		http.Error(w, "failed to get user info", http.StatusInternalServerError)
		return
	}

	if err != nil {
		http.Error(w, "failed to get user info", http.StatusInternalServerError)
		return
	}

	var member guildMember
	if err := json.NewDecoder(resp.Body).Decode(&member); err != nil {
		http.Error(w, "failed to decode user info", http.StatusInternalServerError)
		return
	}

	fmt.Println(member)

	member.User.Admin = member.Permissions&8 == 8
	if member.Nick != nil {
		member.User.Name = *member.Nick
	}

	session.Values["user"] = member.User
	err = session.Save(r, w)
	if err != nil {
		println(err.Error())
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *AuthHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	session, _ := h.store.Get(r, "auth")
	delete(session.Values, "user")
	session.Save(r, w)

	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusSeeOther)
}

func (h *AuthHandler) Wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, _ := h.store.Get(r, "auth")
		user, ok := session.Values["user"].(User)
		if !ok {
			w.Header().Set("HX-Redirect", "/auth")
			http.Redirect(w, r, "/auth", http.StatusSeeOther)
			return
		}

		ctx := r.Context()
		ctx = context.WithValue(ctx, userKey, user)
		r = r.WithContext(ctx)
		next(w, r)
	}
}
