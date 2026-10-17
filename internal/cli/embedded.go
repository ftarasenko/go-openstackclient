package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/ftarasenko/go-openstackclient/internal/cli/shell"
	"github.com/ftarasenko/go-openstackclient/internal/kochome"
)

// The embedded profile (KOC_PROFILE=embedded, always on in a koc_embedded
// build) is koc inside a web-console terminal: a short-lived sandbox holding
// only koc, a read-only root, a throwaway home on tmpfs the browser cannot
// reach, and the signed-in user's own token handed over by the host. What
// makes no sense or is unsafe there is refused here, in one table keyed by
// command path and flag name, and enforced once from the root's
// PersistentPreRunE — never inside a service package. TestEmbeddedFileFlags
// walks the command tree so a new file-reading or file-writing flag cannot
// slip past the table.

// Refusal reasons, as the shell's audit event records them.
const (
	RefusedFile       = "file"
	RefusedCredential = "credential"
	RefusedScope      = "scope"
	RefusedPrompt     = "prompt"
	RefusedCommand    = "command"
	RefusedPrivateKey = "private-key"
)

// msgFiles is the one message every local-file flag gets.
const msgFiles = "not available in the console terminal: files stay inside the session"

// RefusedError is an invocation the embedded profile does not allow.
type RefusedError struct {
	Reason string
	Msg    string
}

func (e *RefusedError) Error() string { return e.Msg }

// RefusalReason is the reason the shell's audit event records.
func (e *RefusedError) RefusalReason() string { return e.Reason }

// commandRefusal refuses a command and everything under it.
type commandRefusal struct {
	path   string // without the leading "koc "
	reason string
	msg    string
}

// flagRefusal refuses a flag given on the line. path "" means a global flag,
// refused on every command. allow, when set, admits the values that do not
// touch a file (inline JSON for the baremetal document flags).
type flagRefusal struct {
	path   string
	flag   string
	reason string
	msg    string
	allow  func(val string) bool
}

// embeddedCommandRefusals are the commands the embedded profile refuses whole.
var embeddedCommandRefusals = []commandRefusal{
	// Absent from a koc_embedded build; refused under KOC_PROFILE=embedded.
	{path: "vault", reason: RefusedCommand, msg: "koc vault is not available in the console terminal"},
	{path: "s3", reason: RefusedCommand, msg: "koc s3 is not available in the console terminal"},
	// Uploads image data from a local file or standard input.
	{path: "image stage", reason: RefusedFile, msg: msgFiles},
	// Writes image data to a file, or raw bytes to the terminal.
	{path: "image save", reason: RefusedFile, msg: msgFiles},
	// Reads a zone file.
	{path: "zone import create", reason: RefusedFile, msg: msgFiles},
}

// msgIdentity is the message for the credential and endpoint flags.
func msgIdentity(flag string) string {
	return fmt.Sprintf("--%s is not available in the console terminal: you act as yourself, in the scope the console set", flag)
}

// embeddedFlagRefusals are the flags the embedded profile refuses on the line.
var embeddedFlagRefusals = func() []flagRefusal {
	var out []flagRefusal
	// Credentials, the auth endpoint, and anything that would point the token
	// somewhere else.
	for _, f := range []string{
		"os-token", "os-auth-url", "os-auth-type", "os-cloud",
		"os-username", "os-user-id", "os-password", "os-password-stdin", "os-passcode",
		"os-application-credential-id", "os-application-credential-name", "os-application-credential-secret",
		"os-trust-id", "os-endpoint", "os-system-scope",
		"os-user-domain-name", "os-user-domain-id",
		"insecure", "keyvrm-endpoint",
		"creds-from-ns", "creds-from-vault", "kube-context",
		"vault-addr", "vault-namespace", "vault-token", "vault-role-id", "vault-secret-id",
		"vault-approle-path", "vault-kv-mount", "vault-kv-prefix", "vault-insecure", "insecure-vault",
	} {
		out = append(out, flagRefusal{flag: f, reason: RefusedCredential, msg: msgIdentity(f)})
	}
	// Scope switches other than the two checked against the session.
	for _, f := range []string{
		"os-project-name", "os-tenant-name", "os-tenant-id",
		"os-project-domain-name", "os-project-domain-id", "os-domain-name", "os-domain-id",
	} {
		out = append(out, flagRefusal{flag: f, reason: RefusedScope,
			msg: fmt.Sprintf("--%s is not available in the console terminal: switch project in the console", f)})
	}
	// Global file paths.
	for _, f := range []string{"os-cacert", "os-cert", "os-key", "kubeconfig", "vault-cacert"} {
		out = append(out, flagRefusal{flag: f, reason: RefusedFile, msg: msgFiles})
	}
	// Per-command local files, in and out.
	for _, pf := range [][2]string{
		{"image create", "file"},
		{"server create", "user-data"},
		{"server rebuild", "user-data"},
		{"server password show", "private-key"},
		{"keypair create", "public-key"},
		{"baremetal introspection data save", "file"},
		{"baremetal node inventory save", "file"},
	} {
		out = append(out, flagRefusal{path: pf[0], flag: pf[1], reason: RefusedFile, msg: msgFiles})
	}
	// Baremetal documents: inline JSON/YAML is fine, a path or '-' is not.
	for _, pf := range [][2]string{
		{"baremetal deploy template create", "steps"},
		{"baremetal deploy template set", "steps"},
		{"baremetal runbook create", "steps"},
		{"baremetal runbook set", "steps"},
		{"baremetal node clean", "clean-steps"},
		{"baremetal node service", "service-steps"},
		{"baremetal inspection rule create", "actions"},
		{"baremetal inspection rule create", "conditions"},
		{"baremetal inspection rule set", "actions"},
		{"baremetal inspection rule set", "conditions"},
	} {
		out = append(out, flagRefusal{path: pf[0], flag: pf[1], reason: RefusedFile,
			msg: msgFiles + "; give the document inline", allow: inlineDocument})
	}
	// Prompts on the terminal for a secret.
	out = append(out, flagRefusal{path: "server set", flag: "root-password", reason: RefusedPrompt,
		msg: "--root-password prompts for the password, which the console terminal cannot do; use --password"})
	return out
}()

