package baremetal

import (
	"fmt"
	"strings"
)

// parseKeyVal splits a "key=value" string into its two halves. The value may
// itself contain '=' signs; only the first is treated as the separator.
func parseKeyVal(s string) (string, string, error) {
	i := strings.Index(s, "=")
	if i < 0 {
		return "", "", fmt.Errorf("expected key=value, got %q", s)
	}
	key := strings.TrimSpace(s[:i])
	if key == "" {
		return "", "", fmt.Errorf("empty key in %q", s)
	}
	return key, s[i+1:], nil
}

// capResults enforces --limit as a hard result cap for ironic list commands,
// where the API treats "limit" only as a page size and AllPages fetches every
// page. A non-positive limit means "no cap".
func capResults[T any](items []T, limit int) []T {
	if limit > 0 && len(items) > limit {
		return items[:limit]
	}
	return items
}

// parseKeyValMap turns a slice of "key=value" flag values into a map.
func parseKeyValMap(pairs []string) (map[string]any, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	m := make(map[string]any, len(pairs))
	for _, p := range pairs {
		k, v, err := parseKeyVal(p)
		if err != nil {
			return nil, err
		}
		m[k] = v
	}
	return m, nil
}
