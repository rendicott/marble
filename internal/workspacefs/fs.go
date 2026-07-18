package workspacefs

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxEditBytes is the max size for text read/edit in the UI (1 MiB).
	MaxEditBytes = 1 << 20
	// MaxUploadBytes is max size per uploaded file (50 MiB).
	MaxUploadBytes = 50 << 20
	// MaxArchiveFiles and MaxArchiveBytes guard tar.gz generation.
	MaxArchiveFiles = 10000
	MaxArchiveBytes = 200 << 20
)

// FS is a workspace-jailed filesystem.
type FS struct {
	Root string // absolute workspace root
}

// New creates an FS for an absolute workspace directory.
func New(root string) (*FS, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("workspace is not a directory")
	}
	return &FS{Root: abs}, nil
}

// Resolve maps a relative path (or ".") into an absolute path under Root.
// Rejects escapes. Symlink targets must also stay under Root when eval.
func (f *FS) Resolve(rel string) (abs string, err error) {
	if rel == "" || rel == "." {
		return f.Root, nil
	}
	// Allow absolute only if under root
	if filepath.IsAbs(rel) {
		abs, err = filepath.Abs(rel)
		if err != nil {
			return "", err
		}
	} else {
		clean := filepath.Clean(rel)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			return "", fmt.Errorf("path escapes workspace")
		}
		abs = filepath.Join(f.Root, clean)
		abs, err = filepath.Abs(abs)
		if err != nil {
			return "", err
		}
	}
	if !f.underRoot(abs) {
		return "", fmt.Errorf("path escapes workspace")
	}
	// If path exists and is symlink, ensure evaluated path stays in jail
	if fi, err := os.Lstat(abs); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return "", fmt.Errorf("symlink: %w", err)
		}
		target, err = filepath.Abs(target)
		if err != nil {
			return "", err
		}
		if !f.underRoot(target) {
			return "", fmt.Errorf("symlink escapes workspace")
		}
	}
	return abs, nil
}

func (f *FS) underRoot(abs string) bool {
	if abs == f.Root {
		return true
	}
	prefix := f.Root + string(os.PathSeparator)
	return strings.HasPrefix(abs, prefix)
}

// Rel returns path relative to workspace root.
func (f *FS) Rel(abs string) (string, error) {
	rel, err := filepath.Rel(f.Root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes workspace")
	}
	return rel, nil
}

// Entry is a directory listing row.
type Entry struct {
	Name    string `json:"name"`
	Path    string `json:"path"` // relative
	Type    string `json:"type"` // file | dir | symlink
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
	IsText  bool   `json:"is_text"`
}

// List returns entries in dirRel (relative). showHidden includes dotfiles.
func (f *FS) List(dirRel string, showHidden bool) ([]Entry, error) {
	abs, err := f.Resolve(dirRel)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("not a directory")
	}
	// Ensure resolved dir is still in jail after symlink follow of Stat
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		real, _ = filepath.Abs(real)
		if !f.underRoot(real) {
			return nil, fmt.Errorf("path escapes workspace")
		}
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(ents))
	for _, e := range ents {
		name := e.Name()
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		rel := name
		if dirRel != "" && dirRel != "." {
			rel = filepath.Join(dirRel, name)
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		typ := "file"
		isText := false
		if e.IsDir() {
			typ = "dir"
		} else if info.Mode()&os.ModeSymlink != 0 {
			typ = "symlink"
		} else {
			isText = f.looksText(filepath.Join(abs, name), info.Size())
		}
		out = append(out, Entry{
			Name:    name,
			Path:    filepath.ToSlash(rel),
			Type:    typ,
			Size:    info.Size(),
			ModTime: info.ModTime().UTC().Format(time.RFC3339),
			IsText:  isText,
		})
	}
	return out, nil
}

// ReadText reads a text file up to MaxEditBytes.
func (f *FS) ReadText(rel string) (content string, err error) {
	abs, err := f.Resolve(rel)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("is a directory")
	}
	if st.Size() > MaxEditBytes {
		return "", fmt.Errorf("file too large to edit (max %d bytes)", MaxEditBytes)
	}
	if !f.looksText(abs, st.Size()) {
		return "", fmt.Errorf("binary file")
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		return "", fmt.Errorf("not valid UTF-8")
	}
	return string(b), nil
}

// WriteText writes full file contents (creates parents).
func (f *FS) WriteText(rel, content string) error {
	if len(content) > MaxEditBytes {
		return fmt.Errorf("content too large (max %d bytes)", MaxEditBytes)
	}
	abs, err := f.Resolve(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, []byte(content), 0o644)
}

