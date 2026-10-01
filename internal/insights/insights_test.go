package insights_test

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ernat-soltanbekov/discuss-hub/internal/insights"
	"github.com/ernat-soltanbekov/discuss-hub/internal/testutil"
)

func TestSentimentFormula(t *testing.T) {
	cases := []struct {
		content, label     string
		positive, negative int
	}{
		{"great love excellent amazing helpful good thanks awesome", "Positive", 8, 0},
		{"bad hate terrible awful horrible wrong broken useless", "Negative", 0, 8},
		{"GREAT! Great, good.", "Positive", 3, 0},
		{"great love bad hate", "Neutral", 2, 2},
		{"", "Neutral", 0, 0}, {"How does SQLite work?", "Neutral", 0, 0},
		{"badge lovingly goodness unbroken", "Neutral", 0, 0},
		{"not good", "Positive", 1, 0},
		{"Привет, мир!", "Neutral", 0, 0},
		{"(good)\ngood—bad\tHATE", "Neutral", 2, 2},
	}
	for _, tc := range cases {
		t.Run(tc.content, func(t *testing.T) {
			got := insights.Analyze(tc.content)
			if got.Label != tc.label || got.Positive != tc.positive || got.Negative != tc.negative || got.Score != tc.positive-tc.negative {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestEmptyDashboard(t *testing.T) {
	store := testutil.Store(t)
	dashboard, err := insights.DashboardFor(context.Background(), store.SQL, 1)
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.TotalPosts != 0 || dashboard.User.Name != "" || dashboard.Category.Name != "" || len(dashboard.Top) != 0 {
		t.Fatalf("empty dashboard: %+v", dashboard)
	}
	for _, mood := range dashboard.Moods {
		if mood.Count != 0 || mood.Percent != 0 || math.IsNaN(mood.Percent) {
			t.Fatal("undefined empty mood")
		}
	}
	topics, err := insights.Trending(context.Background(), store.SQL, time.Now())
	if err != nil || topics == nil || len(topics) != 0 {
		t.Fatalf("empty topics should be []: %v %v", topics, err)
	}
}

func TestSQLStatsNoJoinMultiplicationAndTopFive(t *testing.T) {
	store := testutil.Store(t)
	ctx := context.Background()
	alice := testutil.User(t, store, "alice")
	bob := testutil.User(t, store, "bob")
	carol := testutil.User(t, store, "carol")
	var ids []int64
	for i, body := range []string{"good good", "bad", "neutral", "good bad", "excellent", "broken"} {
		author := bob
		if i < 2 {
			author = alice
		}
		ids = append(ids, testutil.Post(t, store, author, fmt.Sprintf("Post %d", i), body, 1, 2))
	}
	// A third category on one post must not multiply users, comments or votes.
	if _, err := store.SQL.Exec("INSERT INTO post_categories(post_id,category_id) VALUES(?,3)", ids[0]); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		if err := store.CreateComment(ctx, carol, ids[0], "Reply"); err != nil {
			t.Fatal(err)
		}
	}
	for _, user := range []string{alice, bob, carol} {
		if _, err := store.React(ctx, user, "post", ids[1], -1); err != nil {
			t.Fatal(err)
		}
	}
	for _, user := range []string{alice, bob} {
		if _, err := store.React(ctx, user, "post", ids[0], 1); err != nil {
			t.Fatal(err)
		}
	}
	dashboard, err := insights.DashboardFor(ctx, store.SQL, 1)
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.TotalPosts != 6 || dashboard.TotalComments != 7 || dashboard.TotalUsers != 3 {
		t.Fatalf("inflated totals: %+v", dashboard)
	}
	if dashboard.Category.Name != "Engineering" || dashboard.Category.Posts != 6 {
		t.Fatalf("category aggregation/tie broken: %+v", dashboard.Category)
	}
	if dashboard.User.Name != "carol" || dashboard.User.Posts != 0 || dashboard.User.Comments != 7 {
		t.Fatalf("user activity: %+v", dashboard.User)
	}
	if len(dashboard.Top) != 5 || dashboard.Top[0].ID != ids[1] || dashboard.Top[0].Reactions != 3 || dashboard.Top[1].ID != ids[0] || dashboard.Top[1].Reactions != 2 {
		t.Fatalf("top posts: %+v", dashboard.Top)
	}
	for _, mood := range dashboard.Moods {
		if mood.Count != 2 || math.Abs(mood.Percent-100.0/3) > 0.000001 {
			t.Fatalf("mood: %+v", mood)
		}
	}
	// Sentiment uses content, never the title.
	testutil.Post(t, store, alice, "great excellent amazing", "plain content", 1)
	dashboard, err = insights.DashboardFor(ctx, store.SQL, 1)
	if err != nil || dashboard.Recent[0].Sentiment.Label != "Neutral" {
		t.Fatal("title affected mood", err)
	}
}

func TestActiveUserCombinesPostsAndCommentsAndStableTies(t *testing.T) {
	store := testutil.Store(t)
	ctx := context.Background()
	alice := testutil.User(t, store, "alice")
	bob := testutil.User(t, store, "bob")
	p := testutil.Post(t, store, alice, "First", "Content", 1)
	testutil.Post(t, store, bob, "Second", "Content", 1)
	if err := store.CreateComment(ctx, alice, p, "one"); err != nil {
		t.Fatal(err)
	}
	dashboard, err := insights.DashboardFor(ctx, store.SQL, 1)
	if err != nil || dashboard.User.Name != "alice" || dashboard.User.Posts != 1 || dashboard.User.Comments != 1 {
		t.Fatal("combined activity failed", err)
	}
	if err := store.CreateComment(ctx, bob, p, "two"); err != nil {
		t.Fatal(err)
	}
	dashboard, err = insights.DashboardFor(ctx, store.SQL, 1)
	if err != nil || dashboard.User.Name != "alice" {
		t.Fatal("tie is not deterministic", err)
	}
}

func TestRecentSentimentPagination(t *testing.T) {
	store := testutil.Store(t)
	user := testutil.User(t, store, "alice")
	ctx := context.Background()
	for i := 0; i < 23; i++ {
		testutil.Post(t, store, user, fmt.Sprintf("Post %d", i), "good", 1)
	}
	first, err := insights.DashboardFor(ctx, store.SQL, 1)
	if err != nil || len(first.Recent) != 20 || !first.More || first.TotalPosts != 23 {
		t.Fatal("first page", err)
	}
	second, err := insights.DashboardFor(ctx, store.SQL, 2)
	if err != nil || len(second.Recent) != 3 || second.More || second.TotalPosts != 23 {
		t.Fatal("second page", err)
	}
	if first.Recent[19].ID == second.Recent[0].ID {
		t.Fatal("repeated post")
	}
}

func TestTrendingBoundariesStopWordsCountsAndTies(t *testing.T) {
	store := testutil.Store(t)
	user := testutil.User(t, store, "alice")
	ctx := context.Background()
	now := time.Unix(1900000000, 0)
	fixtures := []struct {
		title, content string
		at             time.Time
	}{
		{"Docker DOCKER", "docker authentication SQL sql the a is in it and or of to", now},
		{"database", "database authentication", now.Add(-24 * time.Hour)},
		{"excluded excluded", "excluded", now.Add(-24*time.Hour - time.Second)},
		{"future future", "future", now.Add(time.Second)},
		{"алматы", "Алматы, Go!", now.Add(-time.Hour)},
	}
	for _, fixture := range fixtures {
		id := testutil.Post(t, store, user, fixture.title, fixture.content, 1)
		if _, err := store.SQL.Exec("UPDATE posts SET created_at=? WHERE id=?", fixture.at.Unix(), id); err != nil {
			t.Fatal(err)
		}
	}
	topics, err := insights.Trending(ctx, store.SQL, now)
	if err != nil {
		t.Fatal(err)
	}
	expected := []insights.Topic{{Word: "docker", Count: 3}, {Word: "authentication", Count: 2}, {Word: "database", Count: 2}, {Word: "sql", Count: 2}, {Word: "алматы", Count: 2}, {Word: "go", Count: 1}}
	if !reflect.DeepEqual(topics, expected) {
		t.Fatalf("got %#v want %#v", topics, expected)
	}
}

func TestTrendingTopTenAndDatabaseErrors(t *testing.T) {
	store := testutil.Store(t)
	user := testutil.User(t, store, "alice")
	now := time.Now()
	ctx := context.Background()
	testutil.Post(t, store, user, "alphabet", "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike", 1)
	topics, err := insights.Trending(ctx, store.SQL, now)
	if err != nil || len(topics) != 10 {
		t.Fatalf("top ten: %v %v", topics, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = insights.Trending(ctx, store.SQL, now); err == nil {
		t.Fatal("trending swallowed database error")
	}
	if _, err = insights.DashboardFor(ctx, store.SQL, 1); err == nil {
		t.Fatal("dashboard swallowed database error")
	}
}

func FuzzSentimentInvariant(f *testing.F) {
	for _, seed := range []string{"great bad", "", "GOOD!", "Привет мир", "not good", strings.Repeat("bad ", 100)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		got := insights.Analyze(input)
		if got.Score != got.Positive-got.Negative {
			t.Fatal("score invariant")
		}
		want := "Neutral"
		if got.Score > 0 {
			want = "Positive"
		} else if got.Score < 0 {
			want = "Negative"
		}
		if got.Label != want {
			t.Fatal("label invariant")
		}
		if lower := insights.Analyze(strings.ToLower(input)); lower != got {
			t.Fatal("case invariance")
		}
	})
}

func BenchmarkSentiment(b *testing.B) {
	text := strings.Repeat("A great helpful discussion about Go, with one broken example. ", 100)
	b.ReportAllocs()
	for b.Loop() {
		insights.Analyze(text)
	}
}
