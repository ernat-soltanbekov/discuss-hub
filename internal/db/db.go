// Package db owns persistence. SQL constraints are the final line of defence
// when simultaneous requests try to change the same data.
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schema string

var (
	ErrInvalid  = errors.New("invalid input")
	ErrNotFound = errors.New("not found")
)

const PageSize = 20

type Store struct{ SQL *sql.DB }
type Category struct {
	ID                int64
	Name, Description string
	Posts             int
}
type Post struct {
	ID                                    int64
	Author, Title, Content                string
	CreatedAt                             int64
	Likes, Dislikes, Comments, MyReaction int
	Categories                            []string
}
type Comment struct {
	ID, PostID                  int64
	Author, Content             string
	CreatedAt                   int64
	Likes, Dislikes, MyReaction int
}
type Filter struct {
	Category      int64
	Scope, UserID string
	Page          int
}

// Open creates a file-backed database. The URI options apply to EVERY pooled
// connection, so foreign-key enforcement cannot disappear under concurrency.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("database path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	if err = os.Chmod(absolute, 0600); err != nil {
		return nil, err
	}
	location := url.URL{Scheme: "file", Path: absolute}
	connection, err := sql.Open("sqlite3", location.String()+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_synchronous=FULL")
	if err != nil {
		return nil, err
	}
	connection.SetMaxOpenConns(8)
	connection.SetMaxIdleConns(8)
	store := &Store{SQL: connection}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = store.migrate(ctx); err != nil {
		connection.Close()
		return nil, fmt.Errorf("initialize database: %w", err)
	}
	return store, nil
}

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.SQL.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return fmt.Errorf("database version %d is newer than this application", version)
	}
	if version == 1 {
		return s.SQL.PingContext(ctx)
	}
	tx, err := s.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.SQL.Close() }

// ValidText counts Unicode characters and rejects invalid UTF-8 and NUL bytes.
func ValidText(value string, maximum int) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0) && strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= maximum
}

