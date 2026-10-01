// Package handlers turns HTTP requests into validated application actions.
package handlers

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	assets "github.com/ernat-soltanbekov/discuss-hub"
	"github.com/ernat-soltanbekov/discuss-hub/internal/auth"
	"github.com/ernat-soltanbekov/discuss-hub/internal/db"
	"github.com/ernat-soltanbekov/discuss-hub/internal/insights"
	"golang.org/x/crypto/bcrypt"
)

type Config struct {
	SecureCookies                     bool
	BcryptCost, AuthLimit, WriteLimit int
	Logger                            *slog.Logger
}
type App struct {
	store                 *db.Store
	auth                  *auth.Service
	templates             *template.Template
	logger                *slog.Logger
	secure                bool
	csrfKey               []byte
	limiter               *limiter
	authLimit, writeLimit int
	authSlots             chan struct{}
}
type pageData struct {
	Title, Active, Error, Notice, CSRF string
	User                               *auth.User
	Categories                         []db.Category
	Posts                              []db.Post
	Post                               db.Post
	Comments                           []db.Comment
	Dashboard                          insights.Dashboard
	Topics                             []insights.Topic
	Filter                             db.Filter
	Form                               url.Values
	Previous, Next                     string
	Status                             int
}

func New(store *db.Store, config Config) (*App, error) {
	if config.BcryptCost == 0 {
		config.BcryptCost = bcrypt.DefaultCost
	}
	if config.AuthLimit == 0 {
		config.AuthLimit = 30
	}
	if config.WriteLimit == 0 {
		config.WriteLimit = 120
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	service, err := auth.New(store, config.BcryptCost)
	if err != nil {
		return nil, err
	}
	functions := template.FuncMap{
		"date": func(seconds int64) string { return time.Unix(seconds, 0).UTC().Format("02 Jan 2006 · 15:04 UTC") },
		"excerpt": func(text string) string {
			runes := []rune(text)
			if len(runes) > 220 {
				return string(runes[:220]) + "…"
			}
			return text
		},
		"initial": func(name string) string { r, _ := utf8.DecodeRuneInString(name); return strings.ToUpper(string(r)) },
		"lower":   strings.ToLower,
		"percent": func(value float64) string { return fmt.Sprintf("%.1f", value) },
		"selected": func(values []string, id int64) bool {
			for _, value := range values {
				if value == strconv.FormatInt(id, 10) {
					return true
				}
			}
			return false
		},
	}
	templates, err := template.New("site").Funcs(functions).ParseFS(assets.Files, "templates/*.html")
	if err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return nil, err
	}
	return &App{store: store, auth: service, templates: templates, logger: config.Logger, secure: config.SecureCookies, csrfKey: key, limiter: newLimiter(), authLimit: config.AuthLimit, writeLimit: config.WriteLimit, authSlots: make(chan struct{}, 4)}, nil
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.index)
	mux.HandleFunc("GET /register", a.registerPage)
	mux.HandleFunc("POST /register", a.register)
	mux.HandleFunc("GET /login", a.loginPage)
	mux.HandleFunc("POST /login", a.login)
	mux.HandleFunc("POST /logout", a.logout)
	mux.HandleFunc("GET /posts/new", a.newPost)
	mux.HandleFunc("POST /posts", a.createPost)
	mux.HandleFunc("GET /posts/{id}", a.post)
	mux.HandleFunc("POST /posts/{id}/comments", a.comment)
	mux.HandleFunc("POST /reactions", a.react)
	mux.HandleFunc("GET /insights", a.insights)
	mux.HandleFunc("GET /insights/trending", a.trending)
	mux.HandleFunc("GET /about", func(w http.ResponseWriter, r *http.Request) {
		a.render(w, r, "about", http.StatusOK, pageData{Title: "The person behind the practice", Active: "about"})
	})
	mux.HandleFunc("GET /healthz", a.health)
	static, _ := fs.Sub(assets.Files, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	// Preserve ServeMux's 405 + Allow response for known paths with wrong methods.
	return a.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler, pattern := mux.Handler(r)
		if pattern == "" {
			// Mux generates a 405 handler even when it returns no pattern.
			probe := r.Clone(r.Context())
			probe.Method = http.MethodGet
			_, getPattern := mux.Handler(probe)
			probe.Method = http.MethodPost
			_, postPattern := mux.Handler(probe)
			if getPattern == "" && postPattern == "" {
				a.problem(w, r, http.StatusNotFound, "That page could not be found.")
				return
			}
			handler.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
}

func (a *App) render(w http.ResponseWriter, r *http.Request, name string, status int, data pageData) {
	s := state(r)
	data.User, data.CSRF = s.User, s.CSRF
	if data.Form == nil {
		data.Form = make(url.Values)
	}
	var buffer bytes.Buffer
	if err := a.templates.ExecuteTemplate(&buffer, name, data); err != nil {
		a.logger.Error("render page", "template", name, "error", err)
		http.Error(w, "Internal server error. Please try again.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(buffer.Bytes())
	}
}
func (a *App) problem(w http.ResponseWriter, r *http.Request, status int, message string) {
	a.render(w, r, "error", status, pageData{Title: http.StatusText(status), Error: message, Status: status})
}
func (a *App) fail(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	a.problem(w, r, http.StatusInternalServerError, "We could not complete this request. Please try again shortly.")
}
func (a *App) dataError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, db.ErrInvalid):
		a.problem(w, r, 400, "Check the submitted fields and categories.")
	case errors.Is(err, db.ErrNotFound):
		a.problem(w, r, 404, "That discussion or comment does not exist.")
	default:
		a.fail(w, r, err)
	}
}
func userID(r *http.Request) string {
	if user := state(r).User; user != nil {
		return user.ID
	}
	return ""
}
func pageNumber(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("page")
	if raw == "" {
		return 1, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 100000 {
		return 0, db.ErrInvalid
	}
	return value, nil
}
func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, db.ErrInvalid
	}
	return id, nil
}
func pagination(r *http.Request, page int, more bool) (string, string) {
	link := func(number int) string {
		query := r.URL.Query()
		query.Set("page", strconv.Itoa(number))
		return r.URL.Path + "?" + query.Encode()
	}
	previous, next := "", ""
	if page > 1 {
		previous = link(page - 1)
	}
	if more {
		next = link(page + 1)
	}
	return previous, next
}
