// Package fsutil holds small file helpers shared by every provider: atomic
// writes with private permissions and JSON round-trips that keep unknown keys.
package fsutil

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path through a temp file and rename, so a
// crash never leaves a half-written credential behind. When path is a
// symlink the link itself is replaced by a regular file.
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".dial-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// WriteJSON marshals v with two-space indentation and writes it atomically.
func WriteJSON(path string, v any, perm fs.FileMode) error {
	data, err := MarshalIndent(v)
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, data, perm)
}

// MarshalIndent is json.MarshalIndent without HTML escaping and with a
// trailing newline, matching how the CLIs write their own files.
func MarshalIndent(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ReadJSON decodes path into v. A missing file returns fs.ErrNotExist.
func ReadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// ReadJSONMap reads a JSON object as an ordered-agnostic raw map so callers
// can replace a single key and write everything else back untouched.
func ReadJSONMap(path string) (map[string]json.RawMessage, error) {
	m := map[string]json.RawMessage{}
	err := ReadJSON(path, &m)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	return m, err
}

// Exists reports whether path exists (following symlinks).
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// CopyFile copies src to dst atomically with the given permissions.
func CopyFile(src, dst string, perm fs.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return WriteFileAtomic(dst, data, perm)
}
