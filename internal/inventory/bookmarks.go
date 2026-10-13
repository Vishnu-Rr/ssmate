package inventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/adrg/xdg"
)

// Bookmarks are names you chose for targets.
//
// This lives in the config directory rather than with the cache or the usage
// history, because unlike those it is authored by a person. Deleting a cache
// should be free; deleting something you wrote should not.
type Bookmarks struct {
	Version int               `json:"version"`
	Names   map[string]string `json:"names"` // alias -> target
}

const bookmarksVersion = 1

// BookmarksPath is where bookmarks live.
func BookmarksPath() (string, error) {
	p, err := xdg.ConfigFile(filepath.Join("ssmate", "bookmarks.json"))
	if err != nil {
		return "", fmt.Errorf("resolve bookmarks path: %w", err)
	}
	return p, nil
}

// LoadBookmarks reads bookmarks. A missing file is an empty set, not an error.
func LoadBookmarks(path string) (*Bookmarks, error) {
	b := &Bookmarks{Version: bookmarksVersion, Names: map[string]string{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return b, nil
		}
		return nil, fmt.Errorf("read bookmarks: %w", err)
	}
	if err := json.Unmarshal(raw, b); err != nil {
		// Unlike the cache, this file is hand-editable, so a syntax error is
		// worth reporting rather than silently discarding someone's work.
		return nil, fmt.Errorf("bookmarks file at %s is not valid JSON: %w", path, err)
	}
	if b.Names == nil {
		b.Names = map[string]string{}
	}
	return b, nil
}

// Set adds or replaces a bookmark.
func (b *Bookmarks) Set(alias, target string) error {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return fmt.Errorf("a bookmark needs a name")
	}
	if strings.ContainsAny(alias, " \t") {
		return fmt.Errorf("bookmark names cannot contain spaces: %q", alias)
	}
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("a bookmark needs a target")
	}
	if b.Names == nil {
		b.Names = map[string]string{}
	}
	b.Names[alias] = strings.TrimSpace(target)
	return nil
}

// Remove deletes a bookmark, reporting whether it existed.
func (b *Bookmarks) Remove(alias string) bool {
	if _, ok := b.Names[alias]; !ok {
		return false
	}
	delete(b.Names, alias)
	return true
}

// Lookup resolves an alias to its target.
func (b *Bookmarks) Lookup(alias string) (string, bool) {
	if b == nil {
		return "", false
	}
	t, ok := b.Names[alias]
	return t, ok
}

// Sorted lists bookmarks by name.
func (b *Bookmarks) Sorted() [][2]string {
	out := make([][2]string, 0, len(b.Names))
	for k, v := range b.Names {
		out = append(out, [2]string{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// Save writes bookmarks atomically.
func (b *Bookmarks) Save(path string) error {
	b.Version = bookmarksVersion
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("encode bookmarks: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bookmarks-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