// inlineDocument reports whether a document flag's value is the document
// itself — JSON, or a YAML flow or block list — rather than a path or '-'.
func inlineDocument(val string) bool {
	v := strings.TrimSpace(val)
	return strings.HasPrefix(v, "{") || strings.HasPrefix(v, "[") ||
		(strings.HasPrefix(v, "- ") || strings.HasPrefix(v, "-\n"))
}

// sessionScopeFlags are allowed on the line only with the session's own value.
var sessionScopeFlags = []struct{ flag, what string }{
	{"os-region-name", "region"},
	{"os-project-id", "project"},
}

// flagPrintPrivateKey is the embedded profile's own flag on `keypair create`:
// nova returns a generated private key exactly once, and in the console the
// terminal is the only place it can go.
const flagPrintPrivateKey = "print-private-key"

// keypairCreatePath is the command that may print a generated private key.
const keypairCreatePath = "keypair create"

// addEmbeddedFlags registers the embedded profile's own flags. They exist only
// in that profile, so the default CLI's help is unchanged.
func addEmbeddedFlags(root *cobra.Command) {
	if !kochome.Embedded() {
		return
	}
	if c, _, err := root.Find(strings.Fields(keypairCreatePath)); err == nil && c.CommandPath() == "koc "+keypairCreatePath {
		c.Flags().Bool(flagPrintPrivateKey, false,
			"print the generated private key on the terminal; it is shown once and stored nowhere")
	}
}

// checkEmbedded refuses what the embedded profile does not allow for the
// command cobra resolved and the flags typed for it.
func checkEmbedded(cmd *cobra.Command) error {
	if !kochome.Embedded() {
		return nil
	}
	path := strings.TrimPrefix(cmd.CommandPath(), "koc")
	path = strings.TrimSpace(path)
	switch cmd.Name() {
	case cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd, "help", "completion":
		return nil
	}
	for _, r := range embeddedCommandRefusals {
		if path == r.path || strings.HasPrefix(path, r.path+" ") {
			return &RefusedError{Reason: r.reason, Msg: r.msg}
		}
	}

	fs := cmd.Flags()
	for _, r := range embeddedFlagRefusals {
		if r.path != "" && r.path != path {
			continue
		}
		f := fs.Lookup(r.flag)
		if f == nil || !f.Changed {
			continue
		}
		if r.allow != nil && r.allow(f.Value.String()) {
			continue
		}
		return &RefusedError{Reason: r.reason, Msg: r.msg}
	}
	for _, s := range sessionScopeFlags {
		f := fs.Lookup(s.flag)
		if f == nil || !f.Changed {
			continue
		}
		if f.Value.String() != sessionValue(cmd.Root(), f) {
			return &RefusedError{Reason: RefusedScope,
				Msg: fmt.Sprintf("--%s %s is not this session's %s; switch %s in the console", s.flag, f.Value.String(), s.what, s.what)}
		}
	}
	return checkPrivateKey(cmd, path)
}

// sessionValue is the session's own value of a scope flag: what the shell
// recorded from the scope file or the environment, else the flag's
// environment-derived default.
func sessionValue(root *cobra.Command, f *pflag.Flag) string {
	if v, ok := root.Annotations[shell.SessionAnnotation+f.Name]; ok {
		return v
	}
	return f.DefValue
}

// checkPrivateKey makes `keypair create` without --public-key (nova generates
// the pair) require --print-private-key, and warns when it is given.
func checkPrivateKey(cmd *cobra.Command, path string) error {
	if path != keypairCreatePath {
		return nil
	}
	if pk := cmd.Flags().Lookup("public-key"); pk != nil && pk.Changed {
		return nil
	}
	if f := cmd.Flags().Lookup(flagPrintPrivateKey); f != nil && f.Changed && f.Value.String() == "true" {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(),
			"WARNING: the generated private key is printed below, once. Copy it now: koc keeps no copy and nova cannot show it again.")
		return nil
	}
	return &RefusedError{Reason: RefusedPrivateKey,
		Msg: "keypair create generates a private key, which is printed on the terminal; pass --" + flagPrintPrivateKey + " to confirm"}
}
