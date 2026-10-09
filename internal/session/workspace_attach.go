package session

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rendicott/marble/internal/model"
)

const workspacePathHeader = "[harness] These attachments are files in the workspace. Use file_read, shell, or other workspace tools to copy, hash, or archive them. Do not look in the Marble memory directory."

const workspaceFailHeader = "[harness] These attachments were not copied into the workspace. There is no file to archive. Do not say the original was saved."

// safeExtRe is a filename extension we will keep. Anything else is replaced
// from the MIME type so a name cannot smuggle a path.
var safeExtRe = regexp.MustCompile(`^\.[a-z0-9]{1,8}$`)

// chatFile is one user-upload attachment to copy into the workspace.
type chatFile struct {
	ID, Name, MIME string
	Size           int64
	Data           []byte
	Err            error
}

// copyUserUploads reads staged user-upload bytes and copies them under
// <workspace>/.attachments/<session>/<message>-N.ext. The canonical bytes
// stay in the memory directory. A failed copy is reported in the note and
// does not fail the send.
func (r *Runner) copyUserUploads(sessionID, messageID string, uis []UIAttachment) string {
	if len(uis) == 0 {
		return ""
	}
	files := make([]chatFile, len(uis))
	for i, u := range uis {
		files[i] = chatFile{ID: u.ID, Name: u.Name, MIME: u.MIME, Size: u.Size}
		data, err := r.readUploadBytes(sessionID, u.ID)
		if err != nil {
			files[i].Err = err
			continue
		}
		files[i].Data = data
		if files[i].Size == 0 {
			files[i].Size = int64(len(data))
		}
	}
	ws := ""
	if r != nil {
		ws = r.Cfg.Workspace
	}
	paths, note := copyChatAttachments(ws, sessionID, messageID, files)
	for i := range uis {
		if i < len(paths) {
			uis[i].WorkspacePath = paths[i]
		}
	}
	return note
}

func (r *Runner) readUploadBytes(sessionID, attID string) ([]byte, error) {
	if r == nil || r.Reg == nil || r.Reg.sqldb == nil {
		return nil, fmt.Errorf("no store")
	}
	return r.Reg.sqldb.ReadAttachmentBytes(sessionID, attID)
}

// copyChatAttachments writes files into the workspace and returns the
// workspace-relative path for each (empty when that file was not written)
// plus the harness note for the model.
func copyChatAttachments(workspace, sessionID, messageID string, files []chatFile) ([]string, string) {
	paths := make([]string, len(files))
	if len(files) == 0 {
		return paths, ""
	}
	var okLines, badLines []string
	componentsOK := safeAttachComponent(sessionID) && safeAttachComponent(messageID)
	var wsReal string
	var wsErr error
	if componentsOK {
		wsReal, wsErr = workspaceRoot(workspace)
	}
	for i, f := range files {
		name := oneLineName(f.Name)
		if name == "" {
			name = oneLineName(f.ID)
		}
		fail := func(err error) {
			badLines = append(badLines, fmt.Sprintf("- %s (attachment_id=%s) was not copied: %s", name, oneLineName(f.ID), attachErrText(err)))
		}
		if !componentsOK {
			fail(fmt.Errorf("invalid session or message id"))
			continue
		}
		if wsErr != nil {
			fail(wsErr)
			continue
		}
		if f.Err != nil {
			fail(f.Err)
			continue
		}
		if len(f.Data) == 0 {
			fail(fmt.Errorf("empty attachment"))
			continue
		}
		ext := attachmentFileExt(f.Name, f.MIME)
		rel := fmt.Sprintf(".attachments/%s/%s-%d%s", sessionID, messageID, i+1, ext)
		if err := writeUnderWorkspace(wsReal, rel, f.Data); err != nil {
			fail(err)
			continue
		}
		paths[i] = rel
		size := f.Size
		if size <= 0 {
			size = int64(len(f.Data))
		}
		okLines = append(okLines, formatWorkspacePathLine(rel, name, f.ID, size))
	}
	return paths, joinAttachNote(okLines, badLines)
}

func workspaceRoot(workspace string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return "", fmt.Errorf("no workspace")
	}
	real, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", fmt.Errorf("no workspace")
	}
	st, err := os.Stat(real)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("workspace is not a directory")
	}
	return real, nil
}

// safeAttachComponent accepts session and message ids used as path segments.
// Dots are rejected, so ".." cannot appear.
func safeAttachComponent(s string) bool {
	if s == "" || len(s) > 80 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func attachmentFileExt(name, mime string) string {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	if safeExtRe.MatchString(ext) {
		return ext
	}
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "text/plain":
		return ".txt"
	case "text/markdown", "text/x-markdown":
		return ".md"
	case "text/csv":
		return ".csv"
	case "text/html":
		return ".html"
	case "application/json", "text/json":
		return ".json"
	default:
		return ".bin"
	}
}