// Mkdir creates a directory.
func (f *FS) Mkdir(rel string) error {
	abs, err := f.Resolve(rel)
	if err != nil {
		return err
	}
	return os.MkdirAll(abs, 0o755)
}

// Rename moves from → to within workspace.
func (f *FS) Rename(from, to string) error {
	a, err := f.Resolve(from)
	if err != nil {
		return err
	}
	b, err := f.Resolve(to)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(b), 0o755); err != nil {
		return err
	}
	return os.Rename(a, b)
}

// Delete removes a file or directory (recursive for dirs).
func (f *FS) Delete(rel string) error {
	abs, err := f.Resolve(rel)
	if err != nil {
		return err
	}
	if abs == f.Root {
		return fmt.Errorf("cannot delete workspace root")
	}
	return os.RemoveAll(abs)
}

// OpenFile opens for streaming download.
func (f *FS) OpenFile(rel string) (*os.File, os.FileInfo, error) {
	abs, err := f.Resolve(rel)
	if err != nil {
		return nil, nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, nil, err
	}
	if st.IsDir() {
		return nil, nil, fmt.Errorf("is a directory")
	}
	file, err := os.Open(abs)
	return file, st, err
}

// WriteUpload writes an uploaded file stream to rel path.
func (f *FS) WriteUpload(rel string, r io.Reader, size int64) error {
	if size > MaxUploadBytes {
		return fmt.Errorf("file too large (max %d bytes)", MaxUploadBytes)
	}
	abs, err := f.Resolve(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	tmp := abs + ".upload-tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	limited := io.LimitReader(r, MaxUploadBytes+1)
	n, err := io.Copy(out, limited)
	cerr := out.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if cerr != nil {
		_ = os.Remove(tmp)
		return cerr
	}
	if n > MaxUploadBytes {
		_ = os.Remove(tmp)
		return fmt.Errorf("file too large (max %d bytes)", MaxUploadBytes)
	}
	return os.Rename(tmp, abs)
}

// WriteTarGz streams a gzipped tar of dirRel to w.
func (f *FS) WriteTarGz(dirRel string, w io.Writer) error {
	abs, err := f.Resolve(dirRel)
	if err != nil {
		return err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("not a directory")
	}

	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	var files int
	var bytes int64
	baseName := filepath.Base(abs)
	if dirRel == "" || dirRel == "." {
		baseName = filepath.Base(f.Root)
	}

	err = filepath.Walk(abs, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		// jail check
		if !f.underRoot(path) {
			return fmt.Errorf("path escapes workspace")
		}
		// skip symlink escape
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(path)
			if err != nil || !f.underRoot(target) {
				return nil // skip unsafe symlink
			}
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Join(baseName, rel))
		if rel == "." {
			name = baseName + "/"
		}

		if info.IsDir() {
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = name
			if !strings.HasSuffix(hdr.Name, "/") {
				hdr.Name += "/"
			}
			return tw.WriteHeader(hdr)
		}

		files++
		if files > MaxArchiveFiles {
			return fmt.Errorf("archive too many files (max %d)", MaxArchiveFiles)
		}
		bytes += info.Size()
		if bytes > MaxArchiveBytes {
			return fmt.Errorf("archive too large (max %d bytes)", MaxArchiveBytes)
		}

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = name
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, file)
		file.Close()
		return copyErr
	})
	return err
}

func (f *FS) looksText(abs string, size int64) bool {
	if size == 0 {
		return true
	}
	if size > MaxEditBytes {
		return false
	}
	file, err := os.Open(abs)
	if err != nil {
		return false
	}
	defer file.Close()
	buf := make([]byte, 8192)
	n, _ := file.Read(buf)
	buf = buf[:n]
	if n == 0 {
		return true
	}
	for _, b := range buf {
		if b == 0 {
			return false
		}
	}
	return utf8.Valid(buf)
}

// Exists reports whether rel exists.
func (f *FS) Exists(rel string) bool {
	abs, err := f.Resolve(rel)
	if err != nil {
		return false
	}
	_, err = os.Stat(abs)
	return err == nil
}

// IsDir reports whether rel is a directory.
func (f *FS) IsDir(rel string) bool {
	abs, err := f.Resolve(rel)
	if err != nil {
		return false
	}
	st, err := os.Stat(abs)
	return err == nil && st.IsDir()
}