func (s *Store) Categories(ctx context.Context) ([]Category, error) {
	rows, err := s.SQL.QueryContext(ctx, `SELECT c.id, c.name, c.description, COUNT(pc.post_id)
        FROM categories c LEFT JOIN post_categories pc ON pc.category_id=c.id
        GROUP BY c.id ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Category{}
	for rows.Next() {
		var c Category
		if err = rows.Scan(&c.ID, &c.Name, &c.Description, &c.Posts); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func (s *Store) CreatePost(ctx context.Context, userID, title, content string, categoryIDs []int64) (int64, error) {
	title, content = strings.TrimSpace(title), strings.TrimSpace(content)
	if !ValidText(title, 160) || !ValidText(content, 10000) || len(categoryIDs) < 1 || len(categoryIDs) > 5 {
		return 0, ErrInvalid
	}
	seen := make(map[int64]bool)
	for _, id := range categoryIDs {
		if id <= 0 || seen[id] {
			return 0, ErrInvalid
		}
		seen[id] = true
	}
	tx, err := s.SQL.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "INSERT INTO posts(user_id,title,content,created_at) VALUES(?,?,?,?)", userID, title, content, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, categoryID := range categoryIDs {
		if _, err = tx.ExecContext(ctx, "INSERT INTO post_categories(post_id,category_id) VALUES(?,?)", id, categoryID); err != nil {
			var sqliteErr sqlite3.Error
			if errors.As(err, &sqliteErr) && sqliteErr.ExtendedCode == sqlite3.ErrConstraintForeignKey {
				return 0, ErrInvalid
			}
			return 0, err
		}
	}
	return id, tx.Commit()
}

// Correlated counts use indexes and run only for the requested page. Combining
// independent reaction/category joins would multiply rows and inflate counts.
const postSelect = `SELECT p.id, u.username, p.title, p.content, p.created_at,
    (SELECT COUNT(*) FROM post_reactions r WHERE r.post_id=p.id AND r.value=1),
    (SELECT COUNT(*) FROM post_reactions r WHERE r.post_id=p.id AND r.value=-1),
    (SELECT COUNT(*) FROM comments c WHERE c.post_id=p.id),
    COALESCE((SELECT value FROM post_reactions r WHERE r.post_id=p.id AND r.user_id=?),0),
    COALESCE((SELECT GROUP_CONCAT(name, '|') FROM
        (SELECT c.name FROM categories c JOIN post_categories pc ON pc.category_id=c.id WHERE pc.post_id=p.id ORDER BY c.id)), '')
    FROM posts p JOIN users u ON u.id=p.user_id `

type scanner interface{ Scan(...any) error }

func scanPost(row scanner) (Post, error) {
	var p Post
	var categories string
	err := row.Scan(&p.ID, &p.Author, &p.Title, &p.Content, &p.CreatedAt, &p.Likes, &p.Dislikes, &p.Comments, &p.MyReaction, &categories)
	if categories != "" {
		p.Categories = strings.Split(categories, "|")
	}
	return p, err
}

func (s *Store) Posts(ctx context.Context, filter Filter) ([]Post, bool, error) {
	if filter.Page < 1 || filter.Page > 100000 || filter.Category < 0 {
		return nil, false, ErrInvalid
	}
	query := postSelect + " WHERE 1=1"
	args := []any{filter.UserID}
	if filter.Category > 0 {
		query += " AND EXISTS(SELECT 1 FROM post_categories pc WHERE pc.post_id=p.id AND pc.category_id=?)"
		args = append(args, filter.Category)
	}
	switch filter.Scope {
	case "", "all":
	case "mine":
		if filter.UserID == "" {
			return nil, false, ErrInvalid
		}
		query += " AND p.user_id=?"
		args = append(args, filter.UserID)
	case "liked":
		if filter.UserID == "" {
			return nil, false, ErrInvalid
		}
		query += " AND EXISTS(SELECT 1 FROM post_reactions r WHERE r.post_id=p.id AND r.user_id=? AND r.value=1)"
		args = append(args, filter.UserID)
	default:
		return nil, false, ErrInvalid
	}
	query += " ORDER BY p.created_at DESC, p.id DESC LIMIT ? OFFSET ?"
	args = append(args, PageSize+1, (filter.Page-1)*PageSize)
	rows, err := s.SQL.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := []Post{}
	for rows.Next() {
		post, err := scanPost(rows)
		if err != nil {
			return nil, false, err
		}
		result = append(result, post)
	}
	more := len(result) > PageSize
	if more {
		result = result[:PageSize]
	}
	return result, more, rows.Err()
}

func (s *Store) Post(ctx context.Context, id int64, userID string) (Post, error) {
	post, err := scanPost(s.SQL.QueryRowContext(ctx, postSelect+" WHERE p.id=?", userID, id))
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return post, err
}

func (s *Store) CreateComment(ctx context.Context, userID string, postID int64, content string) error {
	content = strings.TrimSpace(content)
	if !ValidText(content, 5000) {
		return ErrInvalid
	}
	result, err := s.SQL.ExecContext(ctx, `INSERT INTO comments(post_id,user_id,content,created_at)
        SELECT id,?,?,? FROM posts WHERE id=?`, userID, content, time.Now().Unix(), postID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Comments(ctx context.Context, postID int64, userID string, page int) ([]Comment, bool, error) {
	if page < 1 || page > 100000 {
		return nil, false, ErrInvalid
	}
	rows, err := s.SQL.QueryContext(ctx, `SELECT c.id,c.post_id,u.username,c.content,c.created_at,
        (SELECT COUNT(*) FROM comment_reactions r WHERE r.comment_id=c.id AND r.value=1),
        (SELECT COUNT(*) FROM comment_reactions r WHERE r.comment_id=c.id AND r.value=-1),
        COALESCE((SELECT value FROM comment_reactions r WHERE r.comment_id=c.id AND r.user_id=?),0)
        FROM comments c JOIN users u ON u.id=c.user_id WHERE c.post_id=? ORDER BY c.id LIMIT ? OFFSET ?`, userID, postID, PageSize+1, (page-1)*PageSize)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := []Comment{}
	for rows.Next() {
		var c Comment
		if err = rows.Scan(&c.ID, &c.PostID, &c.Author, &c.Content, &c.CreatedAt, &c.Likes, &c.Dislikes, &c.MyReaction); err != nil {
			return nil, false, err
		}
		result = append(result, c)
	}
	more := len(result) > PageSize
	if more {
		result = result[:PageSize]
	}
	return result, more, rows.Err()
}

// React is idempotent: repeating a like leaves one like. Opposite reactions
// replace one another atomically; value zero explicitly removes a reaction.
// Table names come from this fixed allowlist, never from user-provided SQL.
func (s *Store) React(ctx context.Context, userID, kind string, targetID int64, value int) (int64, error) {
	if value < -1 || value > 1 || targetID <= 0 {
		return 0, ErrInvalid
	}
	table, column, targets := "post_reactions", "post_id", "posts"
	if kind == "comment" {
		table, column, targets = "comment_reactions", "comment_id", "comments"
	} else if kind != "post" {
		return 0, ErrInvalid
	}
	tx, err := s.SQL.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// Write first: avoids upgrading a read transaction while another writer
	// commits (SQLITE_BUSY_SNAPSHOT). Foreign keys check the target exists.
	if value == 0 {
		_, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE "+column+"=? AND user_id=?", targetID, userID)
	} else {
		_, err = tx.ExecContext(ctx, "INSERT INTO "+table+"("+column+",user_id,value) VALUES(?,?,?) ON CONFLICT("+column+",user_id) DO UPDATE SET value=excluded.value", targetID, userID, value)
	}
	if err != nil {
		var sqliteErr sqlite3.Error
		if errors.As(err, &sqliteErr) && sqliteErr.ExtendedCode == sqlite3.ErrConstraintForeignKey {
			return 0, ErrNotFound
		}
		return 0, err
	}
	parent := "id"
	if kind == "comment" {
		parent = "post_id"
	}
	var postID int64
	if err = tx.QueryRowContext(ctx, "SELECT "+parent+" FROM "+targets+" WHERE id=?", targetID).Scan(&postID); errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	return postID, tx.Commit()
}
