package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ernat-soltanbekov/discuss-hub/internal/auth"
)

const MaxFormBytes = 256 << 10

type requestState struct {
	User          *auth.User
	CSRF, Session string
}
type stateKey struct{}

func state(r *http.Request) requestState {
	value, _ := r.Context().Value(stateKey{}).(requestState)
	return value
}

func (a *App) sessionCookie() string {
	if a.secure {
		return "__Host-discuss_session"
	}
	return "discuss_session"
}
func (a *App) csrfCookie() string {
	if a.secure {
		return "__Host-discuss_csrf"
	}
	return "discuss_csrf"
}
func (a *App) signature(random, session string) string {
	mac := hmac.New(sha256.New, a.csrfKey)
	mac.Write([]byte(random + ":" + session))
	return hex.EncodeToString(mac.Sum(nil))
}
func (a *App) validCSRF(token, session string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(parts[0]) != 64 || len(parts[1]) != 64 {
		return false
	}
	if _, err := hex.DecodeString(parts[0]); err != nil {
		return false
	}
	return hmac.Equal([]byte(parts[1]), []byte(a.signature(parts[0], session)))
}
func (a *App) newCSRF(w http.ResponseWriter, session string) (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	random := hex.EncodeToString(bytes)
	token := random + "." + a.signature(random, session)
	a.setCookie(w, a.csrfCookie(), token, time.Now().Add(auth.SessionLifetime), int(auth.SessionLifetime.Seconds()))
	return token, nil
}
func (a *App) setCookie(w http.ResponseWriter, name, value string, expiry time.Time, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Expires: expiry, MaxAge: maxAge, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode})
}

func (a *App) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cache-Control", "no-store")
		if a.secure {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		defer func() {
			if value := recover(); value != nil {
				a.logger.Error("request panic", "path", r.URL.Path, "panic", value)
				http.Error(w, "Internal server error. Please try again.", http.StatusInternalServerError)
			}
		}()
		if strings.HasPrefix(r.URL.Path, "/static/") || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		var s requestState
		if cookie, err := r.Cookie(a.sessionCookie()); err == nil {
			s.Session = cookie.Value
		}
		var err error
		s.User, err = a.auth.User(ctx, s.Session)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if cookie, err := r.Cookie(a.csrfCookie()); err == nil && a.validCSRF(cookie.Value, s.Session) {
			s.CSRF = cookie.Value
		}
		if s.CSRF == "" {
			s.CSRF, err = a.newCSRF(w, s.Session)
			if err != nil {
				a.fail(w, r, err)
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, stateKey{}, s)))
	})
}

func (a *App) form(w http.ResponseWriter, r *http.Request) bool {
	// Origin is checked without trusting forwarding headers. A proxy must keep
	// the public Host; COOKIE_SECURE sets the public HTTPS scheme.
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		scheme := "http"
		if a.secure || r.TLS != nil {
			scheme = "https"
		}
		if err != nil || parsed.Scheme != scheme || parsed.Host != r.Host || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
			a.problem(w, r, http.StatusForbidden, "This form came from another site. Reload this page and try again.")
			return false
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		a.problem(w, r, http.StatusForbidden, "Cross-site requests are not allowed.")
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		a.problem(w, r, http.StatusUnsupportedMediaType, "Submit this action using the form on this site.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxFormBytes)
	if err = r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			a.problem(w, r, http.StatusRequestEntityTooLarge, "The submitted form is too large.")
		} else {
			a.problem(w, r, http.StatusBadRequest, "The submitted form could not be read.")
		}
		return false
	}
	for key, values := range r.PostForm {
		if key != "categories" && len(values) != 1 {
			a.problem(w, r, http.StatusBadRequest, "Submit each field only once.")
			return false
		}
	}
	token := r.PostForm.Get("csrf")
	if !a.validCSRF(token, state(r).Session) || subtle.ConstantTimeCompare([]byte(token), []byte(state(r).CSRF)) != 1 {
		a.problem(w, r, http.StatusForbidden, "Your form has expired. Reload the page and try again.")
		return false
	}
	return true
}

func (a *App) requireUser(w http.ResponseWriter, r *http.Request) bool {
	if state(r).User == nil {
		a.problem(w, r, http.StatusUnauthorized, "Sign in to join the discussion.")
		return false
	}
	return true
}

type limitEntry struct {
	Count int
	Until time.Time
}
type limiter struct {
	sync.Mutex
	entries map[string]limitEntry
}

func newLimiter() *limiter { return &limiter{entries: make(map[string]limitEntry)} }
func (l *limiter) allow(key string, maximum int, window time.Duration) bool {
	l.Lock()
	defer l.Unlock()
	now := time.Now()
	entry, exists := l.entries[key]
	if !exists && len(l.entries) >= 4096 {
		for key, value := range l.entries {
			if !now.Before(value.Until) {
				delete(l.entries, key)
			}
		}
		if len(l.entries) >= 4096 {
			return false
		}
	}
	if !now.Before(entry.Until) {
		entry = limitEntry{Until: now.Add(window)}
	}
	if entry.Count >= maximum {
		return false
	}
	entry.Count++
	l.entries[key] = entry
	return true
}
func (a *App) allowWrite(w http.ResponseWriter, r *http.Request, authentication bool) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	maximum, window, key := a.writeLimit, time.Minute, "write:"+host
	if user := state(r).User; user != nil {
		key = "write:" + user.ID
	}
	if authentication {
		maximum, window, key = a.authLimit, 10*time.Minute, "auth:"+host
	}
	if !a.limiter.allow(key, maximum, window) {
		w.Header().Set("Retry-After", "600")
		a.problem(w, r, http.StatusTooManyRequests, "Too many attempts. Take a short break and try again.")
		return false
	}
	return true
}
