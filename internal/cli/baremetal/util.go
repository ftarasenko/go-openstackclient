package baremetal

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/output"
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

// addFieldsAliases registers --fields and --field on cmd, python-ironicclient's
// spelling of the global -c/--column selector. History shows operators reaching
// for the ironic spelling on `baremetal node list`/`show`, where koc only
// accepted -c.
//
// The values are folded into o.Columns in PreRunE, so the output layer remains
// the single place column selection is implemented. StringSliceVar (not
// StringArrayVar) is used because the ironic CLI accepts both a comma-separated
// list and repetition.
func addFieldsAliases(cmd *cobra.Command, o *output.Options) {
	var fields, field []string
	fl := cmd.Flags()
	fl.StringSliceVar(&fields, "fields", nil,
		"field(s) to include; comma-separated or repeated (ironic CLI spelling of -c/--column)")
	fl.StringSliceVar(&field, "field", nil, "alias of --fields")
	cmd.PreRunE = func(_ *cobra.Command, _ []string) error {
		o.Columns = append(o.Columns, fields...)
		o.Columns = append(o.Columns, field...)
		return nil
	}
}
