package handlers

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ernat-soltanbekov/discuss-hub/internal/db"
	"github.com/ernat-soltanbekov/discuss-hub/internal/testutil"
	"golang.org/x/crypto/bcrypt"
)

type browser struct {
	t       *testing.T
	handler http.Handler
	cookies map[string]*http.Cookie
	ip      string
}

func newBrowser(t *testing.T, handler http.Handler) *browser {
	b := &browser{t: t, handler: handler, cookies: map[string]*http.Cookie{}, ip: "192.0.2.1:1234"}
	b.request("GET", "/", nil)
	return b
}
func setup(t *testing.T) (*db.Store, *App, http.Handler) {
	t.Helper()
	store := testutil.Store(t)
	app, err := New(store, Config{BcryptCost: bcrypt.MinCost, AuthLimit: 1000, WriteLimit: 1000, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return store, app, app.Handler()
}
func (b *browser) request(method, path string, form url.Values) *httptest.ResponseRecorder {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		copied := url.Values{}
		for k, v := range form {
			copied[k] = append([]string(nil), v...)
		}
		if _, set := copied["csrf"]; !set {
			if cookie := b.cookies["discuss_csrf"]; cookie != nil {
				copied.Set("csrf", cookie.Value)
			}
		}
		body = strings.NewReader(copied.Encode())
	}
	request := httptest.NewRequest(method, path, body)
	request.RemoteAddr = b.ip
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, cookie := range b.cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	b.handler.ServeHTTP(recorder, request)
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.MaxAge < 0 {
			delete(b.cookies, cookie.Name)
		} else {
			b.cookies[cookie.Name] = cookie
		}
	}
	return recorder
}
func expect(t *testing.T, response *httptest.ResponseRecorder, status int, contains string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status %d want %d, body: %.1500s", response.Code, status, response.Body.String())
	}
	if contains != "" && !strings.Contains(response.Body.String(), contains) {
		t.Fatalf("body missing %q: %.1500s", contains, response.Body.String())
	}
}
func (b *browser) login(name string) {
	b.t.Helper()
	expect(b.t, b.request("POST", "/login", url.Values{"email": {name + "@example.com"}, "password": {"test-password"}}), 303, "")
}

func TestPublicPagesAndStatusCodes(t *testing.T) {
	_, _, handler := setup(t)
	b := newBrowser(t, handler)
	for _, path := range []string{"/", "/login", "/register", "/insights", "/insights/trending", "/about", "/static/css/style.css", "/healthz"} {
		expect(t, b.request("GET", path, nil), 200, "")
	}
	expect(t, b.request("HEAD", "/", nil), 200, "")
	expect(t, b.request("GET", "/missing", nil), 404, "That page could not be found")
	for _, path := range []string{"/posts/nope", "/posts/-1", "/?page=-1", "/?page=abc", "/?category=-3", "/?category=999", "/?scope=arbitrary", "/insights?page=0"} {
		expect(t, b.request("GET", path, nil), 400, "")
	}
	expect(t, b.request("GET", "/posts/999", nil), 404, "")
	for _, tc := range [][2]string{{"GET", "/logout"}, {"PUT", "/login"}, {"POST", "/insights"}, {"GET", "/reactions"}} {
		response := b.request(tc[0], tc[1], nil)
		expect(t, response, 405, "")
		if response.Header().Get("Allow") == "" {
			t.Fatal("405 lacks Allow")
		}
	}
	for _, path := range []string{"/posts/new", "/?scope=mine", "/?scope=liked"} {
		expect(t, b.request("GET", path, nil), 401, "Sign in")
	}
	for _, path := range []string{"/posts", "/posts/1/comments", "/reactions"} {
		expect(t, b.request("POST", path, url.Values{}), 401, "")
	}
}

