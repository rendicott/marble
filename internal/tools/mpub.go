package tools

import (
	"fmt"
	"strings"

	"github.com/rendicott/marble/internal/mpub"
)

type mpubPublishArgs struct {
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Content     string   `json:"content"`
	ContentType string   `json:"content_type"`
	IfExists    string   `json:"if_exists"` // overwrite (default) | fail
	Tags        []string `json:"tags"`
}

func (r *Registry) mpubStore() (*mpub.Store, error) {
	if r.Memory == "" {
		return nil, fmt.Errorf("memory root not configured")
	}
	return mpub.New(r.Memory)
}

func (r *Registry) mpubPublish(argsJSON string, tc *TurnContext) (string, error) {
	var a mpubPublishArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Slug) == "" {
		return "", fmt.Errorf("slug is required")
	}
	if a.Content == "" {
		return "", fmt.Errorf("content is required")
	}
	store, err := r.mpubStore()
	if err != nil {
		return "", err
	}
	ifExistsFail := strings.EqualFold(strings.TrimSpace(a.IfExists), "fail")
	sessionID := ""
	if tc != nil {
		sessionID = tc.SessionID
	}
	meta, err := store.Publish(a.Slug, a.Title, a.Content, a.ContentType, sessionID, a.Tags, ifExistsFail)
	if err != nil {
		return "", err
	}
	path := "/mpub/" + meta.Slug
	url := mpub.PublicURL(r.publicAddr(), meta.Slug)
	return mustJSON(map[string]interface{}{
		"ok":           true,
		"slug":         meta.Slug,
		"title":        meta.Title,
		"content_type": meta.ContentType,
		"path":         path,
		"url":          url,
		"bytes":        meta.Bytes,
		"session_id":   meta.SessionID,
		"updated_at":   meta.UpdatedAt,
	}), nil
}

// PublicAddr is set from config --addr for tool URL results.
func (r *Registry) publicAddr() string {
	if r.Addr != "" {
		return r.Addr
	}
	return ":8080"
}

func (r *Registry) mpubList(argsJSON string) (string, error) {
	store, err := r.mpubStore()
	if err != nil {
		return "", err
	}
	list, err := store.List()
	if err != nil {
		return "", err
	}
	type row struct {
		Slug        string   `json:"slug"`
		Title       string   `json:"title"`
		ContentType string   `json:"content_type"`
		UpdatedAt   string   `json:"updated_at"`
		Path        string   `json:"path"`
		URL         string   `json:"url"`
		Tags        []string `json:"tags,omitempty"`
	}
	rows := make([]row, 0, len(list))
	for _, m := range list {
		rows = append(rows, row{
			Slug: m.Slug, Title: m.Title, ContentType: m.ContentType,
			UpdatedAt: m.UpdatedAt, Path: "/mpub/" + m.Slug,
			URL: mpub.PublicURL(r.publicAddr(), m.Slug), Tags: m.Tags,
		})
	}
	return mustJSON(rows), nil
}

type mpubSlugArgs struct {
	Slug string `json:"slug"`
}

func (r *Registry) mpubGet(argsJSON string) (string, error) {
	var a mpubSlugArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	store, err := r.mpubStore()
	if err != nil {
		return "", err
	}
	doc, err := store.Get(a.Slug)
	if err != nil {
		return "", err
	}
	return mustJSON(map[string]interface{}{
		"meta":    doc.Meta,
		"content": doc.Content,
		"path":    "/mpub/" + doc.Meta.Slug,
		"url":     mpub.PublicURL(r.publicAddr(), doc.Meta.Slug),
	}), nil
}

func (r *Registry) mpubUnpublish(argsJSON string) (string, error) {
	var a mpubSlugArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	store, err := r.mpubStore()
	if err != nil {
		return "", err
	}
	if err := store.Unpublish(a.Slug); err != nil {
		return "", err
	}
	return mustJSON(map[string]interface{}{"ok": true, "slug": a.Slug, "unpublished": true}), nil
}
