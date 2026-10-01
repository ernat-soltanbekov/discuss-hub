package db_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ernat-soltanbekov/discuss-hub/internal/db"
	"github.com/ernat-soltanbekov/discuss-hub/internal/testutil"
)

func TestSchemaPersistenceAndConstraints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "forum ?#.db")
	store, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	user := testutil.User(t, store, "alice")
	postID := testutil.Post(t, store, user, "Persistent", "great", 1, 2)
	var foreignKeys int
	if err = store.SQL.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatal("foreign keys are off", err)
	}
	if _, err = store.SQL.Exec("INSERT INTO comments(post_id,user_id,content,created_at) VALUES(9999,?,'orphan',0)", user); err == nil {
		t.Fatal("orphan accepted")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	post, err := store.Post(context.Background(), postID, user)
	if err != nil || post.Title != "Persistent" || len(post.Categories) != 2 {
		t.Fatal("reopen lost data", err)
	}
	categories, err := store.Categories(context.Background())
	if err != nil || len(categories) != 5 {
		t.Fatal("migration is not idempotent", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe database permissions %v", info.Mode())
	}
}

func TestInvalidPostsRollBackCompletely(t *testing.T) {
	store := testutil.Store(t)
	user := testutil.User(t, store, "alice")
	ctx := context.Background()
	cases := []struct {
		title, body string
		categories  []int64
	}{
		{" ", "text", []int64{1}}, {"title", " \n\t", []int64{1}}, {strings.Repeat("x", 161), "text", []int64{1}},
		{"title", strings.Repeat("я", 10001), []int64{1}}, {"title", "text", nil}, {"title", "text", []int64{1, 999}},
		{"title", "text", []int64{1, 1}}, {"title", "text", []int64{0}}, {"title", "nul\x00byte", []int64{1}},
	}
	for _, tc := range cases {
		if _, err := store.CreatePost(ctx, user, tc.title, tc.body, tc.categories); !errors.Is(err, db.ErrInvalid) {
			t.Errorf("got %v", err)
		}
	}
	var count int
	if err := store.SQL.QueryRow("SELECT COUNT(*) FROM posts").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial posts persisted: %d %v", count, err)
	}
	if _, err := store.CreatePost(ctx, user, strings.Repeat("я", 160), strings.Repeat("界", 10000), []int64{1}); err != nil {
		t.Fatal("valid Unicode boundary rejected", err)
	}
}

func TestFiltersReactionsAndPagination(t *testing.T) {
	store := testutil.Store(t)
	alice := testutil.User(t, store, "alice")
	bob := testutil.User(t, store, "bob")
	ctx := context.Background()
	first := testutil.Post(t, store, alice, "First", "content", 1, 2)
	second := testutil.Post(t, store, bob, "Second", "content", 2)
	if _, err := store.React(ctx, alice, "post", second, 1); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		filter db.Filter
		want   int64
	}{{db.Filter{Category: 1, Page: 1}, first}, {db.Filter{Scope: "mine", UserID: alice, Page: 1}, first}, {db.Filter{Scope: "liked", UserID: alice, Page: 1}, second}}
	for _, tc := range cases {
		posts, _, err := store.Posts(ctx, tc.filter)
		if err != nil || len(posts) != 1 || posts[0].ID != tc.want {
			t.Fatalf("filter %+v: %+v %v", tc.filter, posts, err)
		}
	}
	for _, value := range []int{1, 1, -1, -1} {
		if _, err := store.React(ctx, alice, "post", second, value); err != nil {
			t.Fatal(err)
		}
	}
	post, err := store.Post(ctx, second, alice)
	if err != nil || post.Likes != 0 || post.Dislikes != 1 || post.MyReaction != -1 {
		t.Fatalf("conflicting reactions: %+v %v", post, err)
	}
	if _, err = store.React(ctx, alice, "post", second, 0); err != nil {
		t.Fatal(err)
	}
	posts, _, err := store.Posts(ctx, db.Filter{Scope: "liked", UserID: alice, Page: 1})
	if err != nil || len(posts) != 0 {
		t.Fatal("removed like still filtered")
	}
	for i := 0; i < 21; i++ {
		testutil.Post(t, store, alice, fmt.Sprintf("Page %d", i), "content", 1)
	}
	posts, more, err := store.Posts(ctx, db.Filter{Page: 1})
	if err != nil || len(posts) != 20 || !more {
		t.Fatal("bad first page", err)
	}
	next, more, err := store.Posts(ctx, db.Filter{Page: 2})
	if err != nil || len(next) != 3 || more {
		t.Fatal("bad second page", err)
	}
	for _, left := range posts {
		for _, right := range next {
			if left.ID == right.ID {
				t.Fatal("duplicate across pages")
			}
		}
	}
	if _, _, err = store.Posts(ctx, db.Filter{Page: 0}); !errors.Is(err, db.ErrInvalid) {
		t.Fatal("invalid page accepted")
	}
	if _, _, err = store.Posts(ctx, db.Filter{Page: 1, Scope: "mine"}); !errors.Is(err, db.ErrInvalid) {
		t.Fatal("anonymous private filter accepted")
	}
}

