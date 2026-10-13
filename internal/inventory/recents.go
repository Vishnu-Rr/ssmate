package inventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/adrg/xdg"
)

// Recents ranks nodes you actually use.
//
// Ranking is by frecency rather than raw recency: a host you open every day
// should outrank one you opened once an hour ago. The weights below are
// zoxide's, which have been sanded down by a lot of real use.
type Recents struct {
	Version int                `json:"version"`
	Entries map[string]*Recent `json:"entries"`
}

// Recent is one node's usage record, keyed by instance ID.
type Recent struct {
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
}

const recentsVersion = 1

// maxRecents bounds the file. Beyond this the lowest-scoring entries are
// dropped, so a long-lived install cannot grow without limit.
const maxRecents = 500

// Score is the frecency weight: visit count, multiplied by how recent the
// last visit was.
func (r *Recent) Score(now time.Time) float64 {
	if r == nil || r.Count == 0 {
		return 0
	}
	age := now.Sub(r.Last)
	switch {
	case age < time.Hour:
		return float64(r.Count) * 4
	case age < 24*time.Hour:
		return float64(r.Count) * 2
	case age < 7*24*time.Hour:
		return float64(r.Count) / 2
	default:
		return float64(r.Count) / 4
	}
}

// RecentsPath is where usage lives. This is state, not cache: deleting the
// cache should not cost you your history.
func RecentsPath() (string, error) {
	p, err := xdg.StateFile(filepath.Join("ssmate", "recents.json"))
	if err != nil {
		return "", fmt.Errorf("resolve recents path: %w", err)
	}
	return p, nil
}

// LoadRecents reads usage. A missing or unreadable file is not an error:
// ranking is an enhancement, and losing it must never block a connection.
func LoadRecents(path string) *Recents {
	r := &Recents{Version: recentsVersion, Entries: map[string]*Recent{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		return r
	}
	var loaded Recents
	if err := json.Unmarshal(raw, &loaded); err != nil || loaded.Version != recentsVersion {
		return r
	}
	if loaded.Entries == nil {
		loaded.Entries = map[string]*Recent{}
	}
	return &loaded
}

// Record notes a use of one node.
func (r *Recents) Record(instanceID string, now time.Time) {
	if r.Entries == nil {
		r.Entries = map[string]*Recent{}
	}
	e := r.Entries[instanceID]
	if e == nil {
		e = &Recent{}
		r.Entries[instanceID] = e
	}
	e.Count++
	e.Last = now
}

// Save writes usage atomically, trimming to the highest-scoring entries.
func (r *Recents) Save(path string, now time.Time) error {
	r.Version = recentsVersion
	r.trim(now)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode recents: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".recents-*.json")
	if err != nil {
		return fmt.Errorf("create temp recents file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write recents: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (r *Recents) trim(now time.Time) {
	if len(r.Entries) <= maxRecents {
		return
	}
	type kv struct {
		id string
		s  float64
	}
	all := make([]kv, 0, len(r.Entries))
	for id, e := range r.Entries {
		all = append(all, kv{id, e.Score(now)})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].s > all[j].s })
	for _, e := range all[maxRecents:] {
		delete(r.Entries, e.id)
	}
}

// Sort orders nodes by usage, most-used first, with everything unused
// following in its original order.
//
// Only the used ones move. Reordering the whole list by some cleverness
// would make it unpredictable, and a picker you cannot predict is worse
// than an unsorted one.
func (r *Recents) Sort(nodes []Node, now time.Time) []Node {
	if r == nil || len(r.Entries) == 0 {
		return nodes
	}
	used := make([]Node, 0, len(r.Entries))
	rest := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if e, ok := r.Entries[n.ID]; ok && e.Score(now) > 0 {
			used = append(used, n)
			continue
		}
		rest = append(rest, n)
	}
	sort.SliceStable(used, func(i, j int) bool {
		return r.Entries[used[i].ID].Score(now) > r.Entries[used[j].ID].Score(now)
	})
	return append(used, rest...)
}
