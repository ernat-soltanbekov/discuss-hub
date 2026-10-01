package auth_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ernat-soltanbekov/discuss-hub/internal/auth"
	"github.com/ernat-soltanbekov/discuss-hub/internal/testutil"
	"github.com/gofrs/uuid/v5"
	"golang.org/x/crypto/bcrypt"
)

func TestRegistrationValidationAndStorage(t *testing.T) {
	store := testutil.Store(t)
	service, err := auth.New(store, bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	user, err := service.Register(ctx, " Alice@Example.COM ", "Alice", "strong-password")
	if err != nil {
		t.Fatal(err)
	}
	id, err := uuid.FromString(user.ID)
	if err != nil || id.Version() != 4 {
		t.Fatalf("not a v4 UUID: %q", user.ID)
	}
	if user.Email != "alice@example.com" {
		t.Fatalf("email not normalized: %s", user.Email)
	}
	var hash string
	if err = store.SQL.QueryRow("SELECT password_hash FROM users WHERE id=?", user.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash == "strong-password" || bcrypt.CompareHashAndPassword([]byte(hash), []byte("strong-password")) != nil {
		t.Fatal("password was not hashed correctly")
	}
	cases := []struct {
		email, name, password string
		want                  error
	}{
		{"ALICE@example.com", "other", "strong-password", auth.ErrEmailTaken},
		{"other@example.com", "aLiCe", "strong-password", auth.ErrUsernameTaken},
		{"bad", "other", "strong-password", auth.ErrInvalid},
		{"Name <name@example.com>", "other", "strong-password", auth.ErrInvalid},
		{"other@example.com", "x", "strong-password", auth.ErrInvalid},
		{"other@example.com", "space name", "strong-password", auth.ErrInvalid},
		{"other@example.com", "other", "short", auth.ErrInvalid},
		{"other@example.com", "other", strings.Repeat("я", 37), auth.ErrInvalid},
		{"other@example.com", "other", "        ", auth.ErrInvalid},
	}
	for _, tc := range cases {
		_, err := service.Register(ctx, tc.email, tc.name, tc.password)
		if !errors.Is(err, tc.want) {
			t.Errorf("register %q %q: got %v want %v", tc.email, tc.name, err, tc.want)
		}
	}
}

func TestSessionIsolationReplacementExpiryAndLogout(t *testing.T) {
	store := testutil.Store(t)
	service, err := auth.New(store, bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	alice := testutil.User(t, store, "alice")
	bob := testutil.User(t, store, "bob")
	first, expiry, err := service.Login(ctx, "alice@example.com", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(expiry) < 23*time.Hour || time.Until(expiry) > 25*time.Hour {
		t.Fatal("bad expiry")
	}
	bobToken, _, err := service.Login(ctx, "bob@example.com", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := service.Login(ctx, "ALICE@example.com", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("session token reused")
	}
	for _, tc := range []struct{ token, want string }{{first, ""}, {second, alice}, {bobToken, bob}, {"", ""}, {"not-a-uuid", ""}} {
		user, err := service.User(ctx, tc.token)
		if err != nil {
			t.Fatal(err)
		}
		if tc.want == "" {
			if user != nil {
				t.Error("invalid session accepted")
			}
		} else if user == nil || user.ID != tc.want {
			t.Fatal("user identity leaked or missing")
		}
	}
	var stored string
	if err = store.SQL.QueryRow("SELECT token_hash FROM sessions WHERE user_id=?", alice).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == second || stored != auth.TokenHash(second) {
		t.Fatal("database stores raw session token")
	}
	// Logging out an obsolete browser must not revoke the replacement session.
	if err = service.Logout(ctx, first); err != nil {
		t.Fatal(err)
	}
	if user, err := service.User(ctx, second); err != nil || user == nil {
		t.Fatal("stale logout killed a new session")
	}
	if _, err = store.SQL.Exec("UPDATE sessions SET expires_at=? WHERE user_id=?", time.Now().Unix(), alice); err != nil {
		t.Fatal(err)
	}
	if user, err := service.User(ctx, second); err != nil || user != nil {
		t.Fatal("expired session accepted")
	}
	if err = service.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if err = service.Logout(ctx, bobToken); err != nil {
		t.Fatal(err)
	}
	if user, err := service.User(ctx, bobToken); err != nil || user != nil {
		t.Fatal("logout failed")
	}
}

func TestInvalidCredentialsAndDatabaseFailure(t *testing.T) {
	store := testutil.Store(t)
	service, err := auth.New(store, bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	testutil.User(t, store, "alice")
	for _, tc := range [][2]string{{"", ""}, {"alice@example.com", "wrong-pass"}, {"nobody@example.com", "test-password"}, {"alice@example.com' OR 1=1 --", "test-password"}} {
		if _, _, err := service.Login(ctx, tc[0], tc[1]); !errors.Is(err, auth.ErrCredentials) {
			t.Errorf("unexpected credential result: %v", err)
		}
	}
	token, _, err := service.Login(ctx, "alice@example.com", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.Login(ctx, "alice@example.com", "test-password"); err == nil || errors.Is(err, auth.ErrCredentials) {
		t.Fatal("database error hidden as bad credentials")
	}
	if _, err = service.User(ctx, token); err == nil {
		t.Fatal("database failure silently became anonymous")
	}
}

func TestConcurrentLoginLeavesOneSession(t *testing.T) {
	store := testutil.Store(t)
	service, err := auth.New(store, bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	testutil.User(t, store, "alice")
	var wait sync.WaitGroup
	tokens := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			token, _, err := service.Login(ctx, "alice@example.com", "test-password")
			if err != nil {
				t.Error(err)
				return
			}
			tokens <- token
		}()
	}
	wait.Wait()
	close(tokens)
	active := 0
	for token := range tokens {
		user, err := service.User(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		if user != nil {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("%d active sessions, want 1", active)
	}
}

func TestConcurrentRegistrationHonorsUniqueness(t *testing.T) {
	store := testutil.Store(t)
	service, err := auth.New(store, bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := service.Register(context.Background(), "same@example.com", "same", "test-password")
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, auth.ErrEmailTaken) && !errors.Is(err, auth.ErrUsernameTaken) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("%d duplicate registrations succeeded", successes)
	}
}