func TestFullAuditJourneyAndBrowserIsolation(t *testing.T) {
	store, _, handler := setup(t)
	first := newBrowser(t, handler)
	second := newBrowser(t, handler)
	expect(t, first.request("POST", "/register", url.Values{"email": {"alice@example.com"}, "username": {"alice"}, "password": {"test-password"}}), 303, "")
	expect(t, first.request("POST", "/register", url.Values{"email": {"ALICE@example.com"}, "username": {"other"}, "password": {"test-password"}}), 409, "already registered")
	expect(t, first.request("POST", "/register", url.Values{"email": {"other@example.com"}, "username": {"ALICE"}, "password": {"test-password"}}), 409, "already taken")
	expect(t, first.request("POST", "/login", url.Values{}), 401, "incorrect")
	first.login("alice")
	cookie := first.cookies["discuss_session"]
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Expires.Before(time.Now()) || cookie.MaxAge <= 0 {
		t.Fatalf("bad session cookie: %+v", cookie)
	}
	expect(t, first.request("GET", "/posts/new", nil), 200, "Start a")
	expect(t, second.request("GET", "/posts/new", nil), 401, "")
	response := first.request("POST", "/posts", url.Values{"title": {"A good first question"}, "content": {"GREAT helpful good!"}, "categories": {"1", "2"}})
	expect(t, response, 303, "")
	location := response.Header().Get("Location")
	if location != "/posts/1" {
		t.Fatalf("location %s", location)
	}
	expect(t, second.request("GET", location, nil), 200, "A good first question")
	expect(t, first.request("POST", location+"/comments", url.Values{"content": {"An excellent reply"}}), 303, "")
	for _, value := range []string{"1", "1", "-1"} {
		expect(t, first.request("POST", "/reactions", url.Values{"kind": {"post"}, "id": {"1"}, "value": {value}}), 303, "")
	}
	expect(t, first.request("POST", "/reactions", url.Values{"kind": {"comment"}, "id": {"1"}, "value": {"1"}}), 303, "")
	var likes, dislikes int
	if err := store.SQL.QueryRow("SELECT COUNT(CASE WHEN value=1 THEN 1 END),COUNT(CASE WHEN value=-1 THEN 1 END) FROM post_reactions WHERE post_id=1").Scan(&likes, &dislikes); err != nil || likes != 0 || dislikes != 1 {
		t.Fatal("reaction exclusion", err)
	}
	expect(t, first.request("GET", "/?scope=mine", nil), 200, "A good first question")
	if strings.Contains(first.request("GET", "/?scope=liked", nil).Body.String(), "A good first question") {
		t.Fatal("disliked post in liked filter")
	}
	expect(t, first.request("POST", "/reactions", url.Values{"kind": {"post"}, "id": {"1"}, "value": {"1"}}), 303, "")
	expect(t, first.request("GET", "/?scope=liked&category=2", nil), 200, "A good first question")
	expect(t, second.request("GET", "/insights", nil), 200, "100.0")
	expect(t, second.request("GET", "/insights/trending", nil), 200, "helpful")
	second.login("alice")
	expect(t, first.request("GET", "/posts/new", nil), 401, "")
	expect(t, second.request("GET", "/posts/new", nil), 200, "")
	expect(t, first.request("POST", "/logout", url.Values{}), 303, "")
	expect(t, second.request("GET", "/posts/new", nil), 200, "")
	expect(t, second.request("POST", "/logout", url.Values{}), 303, "")
	expect(t, second.request("GET", "/posts/new", nil), 401, "")
}

func TestValidationXSSSQLInjectionAndPrivateFilterScope(t *testing.T) {
	store, _, handler := setup(t)
	aliceID := testutil.User(t, store, "alice")
	bobID := testutil.User(t, store, "bob")
	b := newBrowser(t, handler)
	b.login("alice")
	for _, form := range []url.Values{{"title": {" "}, "content": {"x"}, "categories": {"1"}}, {"title": {"x"}, "content": {" "}, "categories": {"1"}}, {"title": {"x"}, "content": {"x"}}, {"title": {"x"}, "content": {"x"}, "categories": {"999"}}, {"title": {"x"}, "content": {"x"}, "categories": {"1", "1"}}} {
		expect(t, b.request("POST", "/posts", form), 400, "")
	}
	title := `<script>alert('x')</script>`
	content := `<img src=x onerror=alert(1)> Robert'); DROP TABLE users;--`
	expect(t, b.request("POST", "/posts", url.Values{"title": {title}, "content": {content}, "categories": {"1"}}), 303, "")
	response := b.request("GET", "/posts/1", nil)
	expect(t, response, 200, "&lt;script&gt;")
	if strings.Contains(response.Body.String(), "<script>") || strings.Contains(response.Body.String(), "<img src=x") {
		t.Fatal("stored XSS not escaped")
	}
	expect(t, b.request("POST", "/posts/1/comments", url.Values{"content": {"  "}}), 400, "")
	expect(t, b.request("POST", "/posts/999/comments", url.Values{"content": {"x"}}), 404, "")
	for _, form := range []url.Values{{"kind": {"post"}, "id": {"1"}, "value": {"2"}}, {"kind": {"nope"}, "id": {"1"}, "value": {"1"}}, {"kind": {"post"}, "id": {"x"}, "value": {"1"}}} {
		expect(t, b.request("POST", "/reactions", form), 400, "")
	}
	expect(t, b.request("POST", "/reactions", url.Values{"kind": {"post"}, "id": {"999"}, "value": {"1"}}), 404, "")
	testutil.Post(t, store, bobID, "Bob private filter marker", "content", 2)
	response = b.request("GET", "/?scope=mine&user_id="+bobID, nil)
	if strings.Contains(response.Body.String(), "Bob private filter marker") {
		t.Fatal("user could select someone else's mine filter")
	}
	posts, _, err := store.Posts(context.Background(), db.Filter{Scope: "mine", UserID: aliceID, Page: 1})
	if err != nil || len(posts) != 1 {
		t.Fatal("SQL injection changed storage", err)
	}
}

