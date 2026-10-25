package state

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"filippo.io/age"

	"github.com/rendicott/marble/internal/config"
)

func normalizeSecrets(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "omit", nil
	}
	switch s {
	case "omit", "age":
		return s, nil
	case "include":
		return "", fmt.Errorf("there is no --secrets=include")
	default:
		return "", fmt.Errorf("secrets must be omit or age, got %q", s)
	}
}

func secretNames(memory string) ([]string, error) {
	m, err := config.ParseEnvFile(filepath.Join(memory, config.RelMemoryEnv))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, nil
}

func readEnvFile(memory string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(memory, config.RelMemoryEnv))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", config.RelMemoryEnv, err)
	}
	return b, nil
}

func parseRecipients(ss []string) ([]age.Recipient, error) {
	if len(ss) == 0 {
		return nil, fmt.Errorf("--secrets=age requires --age-recipient")
	}
	out := make([]age.Recipient, 0, len(ss))
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, fmt.Errorf("empty --age-recipient")
		}
		r, err := age.ParseX25519Recipient(s)
		if err != nil {
			return nil, fmt.Errorf("age recipient: %w", err)
		}
		out = append(out, r)
	}
	return out, nil
}

func encryptEnv(path string, plain []byte, recs []age.Recipient) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	w, err := age.Encrypt(f, recs...)
	if err != nil {
		return err
	}
	if _, err := w.Write(plain); err != nil {
		_ = w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	ok = true
	return nil
}

func decryptAge(agePath, identPath string) ([]byte, error) {
	idf, err := os.Open(identPath)
	if err != nil {
		return nil, fmt.Errorf("age identity: %w", err)
	}
	defer idf.Close()
	ids, err := age.ParseIdentities(idf)
	if err != nil {
		return nil, fmt.Errorf("age identity: %w", err)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("age identity %s contained no keys", identPath)
	}
	f, err := os.Open(agePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r, err := age.Decrypt(f, ids...)
	if err != nil {
		return nil, fmt.Errorf("decrypt secrets.age: %w", err)
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("decrypt secrets.age: %w", err)
	}
	return plain, nil
}

func writeSecretEnv(path string, plain []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, plain, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Chmod(path, 0o600)
}
