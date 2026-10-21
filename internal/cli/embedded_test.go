package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/ftarasenko/go-openstackclient/internal/kochome"
)

// fileFlagRe spots a flag (by name or help text) or a command (by usage line)
// that reads or writes a local file.
// Lower-case on purpose: upper-case PATH and FILE_TYPE are octavia L7 rule
// types, not local files.
var fileFlagRe = regexp.MustCompile(`\b(file|files|path|paths|stdin|directory)\b|(?i:standard input)|\bPEM\b|<file|\bfile>`)

// notFileFlags are matches of fileFlagRe that touch no local file, with why.
var notFileFlags = map[string]string{
	"image create --progress":                      "progress of an upload whose --file is refused",
	"image create --size":                          "a byte count; the data comes from the refused --file or a URL",
	"keypair show --public-key":                    "prints the public key; writes no file",
	"loadbalancer healthmonitor create --url-path": "an HTTP path on the member, not a local file",
	"loadbalancer healthmonitor set --url-path":    "an HTTP path on the member, not a local file",
}

// embeddedTree builds the command tree under the embedded profile.
func embeddedTree(t *testing.T) *cobra.Command {
	t.Helper()
	t.Setenv(kochome.EnvProfile, kochome.ProfileEmbedded)
	return NewRootCommand("test")
}

func pathOf(c *cobra.Command) string {
	return strings.TrimSpace(strings.TrimPrefix(c.CommandPath(), "koc"))
}

func commandRefused(path string) bool {
	for _, r := range embeddedCommandRefusals {
		if path == r.path || strings.HasPrefix(path, r.path+" ") {
			return true
		}
	}
	return false
}

func flagRefused(path, flag string) bool {
	for _, r := range embeddedFlagRefusals {
		if (r.path == "" || r.path == path) && r.flag == flag {
			return true
		}
	}
	return false
}

// TestEmbeddedFileFlags walks the whole tree: every flag or argument that
// names a local file must be refused by the table (or be listed in
// notFileFlags with the reason it is not one), so a new file flag cannot slip
// into the console terminal unnoticed.
func TestEmbeddedFileFlags(t *testing.T) {
	root := embeddedTree(t)
	root.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		if fileFlagRe.MatchString(f.Name+" "+f.Usage) && !flagRefused("", f.Name) {
			t.Errorf("global --%s looks like a local file (%q) but the embedded profile does not refuse it", f.Name, f.Usage)
		}
	})
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		path := pathOf(c)
		if c.Hidden || commandRefused(path) {
			return
		}
		if c.Runnable() && fileFlagRe.MatchString(c.Use) {
			t.Errorf("%q takes a file argument (%q) but the embedded profile does not refuse it", path, c.Use)
		}
		c.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
			key := path + " --" + f.Name
			if !fileFlagRe.MatchString(f.Name+" "+f.Usage) || flagRefused(path, f.Name) {
				return
			}
			if _, ok := notFileFlags[key]; !ok {
				t.Errorf("%s looks like a local file (%q) but the embedded profile does not refuse it; "+
					"add it to embeddedFlagRefusals, or to notFileFlags with the reason it is not one", key, f.Usage)
			}
		})
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

// TestEmbeddedTableIsCurrent: every entry names a command and flag that
// exist, so a rename cannot leave a refusal guarding nothing.
func TestEmbeddedTableIsCurrent(t *testing.T) {
	root := embeddedTree(t)
	find := func(path string) *cobra.Command {
		c, rest, err := root.Find(strings.Fields(path))
		if err != nil || len(rest) > 0 || pathOf(c) != path {
			return nil
		}
		return c
	}
	for _, r := range embeddedCommandRefusals {
		if find(r.path) == nil {
			t.Errorf("embeddedCommandRefusals: no command %q", r.path)
		}
	}
	for _, r := range embeddedFlagRefusals {
		c := root
		if r.path != "" {
			if c = find(r.path); c == nil {
				t.Errorf("embeddedFlagRefusals: no command %q", r.path)
				continue
			}
		}
		if c.Flags().Lookup(r.flag) == nil && root.PersistentFlags().Lookup(r.flag) == nil {
			t.Errorf("embeddedFlagRefusals: %q has no flag --%s", r.path, r.flag)
		}
	}
	for key := range notFileFlags {
		path, flag, _ := strings.Cut(key, " --")
		if c := find(path); c == nil || c.Flags().Lookup(flag) == nil {
			t.Errorf("notFileFlags: no %q", key)
		}
	}
}

// runEmbedded runs one invocation under the embedded profile with no
// credentials at all: a refusal must come before any authentication.
func runEmbedded(t *testing.T, args ...string) error {
	t.Helper()
	clearOSEnv(t)
	t.Setenv("OS_REGION_NAME", "RegionOne")
	t.Setenv("OS_PROJECT_ID", "p1")
	t.Setenv(kochome.EnvProfile, kochome.ProfileEmbedded)
	t.Setenv(kochome.EnvHome, t.TempDir())
	var out bytes.Buffer
	_, err := Run(context.Background(), "test", args, func(root *cobra.Command, _ []string) {
		root.SetOut(&out)
		root.SetErr(&out)
	})
	return err
}

