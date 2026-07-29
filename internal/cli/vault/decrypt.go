package vaultcli

import (
	"crypto/rsa"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// runKVDecrypt reads a report produced by "vault kv export" and renders every
// payload it can open as Path/Key/Value rows through the output layer, so
// -f json/yaml/csv and -c all work. It never writes to a Vault: recovering a
// secret and re-injecting it are deliberately separate acts.
func runKVDecrypt(r io.Reader, priv *rsa.PrivateKey, o *output.Options, w io.Writer) error {
	var suite junitSuite
	if err := xml.NewDecoder(r).Decode(&suite); err != nil {
		return fmt.Errorf("parsing the JUnit report: %w", err)
	}

	rows := make([][]any, 0, len(suite.Cases))
	var decrypted int
	for _, tc := range suite.Cases {
		if strings.TrimSpace(tc.SystemOut) == "" {
			continue // skipped (empty) or failed case: nothing was encrypted
		}
		// The case name is the additional authenticated data, so a payload moved
		// to another case fails here rather than decrypting under a wrong path.
		plaintext, err := decryptPayload(priv, tc.Name, tc.SystemOut)
		if err != nil {
			return fmt.Errorf("%s: %w", tc.Name, err)
		}
		decrypted++

		// An ssl_certificates case holds one raw value; every other case holds the
		// whole secret as a JSON object.
		if tc.Classname == classKVSSL {
			path, key, _ := strings.Cut(tc.Name, ":")
			rows = append(rows, []any{path, key, string(plaintext)})
			continue
		}

		var data map[string]any
		if err := json.Unmarshal(plaintext, &data); err != nil {
			return fmt.Errorf("%s: decrypted payload is not a JSON secret: %w", tc.Name, err)
		}
		for _, k := range sortedKeys(data) {
			rows = append(rows, []any{tc.Name, k, data[k]})
		}
	}

	if decrypted == 0 {
		return fmt.Errorf("no encrypted payloads found in the report (is it a koc export?)")
	}
	return o.WriteList(w, output.Table{
		Columns: []string{"Path", "Key", "Value"},
		Rows:    rows,
	})
}

// sortedKeys returns a secret's field names in a stable order, so exports and
// decrypted output are reproducible.
func sortedKeys(data map[string]any) []string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
