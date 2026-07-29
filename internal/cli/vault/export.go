package vaultcli

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ftarasenko/go-openstackclient/internal/vault"
)

// The JUnit document mirrors the report vault-helper.py's --junit-save produced,
// so GitLab renders the same per-secret view (artifacts:reports:junit): which
// paths exist, which were empty, which could not be read. The difference is that
// every <system-out> holds a koc envelope instead of the secret in cleartext —
// those reports are published as artifacts and were at one point copied into
// GitLab Pages, so plaintext there is readable by anyone with project access.
//
// Secret *paths* remain visible: they are what makes the report useful, and they
// are authenticated (they are the GCM additional data), so a payload cannot be
// swapped between test cases without detection.
type junitSuite struct {
	XMLName  xml.Name    `xml:"testsuite"`
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Classname string        `xml:"classname,attr"`
	Name      string        `xml:"name,attr"`
	Time      string        `xml:"time,attr"`
	Skipped   *junitMessage `xml:"skipped,omitempty"`
	Failure   *junitMessage `xml:"failure,omitempty"`
	SystemOut string        `xml:"system-out,omitempty"`
}

type junitMessage struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

// Classnames distinguish a whole-secret payload from the per-key expansion of an
// ssl_certificates secret, matching vault-helper.py's two classnames.
const (
	classKV    = "vault.kv"
	classKVSSL = "vault.kv.ssl"
	// caseTime is a placeholder duration: GitLab expects the attribute, and this
	// is not a timing report.
	caseTime = "0.001"
	// sslCertsSecret is expanded one test case per key, so each certificate is
	// individually visible (and individually encrypted) in the report.
	sslCertsSecret = "ssl_certificates"
)

// exportFlags holds the flags of "vault kv export".
type exportFlags struct {
	recipient string
	output    string
}

// runKVExport walks the subtree under base, encrypts every secret to pub, and
// writes the JUnit report to w. There is no plaintext mode: the recipient key is
// required by the caller, so an export can never emit readable secrets.
func runKVExport(ctx context.Context, c *vault.Client, pub *rsa.PublicKey, base string, w io.Writer) error {
	mount := c.KVMount()
	rels, err := c.WalkKV(ctx, mount, base)
	if err != nil {
		return fmt.Errorf("listing %q: %w", base, err)
	}
	if len(rels) == 0 {
		// A leaf secret: export just it, mirroring "copy -r" on a leaf.
		rels = []string{""}
	}

	suite := junitSuite{Name: "vault:" + base}
	for _, rel := range rels {
		path := joinPath(base, rel)

		data, err := c.ReadKVDataAt(ctx, mount, path, 0)
		if err != nil {
			// Vault errors carry no secret material, so they are safe to embed.
			msg := err.Error()
			if errors.Is(err, vault.ErrNotFound) {
				msg = "secret not found or has no readable version"
			}
			suite.Cases = append(suite.Cases, junitCase{
				Classname: classKV, Name: path, Time: caseTime,
				Failure: &junitMessage{Message: msg, Text: msg},
			})
			continue
		}

		cases, err := exportCases(pub, path, data)
		if err != nil {
			return err
		}
		suite.Cases = append(suite.Cases, cases...)
	}

	for _, tc := range suite.Cases {
		switch {
		case tc.Failure != nil:
			suite.Failures++
		case tc.Skipped != nil:
			suite.Skipped++
		}
	}
	suite.Tests = len(suite.Cases)

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(suite); err != nil {
		return fmt.Errorf("encoding the JUnit report: %w", err)
	}
	if _, err := io.WriteString(w, "\n"); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "%s: exported %d test case(s) under %s (%d unreadable, %d empty), encrypted to the recipient\n",
		c.Addr(), suite.Tests, base, suite.Failures, suite.Skipped)
	return nil
}

// exportCases renders one secret as test cases: normally a single case holding
// the whole secret as encrypted JSON, but an ssl_certificates secret is expanded
// one case per key so each certificate is separately visible.
func exportCases(pub *rsa.PublicKey, path string, data map[string]any) ([]junitCase, error) {
	if len(data) == 0 {
		return []junitCase{{
			Classname: classKV, Name: path, Time: caseTime,
			Skipped: &junitMessage{Message: "empty secret"},
		}}, nil
	}

	if strings.HasSuffix(path, "/"+sslCertsSecret) || path == sslCertsSecret {
		cases := make([]junitCase, 0, len(data))
		for _, k := range sortedKeys(data) {
			name := path + ":" + k
			tc := junitCase{Classname: classKVSSL, Name: name, Time: caseTime}
			s, _ := data[k].(string)
			if s == "" {
				tc.Skipped = &junitMessage{Message: "empty value"}
				cases = append(cases, tc)
				continue
			}
			payload, err := encryptPayload(pub, name, []byte(s))
			if err != nil {
				return nil, fmt.Errorf("encrypting %s: %w", name, err)
			}
			tc.SystemOut = payload
			cases = append(cases, tc)
		}
		return cases, nil
	}

	plaintext, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding secret %q: %w", path, err)
	}
	payload, err := encryptPayload(pub, path, plaintext)
	if err != nil {
		return nil, fmt.Errorf("encrypting %s: %w", path, err)
	}
	return []junitCase{{
		Classname: classKV, Name: path, Time: caseTime, SystemOut: payload,
	}}, nil
}

// openExportOutput returns the writer for --output, and a closer. An export file
// is created 0600: it is ciphertext, but there is no reason to make it readable
// to everyone on the runner.
func openExportOutput(path string) (io.Writer, func() error, error) {
	if path == "" || path == "-" {
		return os.Stdout, func() error { return nil }, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // G304: operator-supplied output path
	if err != nil {
		return nil, nil, fmt.Errorf("creating --output %q: %w", path, err)
	}
	return f, f.Close, nil
}