func TestCSRFOriginBodyLimitsAndDuplicateFields(t *testing.T) {
	store, _, handler := setup(t)
	testutil.User(t, store, "alice")
	b := newBrowser(t, handler)
	b.login("alice")
	expect(t, b.request("POST", "/logout", url.Values{"csrf": {""}}), 403, "expired")
	expect(t, b.request("POST", "/logout", url.Values{"csrf": {"forged"}}), 403, "")
	expect(t, b.request("POST", "/posts", url.Values{"title": {"one", "two"}, "content": {"text"}, "categories": {"1"}}), 400, "only once")
	expect(t, b.request("POST", "/posts", url.Values{"content": {strings.Repeat("x", MaxFormBytes+1)}}), 413, "")
	request := httptest.NewRequest("POST", "/logout", strings.NewReader("csrf="+url.QueryEscape(b.cookies["discuss_csrf"].Value)))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://evil.example")
	for _, cookie := range b.cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	expect(t, recorder, 403, "another site")
	request = httptest.NewRequest("POST", "/login", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	expect(t, recorder, 415, "")
	// An anonymous CSRF token cannot be replayed after login changes the session.
	anonymous := newBrowser(t, handler)
	old := anonymous.cookies["discuss_csrf"].Value
	anonymous.login("alice")
	expect(t, anonymous.request("POST", "/logout", url.Values{"csrf": {old}}), 403, "")
}

func TestDatabaseOutageReturns500AndHealth503(t *testing.T) {
	store, _, handler := setup(t)
	testutil.User(t, store, "alice")
	signed := newBrowser(t, handler)
	signed.login("alice")
	guest := newBrowser(t, handler)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/insights", "/insights/trending"} {
		expect(t, guest.request("GET", path, nil), 500, "could not complete")
	}
	expect(t, signed.request("GET", "/", nil), 500, "")
	expect(t, guest.request("GET", "/healthz", nil), 503, "unavailable")
	response := guest.request("POST", "/login", url.Values{"email": {"alice@example.com"}, "password": {"test-password"}})
	expect(t, response, 500, "")
	if strings.Contains(response.Body.String(), "database is closed") {
		t.Fatal("internal details leaked")
	}
}

func TestRateLimitAndSecureCookies(t *testing.T) {
	store := testutil.Store(t)
	app, err := New(store, Config{BcryptCost: bcrypt.MinCost, AuthLimit: 2, SecureCookies: true})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/login", nil))
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !strings.HasPrefix(cookies[0].Name, "__Host-") || cookies[0].Domain != "" || cookies[0].Path != "/" {
		t.Fatalf("bad secure cookie: %+v", cookies)
	}
	if response.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("missing HSTS")
	}
	_, app, handler := setup(t)
	app.authLimit = 2
	b := newBrowser(t, handler)
	for i := 0; i < 2; i++ {
		expect(t, b.request("POST", "/login", url.Values{}), 401, "")
	}
	response = b.request("POST", "/login", url.Values{})
	expect(t, response, 429, "Too many")
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
	for key, value := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Cache-Control": "no-store"} {
		if response.Header().Get(key) != value {
			t.Errorf("missing %s", key)
		}
	}
}

