package handlers

import (
	"errors"
	"fmt"
	"github.com/ernat-soltanbekov/discuss-hub/internal/db"
	"net/http"
	"net/url"
	"strconv"
)

func (a *App) index(w http.ResponseWriter, r *http.Request) {
	page, err := pageNumber(r)
	if err != nil {
		a.dataError(w, r, err)
		return
	}
	filter := db.Filter{Page: page, Scope: r.URL.Query().Get("scope"), UserID: userID(r)}
	if raw := r.URL.Query().Get("category"); raw != "" {
		filter.Category, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || filter.Category < 0 {
			a.dataError(w, r, db.ErrInvalid)
			return
		}
	}
	if (filter.Scope == "mine" || filter.Scope == "liked") && !a.requireUser(w, r) {
		return
	}
	categories, err := a.store.Categories(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if filter.Category > 0 {
		found := false
		for _, category := range categories {
			if category.ID == filter.Category {
				found = true
			}
		}
		if !found {
			a.dataError(w, r, db.ErrInvalid)
			return
		}
	}
	posts, more, err := a.store.Posts(r.Context(), filter)
	if err != nil {
		a.dataError(w, r, err)
		return
	}
	previous, next := pagination(r, page, more)
	a.render(w, r, "index", 200, pageData{Title: "A place to grow, together", Active: "forum", Posts: posts, Categories: categories, Filter: filter, Previous: previous, Next: next})
}
func (a *App) newPost(w http.ResponseWriter, r *http.Request) {
	if !a.requireUser(w, r) {
		return
	}
	a.postForm(w, r, 200, "", nil)
}
func (a *App) postForm(w http.ResponseWriter, r *http.Request, status int, message string, form url.Values) {
	categories, err := a.store.Categories(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.render(w, r, "new-post", status, pageData{Title: "Start a discussion", Active: "forum", Categories: categories, Error: message, Form: form})
}
func (a *App) createPost(w http.ResponseWriter, r *http.Request) {
	if !a.requireUser(w, r) || !a.form(w, r) || !a.allowWrite(w, r, false) {
		return
	}
	ids := []int64{}
	for _, raw := range r.PostForm["categories"] {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			a.dataError(w, r, db.ErrInvalid)
			return
		}
		ids = append(ids, id)
	}
	id, err := a.store.CreatePost(r.Context(), userID(r), r.PostForm.Get("title"), r.PostForm.Get("content"), ids)
	if errors.Is(err, db.ErrInvalid) {
		a.postForm(w, r, 400, "Add a title (up to 160 characters), a message (up to 10,000), and at least one valid category.", r.PostForm)
		return
	}
	if err != nil {
		a.dataError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/posts/%d", id), http.StatusSeeOther)
}
func (a *App) post(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		a.dataError(w, r, err)
		return
	}
	page, err := pageNumber(r)
	if err != nil {
		a.dataError(w, r, err)
		return
	}
	post, err := a.store.Post(r.Context(), id, userID(r))
	if err != nil {
		a.dataError(w, r, err)
		return
	}
	comments, more, err := a.store.Comments(r.Context(), id, userID(r), page)
	if err != nil {
		a.dataError(w, r, err)
		return
	}
	previous, next := pagination(r, page, more)
	a.render(w, r, "post", 200, pageData{Title: post.Title, Active: "forum", Post: post, Comments: comments, Previous: previous, Next: next})
}
func (a *App) comment(w http.ResponseWriter, r *http.Request) {
	if !a.requireUser(w, r) || !a.form(w, r) || !a.allowWrite(w, r, false) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		a.dataError(w, r, err)
		return
	}
	if err = a.store.CreateComment(r.Context(), userID(r), id, r.PostForm.Get("content")); err != nil {
		a.dataError(w, r, err)
		return
	}
	// Open the final page, so a newly added reply remains visible on long threads.
	post, err := a.store.Post(r.Context(), id, userID(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	page := (post.Comments-1)/db.PageSize + 1
	http.Redirect(w, r, fmt.Sprintf("/posts/%d?page=%d#comments", id, page), http.StatusSeeOther)
}
func (a *App) react(w http.ResponseWriter, r *http.Request) {
	if !a.requireUser(w, r) || !a.form(w, r) || !a.allowWrite(w, r, false) {
		return
	}
	id, err := strconv.ParseInt(r.PostForm.Get("id"), 10, 64)
	if err != nil {
		a.dataError(w, r, db.ErrInvalid)
		return
	}
	value, err := strconv.Atoi(r.PostForm.Get("value"))
	if err != nil {
		a.dataError(w, r, db.ErrInvalid)
		return
	}
	postID, err := a.store.React(r.Context(), userID(r), r.PostForm.Get("kind"), id, value)
	if err != nil {
		a.dataError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/posts/%d", postID), http.StatusSeeOther)
}
