// Package testutil provides isolated, file-backed SQLite fixtures for tests.
package testutil

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ernat-soltanbekov/discuss-hub/internal/db"
	"github.com/gofrs/uuid/v5"
	"golang.org/x/crypto/bcrypt"
)

func Store(t testing.TB) *db.Store {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "forum.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
func User(t testing.TB, store *db.Store, name string) string {
	t.Helper()
	id, err := uuid.NewV4()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.SQL.Exec("INSERT INTO users(id,email,username,password_hash,created_at) VALUES(?,?,?,?,?)", id.String(), name+"@example.com", name, string(hash), time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	return id.String()
}
func Post(t testing.TB, store *db.Store, userID, title, content string, categories ...int64) int64 {
	t.Helper()
	id, err := store.CreatePost(context.Background(), userID, title, content, categories)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