func TestCommentReactionsAndMissingTargets(t *testing.T) {
	store := testutil.Store(t)
	user := testutil.User(t, store, "alice")
	ctx := context.Background()
	post := testutil.Post(t, store, user, "Title", "Content", 1)
	if err := store.CreateComment(ctx, user, post, "  "); !errors.Is(err, db.ErrInvalid) {
		t.Fatal("empty comment accepted")
	}
	if err := store.CreateComment(ctx, user, 999, "content"); !errors.Is(err, db.ErrNotFound) {
		t.Fatal("missing post accepted")
	}
	if err := store.CreateComment(ctx, user, post, "A helpful reply"); err != nil {
		t.Fatal(err)
	}
	comments, _, err := store.Comments(ctx, post, user, 1)
	if err != nil || len(comments) != 1 {
		t.Fatal(err)
	}
	id := comments[0].ID
	for _, value := range []int{1, 1, -1} {
		parent, err := store.React(ctx, user, "comment", id, value)
		if err != nil || parent != post {
			t.Fatal("bad comment reaction", err)
		}
	}
	comments, _, err = store.Comments(ctx, post, user, 1)
	if err != nil || comments[0].Likes != 0 || comments[0].Dislikes != 1 {
		t.Fatal("double reaction", err)
	}
	for _, kind := range []string{"post", "comment"} {
		for _, value := range []int{-1, 0, 1} {
			if _, err := store.React(ctx, user, kind, 999, value); !errors.Is(err, db.ErrNotFound) {
				t.Fatal("missing target", err)
			}
		}
	}
	if _, err = store.React(ctx, user, "posts; DROP TABLE users", post, 1); !errors.Is(err, db.ErrInvalid) {
		t.Fatal("untrusted table name accepted")
	}
}

func TestConcurrentWritesAndIntegrity(t *testing.T) {
	store := testutil.Store(t)
	user := testutil.User(t, store, "alice")
	ctx := context.Background()
	post := testutil.Post(t, store, user, "Stress", "Content", 1)
	var wait sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for n := 0; n < 8; n++ {
				if _, err := store.React(ctx, user, "post", post, 1-2*(worker%2)); err != nil {
					t.Error(err)
				}
				if err := store.CreateComment(ctx, user, post, fmt.Sprintf("Worker %d request %d", worker, n)); err != nil {
					t.Error(err)
				}
			}
		}(worker)
	}
	wait.Wait()
	p, err := store.Post(ctx, post, user)
	if err != nil || p.Comments != 256 || p.Likes+p.Dislikes != 1 {
		t.Fatalf("inconsistent state: %+v, %v", p, err)
	}
	var integrity string
	if err = store.SQL.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity %q %v", integrity, err)
	}
	rows, err := store.SQL.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key damage")
	}
}

func TestDatabaseFailureCancellationAndFutureSchema(t *testing.T) {
	store := testutil.Store(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Categories(ctx); err == nil {
		t.Fatal("cancellation ignored")
	}
	path := filepath.Join(t.TempDir(), "future.db")
	future, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = future.SQL.Exec("PRAGMA user_version=999"); err != nil {
		t.Fatal(err)
	}
	future.Close()
	if reopened, err := db.Open(path); err == nil {
		reopened.Close()
		t.Fatal("future schema accepted")
	}
	corrupt := filepath.Join(t.TempDir(), "corrupt.db")
	if err = os.WriteFile(corrupt, []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	if opened, err := db.Open(corrupt); err == nil {
		opened.Close()
		t.Fatal("corruption accepted")
	}
}

func TestDemoIsExplicitAndCannotOverwriteUsers(t *testing.T) {
	store := testutil.Store(t)
	ctx := context.Background()
	if err := store.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	posts, _, err := store.Posts(ctx, db.Filter{Page: 1})
	if err != nil || len(posts) != 6 {
		t.Fatal("demo incomplete", err)
	}
	if err := store.SeedDemo(ctx); err == nil {
		t.Fatal("demo overwrote existing content")
	}
}

func TestLockedWriterFailsWithoutPartialDataThenRecovers(t *testing.T) {
	store := testutil.Store(t)
	user := testutil.User(t, store, "alice")
	ctx := context.Background()
	tx, err := store.SQL.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE users SET username=username WHERE id=?", user); err != nil {
		t.Fatal(err)
	}
	// A separate writer must wait only for SQLite's bounded busy timeout.
	started := time.Now()
	if _, err = store.CreatePost(ctx, user, "Blocked writer", "Content", []int64{1}); err == nil {
		t.Fatal("a second writer bypassed the lock")
	}
	if time.Since(started) > 8*time.Second {
		t.Fatal("lock wait was not bounded")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	posts, _, err := store.Posts(ctx, db.Filter{Page: 1})
	if err != nil || len(posts) != 0 {
		t.Fatal("failed writer persisted data", err)
	}
	if _, err = store.CreatePost(ctx, user, "Recovered writer", "Content", []int64{1}); err != nil {
		t.Fatal("database did not recover", err)
	}
}
