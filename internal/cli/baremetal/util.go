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

// escapeJSONPointer escapes a single JSON-pointer reference token per RFC 6901:
// '~' becomes '~0' and '/' becomes '~1'. Apply it to user-supplied key segments
// before appending them to a JSON-pointer path prefix (e.g. "/properties/") so
// keys containing '/' or '~' address the intended member.
func escapeJSONPointer(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	s = strings.ReplaceAll(s, "/", "~1")
	return s
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
