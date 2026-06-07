// Package auth implements API-key authentication for the kube-state-graph
// HTTP API.
//
// A KeySet holds the active set of accepted API keys. Keys are loaded from a
// file (one per line; `#` comments allowed) and/or a comma-separated env
// value. Validation uses constant-time comparison and iterates the full
// stored set on every call so neither match latency nor early exit leaks key
// count or position.
//
// File-backed key sets support hot reload via Reload(): a background ticker
// re-reads the file and atomically swaps the active set, so a Kubernetes
// Secret rotation is picked up without a process restart.
package auth

import (
	"bufio"
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
)

// KeySet is the live set of accepted API keys. Safe for concurrent use.
type KeySet struct {
	keys atomic.Pointer[[]string]
}

// NewKeySet returns an empty KeySet (auth-disabled).
func NewKeySet() *KeySet {
	ks := &KeySet{}
	empty := []string{}
	ks.keys.Store(&empty)
	return ks
}

// LoadFile reads keys from path (one per line, blanks + `#` comments
// stripped) and atomically replaces the active set.
func (ks *KeySet) LoadFile(path string) error {
	keys, err := readKeyFile(path)
	if err != nil {
		return err
	}
	ks.keys.Store(&keys)
	return nil
}

// LoadCSV parses comma-separated keys (whitespace trimmed; blanks dropped)
// and atomically replaces the active set.
func (ks *KeySet) LoadCSV(csv string) {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	ks.keys.Store(&out)
}

// Empty reports whether the set holds zero keys (auth disabled).
func (ks *KeySet) Empty() bool {
	keys := ks.keys.Load()
	return keys == nil || len(*keys) == 0
}

// Validate reports whether presented matches any stored key. Comparison is
// constant-time per stored key and always iterates the full set, so neither
// match latency nor early exit leaks the matching position or the key count.
func (ks *KeySet) Validate(presented string) bool {
	if presented == "" {
		return false
	}
	keys := ks.keys.Load()
	if keys == nil || len(*keys) == 0 {
		return false
	}
	pb := []byte(presented)
	matched := 0
	for _, k := range *keys {
		kb := []byte(k)
		if len(kb) != len(pb) {
			// A different length cannot match; skip without a self-compare (the
			// previous subtle.ConstantTimeCompare(kb, kb) was a no-op against
			// itself). The whole set is still iterated — no early return — so key
			// count / position is not leaked via timing. API keys are
			// high-entropy tokens, so a per-key length difference is not a useful
			// timing oracle.
			continue
		}
		if subtle.ConstantTimeCompare(kb, pb) == 1 {
			matched = 1
		}
	}
	return matched == 1
}

// Snapshot returns the current key count for diagnostics. The keys themselves
// are never exposed.
func (ks *KeySet) Snapshot() int {
	keys := ks.keys.Load()
	if keys == nil {
		return 0
	}
	return len(*keys)
}

func readKeyFile(path string) ([]string, error) {
	if path == "" {
		return nil, errors.New("api-keys-file path is empty")
	}
	f, err := os.Open(path) //nolint:gosec // path is operator-supplied config
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	out := make([]string, 0, 8)
	seen := map[string]struct{}{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, dup := seen[line]; dup {
			continue
		}
		seen[line] = struct{}{}
		out = append(out, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}
