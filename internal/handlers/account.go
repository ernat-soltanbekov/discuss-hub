package handlers

import (
	"errors"
	"github.com/ernat-soltanbekov/discuss-hub/internal/auth"
	"net/http"
	"net/url"
	"time"
)

func (a *App) registerPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "register", 200, pageData{Title: "Join the conversation"})
}
func (a *App) loginPage(w http.ResponseWriter, r *http.Request) {
	data := pageData{Title: "Welcome back"}
	if r.URL.Query().Get("registered") == "1" {
		data.Notice = "Your account is ready. Sign in to get started."
	}
	a.render(w, r, "login", 200, data)
}
func (a *App) authenticationSlot(w http.ResponseWriter, r *http.Request) bool {
	select {
	case a.authSlots <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "2")
		a.problem(w, r, 503, "Sign-in is busy. Please try again in a moment.")
		return false
	}
}
func (a *App) register(w http.ResponseWriter, r *http.Request) {
	if !a.form(w, r) || !a.allowWrite(w, r, true) || !a.authenticationSlot(w, r) {
		return
	}
	defer func() { <-a.authSlots }()
	_, err := a.auth.Register(r.Context(), r.PostForm.Get("email"), r.PostForm.Get("username"), r.PostForm.Get("password"))
	if err != nil {
		status := 400
		if errors.Is(err, auth.ErrEmailTaken) || errors.Is(err, auth.ErrUsernameTaken) {
			status = 409
		} else if !errors.Is(err, auth.ErrInvalid) {
			a.fail(w, r, err)
			return
		}
		r.PostForm.Del("password")
		a.render(w, r, "register", status, pageData{Title: "Join the conversation", Error: err.Error(), Form: r.PostForm})
		return
	}
	http.Redirect(w, r, "/login?registered=1", http.StatusSeeOther)
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if !a.form(w, r) || !a.allowWrite(w, r, true) || !a.authenticationSlot(w, r) {
		return
	}
	defer func() { <-a.authSlots }()
	token, expiry, err := a.auth.Login(r.Context(), r.PostForm.Get("email"), r.PostForm.Get("password"))
	if err != nil {
		if !errors.Is(err, auth.ErrCredentials) {
			a.fail(w, r, err)
			return
		}
		a.render(w, r, "login", 401, pageData{Title: "Welcome back", Error: err.Error(), Form: url.Values{"email": {r.PostForm.Get("email")}}})
		return
	}
	a.setCookie(w, a.sessionCookie(), token, expiry, int(auth.SessionLifetime.Seconds()))
	if _, err = a.newCSRF(w, token); err != nil {
		a.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if !a.form(w, r) {
		return
	}
	if err := a.auth.Logout(r.Context(), state(r).Session); err != nil {
		a.fail(w, r, err)
		return
	}
	a.setCookie(w, a.sessionCookie(), "", time.Unix(1, 0), -1)
	if _, err := a.newCSRF(w, ""); err != nil {
		a.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
