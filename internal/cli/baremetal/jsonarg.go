package baremetal

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// readJSONArg reads a structured flag value the way python-ironicclient's
// utils.handle_json_arg does: '-' reads JSON from standard input, the name of
// an existing file reads that file as JSON or YAML, and anything else must be
// inline JSON.
func readJSONArg(arg, what string, stdin io.Reader) (any, error) {
	if arg == "-" {
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("reading %s from standard input: %w", what, err)
		}
		return decodeJSONArg(raw, what)
	}
	if fi, err := os.Stat(arg); err == nil && fi.Mode().IsRegular() {
		raw, err := os.ReadFile(arg) //nolint:gosec // G304: operator-supplied file path, as upstream
		if err != nil {
			return nil, fmt.Errorf("reading %s file %q: %w", what, arg, err)
		}
		var v any
		if err := yaml.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("cannot get JSON/YAML %s from file %q: %w", what, arg, err)
		}
		return v, nil
	}
	return decodeJSONArg([]byte(arg), what)
}

func decodeJSONArg(raw []byte, what string) (any, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("%s: value is not a file and cannot be parsed as JSON: %w", what, err)
	}
	return v, nil
}
