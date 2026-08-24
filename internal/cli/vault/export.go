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
		// Same guard as "kv copy": rel is the source Vault's own listing, and it is
		// joined onto the exported base path.
		if err := vault.ValidateRelPath(rel); err != nil {
			return fmt.Errorf("listing %q returned an unsafe secret path %q: %w", base, rel, err)
		}
		cases, err := exportSecret(ctx, c, pub, mount, joinPath(base, rel))
		if err != nil {
			return err
		}
		suite.Cases = append(suite.Cases, cases...)
	}
	suite.tally()

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

// exportSecret reads the secret at path and renders it as JUnit test cases.
//
// An unreadable secret is a failed test case rather than a failed export: the
// report's whole job is to say which paths could and could not be read, so one
// missing secret must not cost the operator the other several hundred. Only an
// encryption failure — which would mean the recipient key is unusable — aborts.
func exportSecret(ctx context.Context, c *vault.Client, pub *rsa.PublicKey, mount, path string) ([]junitCase, error) {
	data, err := c.ReadKVDataAt(ctx, mount, path, 0)
	if err != nil {
		// Vault errors carry no secret material, so they are safe to embed.
		msg := err.Error()
		if errors.Is(err, vault.ErrNotFound) {
			msg = "secret not found or has no readable version"
		}
		return []junitCase{{
			Classname: classKV, Name: path, Time: caseTime,
			Failure: &junitMessage{Message: msg, Text: msg},
		}}, nil
	}
	return exportCases(pub, path, data)
}

// tally recounts the suite-level attributes GitLab reads from the cases now in
// the suite.
func (s *junitSuite) tally() {
	s.Tests, s.Failures, s.Skipped = len(s.Cases), 0, 0
	for _, tc := range s.Cases {
		switch {
		case tc.Failure != nil:
			s.Failures++
		case tc.Skipped != nil:
			s.Skipped++
		}
	}
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

// openExportOutput returns the writer for --output, and a closer. The export file
// ends up 0600: it is ciphertext, but there is no reason to make it readable to
// everyone on the runner.
//
// The mode is applied twice on purpose. O_CREATE's permission argument only takes
// effect when the file is created, so exporting over an already-existing
// world-readable file (a re-run in the same CI workspace) would otherwise keep
// its 0644 — the mode is therefore also enforced on the open handle, before
// anything is written to it.
func openExportOutput(path string) (io.Writer, func() error, error) {
	if path == "" || path == "-" {
		return os.Stdout, func() error { return nil }, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // G304: operator-supplied output path
	if err != nil {
		return nil, nil, fmt.Errorf("creating --output %q: %w", path, err)
	}
	if err := enforceFileMode(f, 0o600); err != nil {
		_ = f.Close()
		return nil, nil, fmt.Errorf("securing --output %q: %w", path, err)
	}
	return f, f.Close, nil
}

// enforceFileMode chmods an open file to mode. Only regular files are chmod-ed:
// --output may legitimately name a pipe or a device (/dev/stdout), where the mode
// is neither ours to change nor meaningful.
func enforceFileMode(f *os.File, mode os.FileMode) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() == mode {
		return nil
	}
	return f.Chmod(mode)
}
