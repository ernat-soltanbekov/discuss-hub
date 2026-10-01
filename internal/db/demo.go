package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
	"golang.org/x/crypto/bcrypt"
)

// SeedDemo is opt-in and refuses to touch a forum containing any users or posts.
// Sample accounts get random, discarded passwords; there are no default logins.
func (s *Store) SeedDemo(ctx context.Context) error {
	tx, err := s.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing int
	if err = tx.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM users)+(SELECT COUNT(*) FROM posts)").Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		return fmt.Errorf("demo seed requires an empty database; use a separate DB_PATH")
	}
	users := []string{}
	for index, name := range []string{"demo_practice", "demo_builder", "demo_peer"} {
		id, err := uuid.NewV4()
		if err != nil {
			return err
		}
		password := make([]byte, 24)
		if _, err = rand.Read(password); err != nil {
			return err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(hex.EncodeToString(password)), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO users(id,email,username,password_hash,created_at) VALUES(?,?,?,?,?)", id.String(), fmt.Sprintf("sample%d@example.invalid", index), name, string(hash), time.Now().Unix()); err != nil {
			return err
		}
		users = append(users, id.String())
	}
	samples := []struct {
		title, content string
		author         int
		categories     []int
	}{
		{"What daily practice teaches us about writing better code", "A sample discussion: since 2008, morning practice has reminded me that progress starts with showing up. The same attention helps when reading a difficult function.\n\nI love the connection between a good training habit and a helpful code review. What is one small habit that improved your learning?", 0, []int{3, 4}},
		{"Go + SQLite: small tools, strong foundations", "A sample engineering note: a foreign key is a promise the database helps us keep. A transaction makes a group of changes succeed or fail together.\n\nThe great thing about a small stack is that the whole request path stays visible. What database constraint saved you from a bug?", 1, []int{1, 2}},
		{"One useful question from today’s peer review", "Sample peer learning: explaining a design decision out loud can reveal the part you have not understood yet. Today’s question: what happens when the same user signs in twice?\n\nThanks for the helpful review. Clear questions make excellent learning tools.", 2, []int{2}},
		{"Building something useful for Astana’s tech community", "Sample conversation: local communities grow when people share practical experience. What small tool would make a student, mentor or community organizer’s day easier?\n\nBring a concrete problem and we can explore it together.", 0, []int{5, 1}},
		{"The broken migration that taught me to test recovery", "A sample debugging story: a broken migration left half of my test data missing. The error handling was bad, and guessing what went wrong was frustrating.\n\nWrapping related writes in one transaction made failure much easier to reason about. How do you test recovery?", 1, []int{1}},
		{"A quiet morning, a clear next step", "Sample practice log: warm up, train, reflect. Then choose one focused task for the day. No dramatic transformation required.\n\nWhat are you learning this week?", 0, []int{3}},
	}
	for index, sample := range samples {
		result, err := tx.ExecContext(ctx, "INSERT INTO posts(user_id,title,content,created_at) VALUES(?,?,?,?)", users[sample.author], sample.title, sample.content, time.Now().Add(-time.Duration(index+1)*time.Hour).Unix())
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		for _, category := range sample.categories {
			if _, err = tx.ExecContext(ctx, "INSERT INTO post_categories(post_id,category_id) VALUES(?,?)", id, category); err != nil {
				return err
			}
		}
		for vote := 0; vote < 3-index%3; vote++ {
			value := 1
			if index == 4 {
				value = -1
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO post_reactions(post_id,user_id,value) VALUES(?,?,?)", id, users[vote], value); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO comments(post_id,user_id,content,created_at) VALUES(?,?,?,?)", id, users[(sample.author+1)%3], "Sample reply: thanks for opening this discussion. A small example and a repeatable test make the next step clearer.", time.Now().Add(-30*time.Minute).Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