func TestEmbeddedRefusals(t *testing.T) {
	for _, tc := range []struct {
		args   string
		reason string // "" means not refused
		msg    string
	}{
		{"server list --os-token x", RefusedCredential, "you act as yourself"},
		{"server list --os-auth-url https://elsewhere.example.com", RefusedCredential, ""},
		{"server list --os-cloud prod", RefusedCredential, ""},
		{"server list --os-password-stdin", RefusedCredential, ""},
		{"server list --os-application-credential-secret s", RefusedCredential, ""},
		{"server list --keyvrm-endpoint https://elsewhere.example.com", RefusedCredential, ""},
		{"server list --os-project-name other", RefusedScope, "switch project in the console"},
		{"server list --os-region-name RegionTwo", RefusedScope, "switch region in the console"},
		{"server list --os-project-id p2", RefusedScope, "switch project in the console"},
		{"server list --os-cacert /etc/ssl/ca.pem", RefusedFile, msgFiles},
		{"image create img --file disk.qcow2", RefusedFile, msgFiles},
		{"image save img", RefusedFile, msgFiles},
		{"image stage img", RefusedFile, msgFiles},
		{"server create vm --flavor f --image i --user-data ud.yaml", RefusedFile, msgFiles},
		{"server password show vm --private-key id_rsa", RefusedFile, msgFiles},
		{"keypair create k --public-key id_rsa.pub", RefusedFile, msgFiles},
		{"baremetal node inventory save n --file inv.json", RefusedFile, msgFiles},
		{"baremetal node clean n --clean-steps steps.yaml", RefusedFile, "give the document inline"},
		{"baremetal node clean n --clean-steps -", RefusedFile, ""},
		{"zone import create zone.txt", RefusedFile, msgFiles},
		{"vault kv list x", RefusedCommand, ""},
		{"s3 bucket list", RefusedCommand, ""},
		{"server set vm --root-password", RefusedPrompt, "use --password"},
		{"user create bob --password-prompt", RefusedPrompt, "use --password"},
		{"user set bob --password-prompt", RefusedPrompt, "use --password"},
		{"application credential create c --access-rules rules.json", RefusedFile, "give the rules inline as JSON"},
		{"application credential create c --access-rules '- service: compute'", RefusedFile, ""},
		{"keypair create k", RefusedPrivateKey, "--print-private-key"},
		// Allowed: they reach authentication, which has nothing to go on.
		{"server list", "", ""},
		{"server list --os-region-name RegionOne", "", ""},
		{"server list --os-project-id p1 --long --debug --timing", "", ""},
		{"baremetal node clean n --clean-steps '[{\"interface\":\"deploy\",\"step\":\"erase_devices\"}]'", "", ""},
		{"baremetal node inventory save n", "", ""},
		{"keypair create k --print-private-key", "", ""},
		{"application credential create c --access-rules '[{\"service\":\"compute\",\"method\":\"GET\",\"path\":\"/v2.1/servers\"}]'", "", ""},
		{"help server", "", ""},
	} {
		t.Run(tc.args, func(t *testing.T) {
			args, err := splitForTest(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			err = runEmbedded(t, args...)
			var refused *RefusedError
			switch {
			case tc.reason == "" && errors.As(err, &refused):
				t.Fatalf("refused (%s): %v", refused.Reason, err)
			case tc.reason == "":
				return
			case !errors.As(err, &refused):
				t.Fatalf("not refused: %v", err)
			case refused.Reason != tc.reason:
				t.Errorf("reason %q, want %q", refused.Reason, tc.reason)
			}
			if !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("message %q lacks %q", err, tc.msg)
			}
		})
	}
}

// splitForTest splits a test's command line on spaces, honouring single
// quotes, which is all the table above needs.
func splitForTest(s string) ([]string, error) {
	var out []string
	for s != "" {
		s = strings.TrimLeft(s, " ")
		if s == "" {
			break
		}
		if s[0] == '\'' {
			end := strings.IndexByte(s[1:], '\'')
			if end < 0 {
				return nil, errors.New("open quote")
			}
			out = append(out, s[1:end+1])
			s = s[end+2:]
			continue
		}
		word, rest, _ := strings.Cut(s, " ")
		out = append(out, word)
		s = rest
	}
	return out, nil
}

// TestDefaultProfileRefusesNothing: without the embedded profile every one of
// those lines reaches authentication as before.
func TestDefaultProfileRefusesNothing(t *testing.T) {
	clearOSEnv(t)
	t.Setenv(kochome.EnvProfile, "")
	for _, line := range [][]string{
		{"image save", "img", "--file", "out.img"},
		{"server", "list", "--os-region-name", "RegionTwo"},
		{"keypair", "create", "k"},
	} {
		_, err := Run(context.Background(), "test", line, func(root *cobra.Command, _ []string) {
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
		})
		var refused *RefusedError
		if errors.As(err, &refused) {
			t.Errorf("%q refused in the default profile: %v", line, err)
		}
	}
	if c, _, err := NewRootCommand("test").Find([]string{"keypair", "create"}); err != nil || c.Flags().Lookup(flagPrintPrivateKey) != nil {
		t.Errorf("--%s exists outside the embedded profile", flagPrintPrivateKey)
	}
}

// An unsafe default home stops the command before anything reads from it.
func TestEmbeddedRefusesAnUnsafeDefaultHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not describe who may write on Windows")
	}
	clearOSEnv(t)
	t.Setenv("OS_REGION_NAME", "RegionOne")
	t.Setenv("OS_PROJECT_ID", "p1")
	t.Setenv(kochome.EnvProfile, kochome.ProfileEmbedded)
	t.Setenv(kochome.EnvHome, "")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	home := filepath.Join(tmp, "koc-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0o777); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), "test", []string{"server", "list"}, func(root *cobra.Command, _ []string) {
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
	})
	if err == nil || !strings.Contains(err.Error(), "writable by group or others") {
		t.Errorf("server list with a 0777 default home: err = %v, want the home refused", err)
	}
}
