package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// SoulMaxBytes is the ADR-0013 Q10 cap (64 KiB).
const SoulMaxBytes = 64 * 1024

// SoulPath returns $MEMORY/soul.md
func (s *Store) SoulPath() string {
	if s == nil {
		return ""
	}
	return filepath.Join(s.Root, "soul.md")
}

// ReadSoul returns every-turn context text. Missing file ⇒ empty string, nil error.
func (s *Store) ReadSoul() (string, error) {
	if s == nil {
		return "", nil
	}
	path := s.SoulPath()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(b), nil
}

// WriteSoul writes soul.md. Empty soul removes the file (omit inject).
// Rejects content larger than SoulMaxBytes (by UTF-8 byte length of the string).
func (s *Store) WriteSoul(content string) error {
	if s == nil {
		return fmt.Errorf("memory store not configured")
	}
	if utf8.RuneCountInString(content) > SoulMaxBytes {
		// Cap by runes approximately; also enforce byte cap
		return fmt.Errorf("soul exceeds max size (%d characters)", SoulMaxBytes)
	}
	if len(content) > SoulMaxBytes {
		return fmt.Errorf("soul exceeds max size (%d bytes)", SoulMaxBytes)
	}
	path := s.SoulPath()
	if strings.TrimSpace(content) == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return err
	}
	return atomicWrite(path, []byte(content))
}
