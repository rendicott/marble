package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/rendicott/marble/internal/workspacefs"
)

func (s *Server) handleWorkspace(w http.ResponseWriter, r *http.Request) {
	if s.WS == nil {
		http.Error(w, "workspace unavailable", http.StatusServiceUnavailable)
		return
	}
	// Route by path after /api/workspace
	p := strings.TrimPrefix(r.URL.Path, "/api/workspace")
	p = strings.Trim(p, "/")

	switch {
	case p == "" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"root": s.WS.Root,
			"sep":  string(filepath.Separator),
		})
	case p == "list" && r.Method == http.MethodGet:
		s.wsList(w, r)
	case p == "read" && r.Method == http.MethodGet:
		s.wsRead(w, r)
	case p == "write" && r.Method == http.MethodPut:
		s.wsWrite(w, r)
	case p == "mkdir" && r.Method == http.MethodPost:
		s.wsMkdir(w, r)
	case p == "rename" && r.Method == http.MethodPost:
		s.wsRename(w, r)
	case p == "download" && r.Method == http.MethodGet:
		s.wsDownload(w, r)
	case p == "archive" && r.Method == http.MethodGet:
		s.wsArchive(w, r)
	case p == "upload" && r.Method == http.MethodPost:
		s.wsUpload(w, r)
	case p == "" && r.Method == http.MethodDelete:
		s.wsDelete(w, r)
	default:
		// DELETE with path as query uses p=="" above; also allow /api/workspace/delete
		if p == "delete" && r.Method == http.MethodPost {
			s.wsDelete(w, r)
			return
		}
		http.NotFound(w, r)
	}
}

func (s *Server) wsList(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	showHidden := r.URL.Query().Get("hidden") != "0"
	ents, err := s.WS.List(path, showHidden)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"path":    path,
		"entries": ents,
		"root":    s.WS.Root,
	})
}

func (s *Server) wsRead(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	content, err := s.WS.ReadText(path)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"path":    path,
		"content": content,
	})
}

func (s *Server) wsWrite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if err := s.WS.WriteText(body.Path, body.Content); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "path": body.Path})
}

func (s *Server) wsMkdir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if err := s.WS.Mkdir(body.Path); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok", "path": body.Path})
}

func (s *Server) wsRename(w http.ResponseWriter, r *http.Request) {
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if err := s.WS.Rename(body.From, body.To); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "from": body.From, "to": body.To})
}

func (s *Server) wsDelete(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" && r.Method == http.MethodPost {
		var body struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		path = body.Path
	}
	if path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	if err := s.WS.Delete(path); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "path": path})
}

func (s *Server) wsDownload(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	file, st, err := s.WS.OpenFile(path)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	defer file.Close()
	name := filepath.Base(path)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", st.Size()))
	_, _ = io.Copy(w, file)
}

func (s *Server) wsArchive(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "."
	}
	base := filepath.Base(path)
	if path == "." || path == "" {
		base = filepath.Base(s.WS.Root)
	}
	name := base + ".tar.gz"
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	if err := s.WS.WriteTarGz(path, w); err != nil {
		// headers may already be sent; best effort
		return
	}
}

func (s *Server) wsUpload(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir = "."
	}
	// limit body
	r.Body = http.MaxBytesReader(w, r.Body, workspacefs.MaxUploadBytes+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "multipart: "+err.Error(), http.StatusBadRequest)
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		// also try "files"
		files = r.MultipartForm.File["files"]
	}
	if len(files) == 0 {
		http.Error(w, "no files", http.StatusBadRequest)
		return
	}
	force := r.URL.Query().Get("force") == "1"
	var written []string
	for _, fh := range files {
		name := filepath.Base(fh.Filename)
		if name == "" || name == "." || name == ".." {
			continue
		}
		rel := name
		if dir != "" && dir != "." {
			rel = filepath.Join(dir, name)
		}
		if !force && s.WS.Exists(rel) {
			http.Error(w, "exists: "+rel+" (pass force=1 to overwrite)", http.StatusConflict)
			return
		}
		src, err := fh.Open()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		err = s.WS.WriteUpload(rel, src, fh.Size)
		src.Close()
		if err != nil {
			http.Error(w, err.Error(), statusFor(err))
			return
		}
		written = append(written, filepath.ToSlash(rel))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "files": written})
}

func statusFor(err error) int {
	if err == nil {
		return http.StatusOK
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "escapes"):
		return http.StatusForbidden
	case strings.Contains(msg, "not exist") || strings.Contains(msg, "no such"):
		return http.StatusNotFound
	case strings.Contains(msg, "too large") || strings.Contains(msg, "binary") || strings.Contains(msg, "not a directory") || strings.Contains(msg, "is a directory"):
		return http.StatusBadRequest
	default:
		return http.StatusBadRequest
	}
}
