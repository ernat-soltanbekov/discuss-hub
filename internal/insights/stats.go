package insights

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/ernat-soltanbekov/discuss-hub/internal/db"
)

type LabeledPost struct {
	ID        int64
	Title     string
	Reactions int
	Sentiment Sentiment
}
type ActiveCategory struct {
	Name  string
	Posts int
}
type ActiveUser struct {
	Name            string
	Posts, Comments int
}
type Mood struct {
	Label   string
	Count   int
	Percent float64
}
type Dashboard struct {
	TotalPosts, TotalComments, TotalUsers int
	Category                              ActiveCategory
	User                                  ActiveUser
	Moods                                 []Mood
	Top, Recent                           []LabeledPost
	More                                  bool
}

// DashboardFor reads a consistent SQLite snapshot. Engagement is aggregated in
// SQL; only content needed for keyword analysis is streamed through Go.
func DashboardFor(ctx context.Context, connection *sql.DB, page int) (Dashboard, error) {
	var result Dashboard
	if page < 1 || page > 100000 {
		return result, db.ErrInvalid
	}
	tx, err := connection.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM posts),
        (SELECT COUNT(*) FROM comments),(SELECT COUNT(*) FROM users)`).Scan(&result.TotalPosts, &result.TotalComments, &result.TotalUsers)
	if err != nil {
		return result, err
	}
	err = tx.QueryRowContext(ctx, `SELECT c.name,COUNT(*) FROM post_categories pc JOIN categories c ON c.id=pc.category_id
        GROUP BY c.id ORDER BY COUNT(*) DESC,c.name COLLATE NOCASE,c.id LIMIT 1`).Scan(&result.Category.Name, &result.Category.Posts)
	if err != nil && err != sql.ErrNoRows {
		return result, err
	}
	err = tx.QueryRowContext(ctx, `WITH p AS (SELECT user_id,COUNT(*) AS n FROM posts GROUP BY user_id),
        c AS (SELECT user_id,COUNT(*) AS n FROM comments GROUP BY user_id)
        SELECT u.username,COALESCE(p.n,0),COALESCE(c.n,0) FROM users u
        LEFT JOIN p ON p.user_id=u.id LEFT JOIN c ON c.user_id=u.id
        WHERE COALESCE(p.n,0)+COALESCE(c.n,0)>0
        ORDER BY COALESCE(p.n,0)+COALESCE(c.n,0) DESC,u.username COLLATE NOCASE,u.id LIMIT 1`).Scan(&result.User.Name, &result.User.Posts, &result.User.Comments)
	if err != nil && err != sql.ErrNoRows {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT content FROM posts")
	if err != nil {
		return result, err
	}
	counts := map[string]int{}
	for rows.Next() {
		var content string
		if err = rows.Scan(&content); err != nil {
			rows.Close()
			return result, err
		}
		counts[Analyze(content).Label]++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	for _, label := range []string{"Positive", "Neutral", "Negative"} {
		mood := Mood{Label: label, Count: counts[label]}
		if result.TotalPosts > 0 {
			mood.Percent = float64(mood.Count) * 100 / float64(result.TotalPosts)
		}
		result.Moods = append(result.Moods, mood)
	}
	result.Top, err = labeled(ctx, tx, `SELECT p.id,p.title,p.content,COUNT(r.user_id) FROM posts p
        LEFT JOIN post_reactions r ON r.post_id=p.id GROUP BY p.id
        ORDER BY COUNT(r.user_id) DESC,p.created_at DESC,p.id DESC LIMIT 5`)
	if err != nil {
		return result, err
	}
	result.Recent, err = labeled(ctx, tx, `SELECT id,title,content,0 FROM posts
        ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?`, db.PageSize+1, (page-1)*db.PageSize)
	if err != nil {
		return result, err
	}
	result.More = len(result.Recent) > db.PageSize
	if result.More {
		result.Recent = result.Recent[:db.PageSize]
	}
	return result, tx.Commit()
}

func labeled(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]LabeledPost, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []LabeledPost{}
	for rows.Next() {
		var post LabeledPost
		var content string
		if err = rows.Scan(&post.ID, &post.Title, &content, &post.Reactions); err != nil {
			return nil, err
		}
		post.Sentiment = Analyze(content)
		result = append(result, post)
	}
	return result, rows.Err()
}

type Topic struct {
	Word  string `json:"word"`
	Count int    `json:"count"`
}

var stopWords = wordSet("the a is in it and or of to an are as at be been but by for from has have he her him his i if into its me my not on our she so that their them there these they this was we were what when which who will with you your")

// Trending uses the rolling interval [now-24h, now]. Future-dated records are
// excluded. Titles and content both contribute; ties are alphabetical.
func Trending(ctx context.Context, connection *sql.DB, now time.Time) ([]Topic, error) {
	rows, err := connection.QueryContext(ctx, "SELECT title,content FROM posts WHERE created_at>=? AND created_at<=?", now.Add(-24*time.Hour).Unix(), now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var title, content string
		if err = rows.Scan(&title, &content); err != nil {
			return nil, err
		}
		for _, word := range Words(title + " " + content) {
			if !stopWords[word] {
				counts[word]++
			}
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	topics := make([]Topic, 0, len(counts))
	for word, count := range counts {
		topics = append(topics, Topic{Word: word, Count: count})
	}
	sort.Slice(topics, func(i, j int) bool {
		if topics[i].Count != topics[j].Count {
			return topics[i].Count > topics[j].Count
		}
		return topics[i].Word < topics[j].Word
	})
	if len(topics) > 10 {
		topics = topics[:10]
	}
	return topics, nil
}

func (s Sentiment) Explanation() string {
	return fmt.Sprintf("%d positive − %d negative = %d", s.Positive, s.Negative, s.Score)
}