func formatWorkspacePathLine(rel, name, id string, size int64) string {
	name = oneLineName(name)
	if name == "" {
		name = oneLineName(id)
	}
	return fmt.Sprintf("- %s (original %s, %d bytes, attachment_id=%s)", rel, name, size, oneLineName(id))
}

func oneLineName(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ", "/", " ", "\\", " ").Replace(s)
	return strings.TrimSpace(s)
}

func joinAttachNote(okLines, badLines []string) string {
	var b strings.Builder
	if len(okLines) > 0 {
		b.WriteString(workspacePathHeader)
		b.WriteByte('\n')
		for _, ln := range okLines {
			b.WriteString(ln)
			b.WriteByte('\n')
		}
	}
	if len(badLines) > 0 {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(workspaceFailHeader)
		b.WriteByte('\n')
		for _, ln := range badLines {
			b.WriteString(ln)
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// workspacePathNote is the reload form of the success half of the harness note.
func workspacePathNote(atts []UIAttachment) string {
	var lines []string
	for _, a := range atts {
		if strings.TrimSpace(a.WorkspacePath) == "" {
			continue
		}
		lines = append(lines, formatWorkspacePathLine(a.WorkspacePath, a.Name, a.ID, a.Size))
	}
	return joinAttachNote(lines, nil)
}

func appendHarnessNote(c model.Content, note string) model.Content {
	note = strings.TrimSpace(note)
	if note == "" {
		return c
	}
	if len(c.Parts) == 0 {
		var parts []model.ContentPart
		if t := strings.TrimSpace(c.Text); t != "" {
			parts = append(parts, model.ContentPart{Type: "text", Text: t})
		}
		parts = append(parts, model.ContentPart{Type: "text", Text: note})
		return model.ContentFromParts(parts)
	}
	c.Parts = append(append([]model.ContentPart{}, c.Parts...), model.ContentPart{Type: "text", Text: note})
	return c
}

// attachErrText is safe to show the model. It never includes a filesystem path,
// so a read of the memory directory cannot leak that location into the transcript.
func attachErrText(err error) string {
	if err == nil {
		return "could not write the workspace file"
	}
	msg := err.Error()
	for _, ok := range []string{
		"invalid session or message id",
		"no workspace",
		"workspace is not a directory",
		"empty attachment",
		"attachment path escapes workspace",
		"attachment path is a symlink",
		"attachment path is not a directory",
		"attachment path is a directory",
		"attachment file already exists",
		"no store",
		"attachment not found",
	} {
		if strings.Contains(msg, ok) {
			return ok
		}
	}
	if os.IsPermission(err) || strings.Contains(strings.ToLower(msg), "permission denied") {
		return "permission denied"
	}
	if os.IsNotExist(err) || strings.Contains(strings.ToLower(msg), "no such file") {
		return "attachment not found"
	}
	return "could not write the workspace file"
}

// writeUnderWorkspace creates rel under an already-resolved workspace root.
// An existing symlink that leaves the workspace is refused before any create.
func writeUnderWorkspace(root, rel string, data []byte) error {
	rel = filepath.ToSlash(rel)
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
		return fmt.Errorf("attachment path escapes workspace")
	}
	parts := strings.Split(rel, "/")
	cur := root
	for i, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `/\`) {
			return fmt.Errorf("attachment path escapes workspace")
		}
		next := filepath.Join(cur, part)
		last := i == len(parts)-1
		fi, err := os.Lstat(next)
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			if last {
				return writeNewFile(next, data)
			}
			if err := os.Mkdir(next, 0o755); err != nil {
				return err
			}
			if err := os.Chmod(next, 0o755); err != nil {
				return err
			}
			cur = next
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			if last {
				return fmt.Errorf("attachment path is a symlink")
			}
			resolved, err := filepath.EvalSymlinks(next)
			if err != nil {
				return err
			}
			if !pathInside(root, resolved) {
				return fmt.Errorf("attachment path escapes workspace")
			}
			st, err := os.Stat(resolved)
			if err != nil || !st.IsDir() {
				return fmt.Errorf("attachment path is not a directory")
			}
			cur = resolved
			continue
		}
		if last {
			if fi.IsDir() {
				return fmt.Errorf("attachment path is a directory")
			}
			return fmt.Errorf("attachment file already exists")
		}
		if !fi.IsDir() {
			return fmt.Errorf("attachment path is not a directory")
		}
		cur = next
	}
	return fmt.Errorf("attachment path escapes workspace")
}

func pathInside(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func writeNewFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("attachment file already exists")
		}
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(path)
		if werr != nil {
			return werr
		}
		return cerr
	}
	return os.Chmod(path, 0o644)
}
