package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rendicott/marble/internal/mpub"
)

type mpubPublishArgs struct {
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Content     string   `json:"content"`
	ContentPath string   `json:"content_path"` // workspace file to publish instead of inline content
	Assets      []string `json:"assets"`       // workspace image paths served beside the page
	ContentType string   `json:"content_type"`
	IfExists    string   `json:"if_exists"`  // overwrite (default) | fail
	Visibility  string   `json:"visibility"` // public | private (default private for new)
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
	content, contentType := a.Content, a.ContentType
	switch {
	case a.Content != "" && a.ContentPath != "":
		return "", fmt.Errorf("pass either content or content_path, not both")
	case a.ContentPath != "":
		data, _, err := r.readWorkspaceFile(a.ContentPath, mpub.MaxBodyBytes)
		if err != nil {
			return "", fmt.Errorf("content_path: %w", err)
		}
		content = string(data)
		if contentType == "" {
			contentType = contentTypeFromExt(a.ContentPath)
		}
	case a.Content == "":
		return "", fmt.Errorf("content or content_path is required")
	}
	var assets []mpub.Asset
	for _, p := range a.Assets {
		data, abs, err := r.readWorkspaceFile(p, mpub.MaxAssetBytes)
		if err != nil {
			return "", fmt.Errorf("asset %q: %w", p, err)
		}
		assets = append(assets, mpub.Asset{Name: filepath.Base(abs), Data: data})
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
	meta, err := store.Publish(a.Slug, a.Title, content, contentType, sessionID, a.Tags, ifExistsFail, a.Visibility, assets)
	if err != nil {
		return "", err
	}
	stored, _ := store.ListAssets(meta.Slug)
	warnings := mpub.Lint(meta.ContentType, content, stored)
	out := map[string]interface{}{
		"ok":           true,
		"slug":         meta.Slug,
		"title":        meta.Title,
		"content_type": meta.ContentType,
		"visibility":   mpub.EffectiveVisibility(*meta),
		"path":         "/mpub/" + meta.Slug,
		"bytes":        meta.Bytes,
		"sha256":       contentSHA256(content),
		"assets":       r.mpubAssetRows(meta.Slug, stored),
		"summary":      fmt.Sprintf("published %d bytes, %d assets", meta.Bytes, len(stored)),
		"session_id":   meta.SessionID,
		"updated_at":   meta.UpdatedAt,
		"note":         "Default visibility is private (admins only when OAuth is on). Set visibility=public to share openly.",
	}
	r.addMpubURLs(out, meta.Slug)
	if len(warnings) > 0 {
		out["warnings"] = warnings
	}
	return mustJSON(out), nil
}

func contentSHA256(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func contentTypeFromExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	default:
		return "text/html"
	}
}

// readWorkspaceFile reads a regular file inside the workspace jail, following
// symlinks only if they stay inside it. It returns the data and the resolved path.
func (r *Registry) readWorkspaceFile(rel string, maxBytes int64) ([]byte, string, error) {
	abs, err := r.resolve(rel)
	if err != nil {
		return nil, "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, "", err
	}
	wsReal, err := filepath.EvalSymlinks(r.Workspace)
	if err != nil {
		return nil, "", err
	}
	if real != wsReal && !strings.HasPrefix(real, wsReal+string(os.PathSeparator)) {
		return nil, "", fmt.Errorf("symlink escapes workspace")
	}
	fi, err := os.Stat(real)
	if err != nil {
		return nil, "", err
	}
	if !fi.Mode().IsRegular() {
		return nil, "", fmt.Errorf("not a regular file")
	}
	if fi.Size() > maxBytes {
		return nil, "", fmt.Errorf("file is %d bytes (max %d)", fi.Size(), maxBytes)
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return nil, "", err
	}
	// Name/extension come from the path the caller gave, not the symlink target.
	return data, abs, nil
}

// mpubURL is the browser-reachable URL for a slug. Prefers the operator's public
// origin (--public-url / OAuth host / listen host) over loopback, which is
// unreachable when the harness binds only to a Tailscale interface.
func (r *Registry) mpubURL(slug string) string {
	if r.PublicBaseURL != nil {
		base := strings.TrimRight(strings.TrimSpace(r.PublicBaseURL()), "/")
		base = strings.Replace(base, "://0.0.0.0", "://127.0.0.1", 1)
		base = strings.Replace(base, "://[::]", "://127.0.0.1", 1)
		if base != "" {
			return base + "/mpub/" + slug
		}
	}
	return mpub.PublicURL(r.publicAddr(), slug)
}

// addMpubURLs sets url (the working one) and, when different, local_url (loopback).
func (r *Registry) addMpubURLs(out map[string]interface{}, slug string) {
	url := r.mpubURL(slug)
	out["url"] = url
	if local := mpub.PublicURL(r.publicAddr(), slug); local != url {
		out["local_url"] = local
	}
}

func (r *Registry) mpubAssetRows(slug string, assets []mpub.AssetInfo) []map[string]interface{} {
	rows := make([]map[string]interface{}, 0, len(assets))
	for _, a := range assets {
		rows = append(rows, map[string]interface{}{
			"name": a.Name, "bytes": a.Bytes, "content_type": a.ContentType,
			"url": r.mpubURL(slug) + "/" + a.Name,
		})
	}
	return rows
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
		Visibility  string   `json:"visibility"`
		UpdatedAt   string   `json:"updated_at"`
		Path        string   `json:"path"`
		URL         string   `json:"url"`
		Tags        []string `json:"tags,omitempty"`
	}
	rows := make([]row, 0, len(list))
	for _, m := range list {
		rows = append(rows, row{
			Slug: m.Slug, Title: m.Title, ContentType: m.ContentType,
			Visibility: mpub.EffectiveVisibility(m),
			UpdatedAt:  m.UpdatedAt, Path: "/mpub/" + m.Slug,
			URL: r.mpubURL(m.Slug), Tags: m.Tags,
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
	out := map[string]interface{}{
		"meta":    doc.Meta,
		"content": doc.Content,
		"bytes":   len(doc.Content),
		"sha256":  contentSHA256(doc.Content),
		"assets":  r.mpubAssetRows(doc.Meta.Slug, doc.Assets),
		"path":    "/mpub/" + doc.Meta.Slug,
	}
	r.addMpubURLs(out, doc.Meta.Slug)
	return mustJSON(out), nil
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

type mpubVisArgs struct {
	Slug       string `json:"slug"`
	Visibility string `json:"visibility"`
}

func (r *Registry) mpubSetVisibility(argsJSON string) (string, error) {
	var a mpubVisArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Slug) == "" {
		return "", fmt.Errorf("slug is required")
	}
	store, err := r.mpubStore()
	if err != nil {
		return "", err
	}
	meta, err := store.SetVisibility(a.Slug, a.Visibility)
	if err != nil {
		return "", err
	}
	out := map[string]interface{}{
		"ok":         true,
		"slug":       meta.Slug,
		"visibility": mpub.EffectiveVisibility(*meta),
		"path":       "/mpub/" + meta.Slug,
		"updated_at": meta.UpdatedAt,
	}
	r.addMpubURLs(out, meta.Slug)
	return mustJSON(out), nil
}
