package handlers

import (
	"context"
	"encoding/json"
	"github.com/ernat-soltanbekov/discuss-hub/internal/insights"
	"net/http"
	"time"
)

func (a *App) insights(w http.ResponseWriter, r *http.Request) {
	page, err := pageNumber(r)
	if err != nil {
		a.dataError(w, r, err)
		return
	}
	dashboard, err := insights.DashboardFor(r.Context(), a.store.SQL, page)
	if err != nil {
		a.dataError(w, r, err)
		return
	}
	previous, next := pagination(r, page, dashboard.More)
	a.render(w, r, "insights", 200, pageData{Title: "Listen to the community", Active: "insights", Dashboard: dashboard, Previous: previous, Next: next})
}
func (a *App) trending(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	topics, err := insights.Trending(r.Context(), a.store.SQL, now)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(struct {
			Since  time.Time        `json:"since"`
			Until  time.Time        `json:"until"`
			Topics []insights.Topic `json:"topics"`
		}{now.Add(-24 * time.Hour), now, topics})
		return
	}
	a.render(w, r, "trending", 200, pageData{Title: "What’s on our minds", Active: "trending", Topics: topics})
}
func (a *App) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	var schemaVersion int
	if err := a.store.SQL.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schemaVersion); err != nil {
		http.Error(w, "unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}