func TestConcurrentHTTPReadersAndWriters(t *testing.T) {
	store, app, handler := setup(t)
	id := testutil.User(t, store, "alice")
	testutil.Post(t, store, id, "Stress topic", "Great helpful post", 1)
	// Each worker has its own request/response/cookie state. Share the one valid
	// session to exercise the intended single-session invariant under load.
	primary := newBrowser(t, handler)
	primary.login("alice")
	app.writeLimit = 10000
	var wait sync.WaitGroup
	for i := 0; i < 16; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			b := &browser{t: t, handler: handler, cookies: map[string]*http.Cookie{}, ip: fmt.Sprintf("192.0.2.%d:1234", i+2)}
			for key, cookie := range primary.cookies {
				copy := *cookie
				b.cookies[key] = &copy
			}
			for n := 0; n < 10; n++ {
				response := b.request("POST", "/posts/1/comments", url.Values{"content": {fmt.Sprintf("worker %d request %d", i, n)}})
				if response.Code != 303 {
					t.Errorf("write status %d", response.Code)
					return
				}
				response = b.request("GET", "/insights", nil)
				if response.Code != 200 {
					t.Errorf("read status %d", response.Code)
					return
				}
			}
		}(i)
	}
	wait.Wait()
	post, err := store.Post(context.Background(), 1, id)
	if err != nil || post.Comments != 160 {
		t.Fatalf("lost HTTP writes: %d %v", post.Comments, err)
	}
}

func TestExpiredSessionAndCSRFSessionBinding(t *testing.T) {
	store, _, handler := setup(t)
	alice := testutil.User(t, store, "alice")
	testutil.User(t, store, "bob")
	first := newBrowser(t, handler)
	second := newBrowser(t, handler)
	first.login("alice")
	second.login("bob")
	expect(t, second.request("POST", "/logout", url.Values{"csrf": {first.cookies["discuss_csrf"].Value}}), 403, "")
	if _, err := store.SQL.Exec("UPDATE sessions SET expires_at=? WHERE user_id=?", time.Now().Unix()-1, alice); err != nil {
		t.Fatal(err)
	}
	expect(t, first.request("POST", "/posts", url.Values{"title": {"Expired"}, "content": {"Body"}, "categories": {"1"}}), 401, "")
	expect(t, second.request("GET", "/posts/new", nil), 200, "")
}

func TestAuthBackpressureAndLimiterMemoryBound(t *testing.T) {
	_, app, handler := setup(t)
	b := newBrowser(t, handler)
	for i := 0; i < cap(app.authSlots); i++ {
		app.authSlots <- struct{}{}
	}
	expect(t, b.request("POST", "/login", url.Values{"email": {"alice@example.com"}, "password": {"test-password"}}), 503, "busy")
	for i := 0; i < cap(app.authSlots); i++ {
		<-app.authSlots
	}
	limit := newLimiter()
	for i := 0; i < 4096; i++ {
		if !limit.allow(fmt.Sprintf("key%d", i), 1, time.Minute) {
			t.Fatal("limit filled early")
		}
	}
	if limit.allow("overflow", 1, time.Minute) || len(limit.entries) != 4096 {
		t.Fatal("limiter grew without bound")
	}
	limit.entries["key0"] = limitEntry{Until: time.Now().Add(-time.Second)}
	if !limit.allow("new-key", 1, time.Minute) || len(limit.entries) != 4096 {
		t.Fatal("expired entries not reclaimed")
	}
}

func TestTrendingJSONAndAriaValues(t *testing.T) {
	store, _, handler := setup(t)
	user := testutil.User(t, store, "alice")
	testutil.Post(t, store, user, "Docker", "docker good", 1)
	b := newBrowser(t, handler)
	response := b.request("GET", "/insights/trending?format=json", nil)
	expect(t, response, 200, `"word":"docker","count":2`)
	if !strings.Contains(response.Header().Get("Content-Type"), "application/json") {
		t.Fatal("wrong JSON type")
	}
	b.login("alice")
	expect(t, b.request("GET", "/posts/1", nil), 200, `aria-pressed="false"`)
	expect(t, b.request("POST", "/reactions", url.Values{"kind": {"post"}, "id": {"1"}, "value": {"1"}}), 303, "")
	expect(t, b.request("GET", "/posts/1", nil), 200, `aria-pressed="true"`)
}
