package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/pflag"
)

// --- --os-password-stdin ---

func TestReadPasswordStdin(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{name: "bare", in: "s3cret", want: "s3cret"},
		{name: "trailing newline", in: "s3cret\n", want: "s3cret"},
		{name: "trailing CRLF", in: "s3cret\r\n", want: "s3cret"},
		// A password may legitimately end in a space; only the line ending goes.
		{name: "keeps surrounding spaces", in: " s3 cret \n", want: " s3 cret "},
		{name: "keeps inner quoting", in: `"s3cret"` + "\n", want: `"s3cret"`},
		{name: "empty", in: "", wantErr: "no password on stdin"},
		{name: "newline only", in: "\n", wantErr: "no password on stdin"},
		{name: "whole openrc", in: "export OS_PASSWORD=s3cret\nexport OS_USERNAME=admin\n", wantErr: "more than one line"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readPasswordStdin(strings.NewReader(tc.in))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("readPasswordStdin: %v", err)
			}
			if got != tc.want {
				t.Errorf("password = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReadPasswordStdin_ReadError(t *testing.T) {
	_, err := readPasswordStdin(errReader{})
	if err == nil || !strings.Contains(err.Error(), "reading stdin") {
		t.Fatalf("error = %v, want a read failure", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestApplyPasswordStdin_PopulatesPasswordAndOutranksACloud(t *testing.T) {
	o := &Options{PasswordStdin: true, passwordStdinSrc: strings.NewReader("piped\n")}
	if err := o.applyPasswordStdin(); err != nil {
		t.Fatalf("applyPasswordStdin: %v", err)
	}
	if o.Password != "piped" {
		t.Errorf("password = %q, want piped", o.Password)
	}
	// A piped password is as deliberate as a typed one, so it must survive the
	// clouds.yaml guard in Options.override.
	o.Cloud = "named"
	if got := o.override("os-password", o.Password); got != "piped" {
		t.Errorf("override dropped the piped password: %q", got)
	}
}

func TestApplyPasswordStdin_Inert(t *testing.T) {
	o := &Options{Password: "fromenv", passwordStdinSrc: strings.NewReader("piped\n")}
	if err := o.applyPasswordStdin(); err != nil {
		t.Fatalf("applyPasswordStdin: %v", err)
	}
	if o.Password != "fromenv" {
		t.Errorf("password = %q, want the flag/env value untouched", o.Password)
	}
}

// An explicitly typed --os-password and --os-password-stdin name two different
// secrets; picking one silently is how the wrong credential reaches Keystone.
func TestApplyPasswordStdin_ConflictsWithExplicitFlag(t *testing.T) {
	o := &Options{}
	fs := pflag.NewFlagSet("koc", pflag.ContinueOnError)
	o.AddFlags(fs)
	if err := fs.Parse([]string{"--os-password", "typed", "--os-password-stdin"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	o.passwordStdinSrc = strings.NewReader("piped\n")

	err := o.applyPasswordStdin()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("error = %v, want a mutual-exclusion error", err)
	}
}

// OS_PASSWORD is background configuration, not a typed flag, so --os-password-
// stdin overrides it rather than erroring.
func TestApplyPasswordStdin_BeatsEnvPassword(t *testing.T) {
	t.Setenv("OS_PASSWORD", "fromenv")
	o := &Options{}
	fs := pflag.NewFlagSet("koc", pflag.ContinueOnError)
	o.AddFlags(fs)
	if err := fs.Parse([]string{"--os-password-stdin"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	o.passwordStdinSrc = strings.NewReader("piped\n")

	if err := o.applyPasswordStdin(); err != nil {
		t.Fatalf("applyPasswordStdin: %v", err)
	}
	if o.Password != "piped" {
		t.Errorf("password = %q, want piped", o.Password)
	}
}

func TestApplyPasswordStdin_ConflictsWithCredsFrom(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		opts       Options
	}{
		{name: "ns", want: "--creds-from-ns", opts: Options{CredsFromNS: "ironic"}},
		{name: "vault", want: "--creds-from-vault", opts: Options{CredsFromVault: "openrc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := tc.opts
			o.PasswordStdin = true
			o.passwordStdinSrc = strings.NewReader("piped\n")
			err := o.applyPasswordStdin()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one naming %s", err, tc.want)
			}
		})
	}
}

// --os-password-stdin has taken stdin, so there is nothing left to prompt on
// even when the process still has a terminal.
func TestTerminalPassword_NilWhenStdinIsThePassword(t *testing.T) {
	o := &Options{PasswordStdin: true}
	if o.terminalPassword() != nil {
		t.Error("--os-password-stdin must not also prompt")
	}
}

// --- the interactive prompt ---

func TestPromptMissingPassword_AsksAndFills(t *testing.T) {
	var w bytes.Buffer
	o := &Options{promptPassword: func(out io.Writer) (string, error) {
		_, _ = io.WriteString(out, passwordPrompt)
		return "typed", nil
	}}
	ao := gophercloud.AuthOptions{Username: "admin"}

	if err := o.promptMissingPassword(&ao, &w); err != nil {
		t.Fatalf("promptMissingPassword: %v", err)
	}
	if ao.Password != "typed" {
		t.Errorf("password = %q, want typed", ao.Password)
	}
	if w.String() != passwordPrompt {
		t.Errorf("prompt = %q, want %q", w.String(), passwordPrompt)
	}
}

func TestPromptMissingPassword_EmptyAnswerIsAnError(t *testing.T) {
	o := &Options{promptPassword: func(io.Writer) (string, error) { return "", nil }}
	ao := gophercloud.AuthOptions{Username: "admin"}
	if err := o.promptMissingPassword(&ao, io.Discard); err == nil {
		t.Fatal("expected an error when the operator just hit Enter")
	}
}

func TestPromptMissingPassword_PropagatesReadError(t *testing.T) {
	o := &Options{promptPassword: func(io.Writer) (string, error) { return "", errors.New("no tty") }}
	ao := gophercloud.AuthOptions{Username: "admin"}
	if err := o.promptMissingPassword(&ao, io.Discard); err == nil {
		t.Fatal("expected the terminal read error to propagate")
	}
}

// Everything that authenticates without a password must go through untouched —
// prompting for one would be a lie about what the request needs.
func TestPromptMissingPassword_SkipsWhenNoPasswordIsNeeded(t *testing.T) {
	cases := map[string]gophercloud.AuthOptions{
		"password already set":   {Username: "admin", Password: "s3cret"},
		"application credential": {ApplicationCredentialID: "ac1", ApplicationCredentialSecret: "s"},
		"app credential by name": {Username: "admin", ApplicationCredentialName: "deploy"},
		"pre-issued token":       {TokenID: "gAAAAA"},
		"no user to ask about":   {IdentityEndpoint: "https://keystone.example/v3"},
	}
	for name, ao := range cases {
		t.Run(name, func(t *testing.T) {
			o := &Options{promptPassword: func(io.Writer) (string, error) {
				t.Error("must not prompt")
				return "", nil
			}}
			before := ao
			if err := o.promptMissingPassword(&ao, io.Discard); err != nil {
				t.Fatalf("promptMissingPassword: %v", err)
			}
			if ao != before {
				t.Errorf("auth options were modified: %+v", ao)
			}
		})
	}
}

// With no terminal the env path's own error is the better message, so the
// prompt step must leave the options alone rather than invent a failure.
func TestPromptMissingPassword_NoTerminalIsNotAnError(t *testing.T) {
	o := &Options{} // no seam, and `go test` stdin is not a terminal
	ao := gophercloud.AuthOptions{Username: "admin"}
	if err := o.promptMissingPassword(&ao, io.Discard); err != nil {
		t.Fatalf("promptMissingPassword: %v", err)
	}
	if ao.Password != "" {
		t.Errorf("password = %q, want it left empty", ao.Password)
	}
}

// The env path rejects a missing password up front — unless it is about to be
// asked for.
func TestResolveAuth_EnvMissingPasswordDefersToThePrompt(t *testing.T) {
	o := &Options{
		AuthURL:  "https://keystone.example/v3",
		Username: "admin",
		promptPassword: func(io.Writer) (string, error) {
			return "typed", nil
		},
	}
	if _, _, _, err := o.resolveAuth(); err != nil {
		t.Fatalf("resolveAuth must defer to the prompt: %v", err)
	}

	// ...but only when there is a user to prompt about.
	anon := &Options{AuthURL: "https://keystone.example/v3", promptPassword: o.promptPassword}
	if _, _, _, err := anon.resolveAuth(); err == nil {
		t.Error("expected the no-credentials error when there is no user to prompt about")
	}
}

// --- end to end against a mock Keystone ---

// passwordCapture answers a token request and records the password Keystone was
// asked to verify.
func passwordCapture(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Auth struct {
				Identity struct {
					Password struct {
						User struct {
							Password string `json:"password"`
						} `json:"user"`
					} `json:"password"`
				} `json:"identity"`
			} `json:"auth"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding the token request: %v", err)
		}
		got = body.Auth.Identity.Password.User.Password
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Subject-Token", "gAAAAAtoken")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(tokenResponse))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestAuthenticate_PasswordFromStdin(t *testing.T) {
	srv, got := passwordCapture(t)
	o := mockOptions(srv.URL + "/v3")
	o.Password = ""
	o.PasswordStdin = true
	o.passwordStdinSrc = strings.NewReader("piped-password\n")

	if _, err := o.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if *got != "piped-password" {
		t.Errorf("keystone verified %q, want piped-password", *got)
	}
}

func TestAuthenticate_PromptsForAMissingPassword(t *testing.T) {
	srv, got := passwordCapture(t)
	o := mockOptions(srv.URL + "/v3")
	o.Password = ""
	asked := 0
	o.promptPassword = func(io.Writer) (string, error) {
		asked++
		return "typed-password", nil
	}

	if _, err := o.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if asked != 1 {
		t.Errorf("prompted %d times, want 1", asked)
	}
	if *got != "typed-password" {
		t.Errorf("keystone verified %q, want typed-password", *got)
	}
}

// The password the operator typed has to be kept for gophercloud's reauth, or
// the second token request of a long command asks for it again.
func TestAuthenticate_PromptedPasswordSurvivesForReauth(t *testing.T) {
	srv, got := passwordCapture(t)
	o := mockOptions(srv.URL + "/v3")
	o.Password = ""
	prompted := 0
	o.promptPassword = func(io.Writer) (string, error) {
		prompted++
		return "typed-password", nil
	}

	client, err := o.Authenticate(context.Background())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if client.Provider.ReauthFunc == nil {
		t.Fatal("provider has no reauth function")
	}

	*got = ""
	if err := client.Provider.ReauthFunc(context.Background()); err != nil {
		t.Fatalf("reauth: %v", err)
	}
	if *got != "typed-password" {
		t.Errorf("reauth sent %q, want the password typed at the first prompt", *got)
	}
	if prompted != 1 {
		t.Errorf("prompted %d times, want 1 — reauth must replay the answer, not ask again", prompted)
	}
}

func TestAuthenticate_StdinConflictReportedBeforeAnyRequest(t *testing.T) {
	srv, got := passwordCapture(t)
	o := mockOptions(srv.URL + "/v3")
	o.PasswordStdin = true
	o.passwordStdinSrc = strings.NewReader("piped\n")
	o.CredsFromVault = "openrc"

	if _, err := o.Authenticate(context.Background()); err == nil {
		t.Fatal("expected a mutual-exclusion error")
	}
	if *got != "" {
		t.Errorf("keystone was contacted with %q", *got)
	}
}

// The classic osc-lib case: a clouds.yaml entry that deliberately stores no
// password, so the operator is asked for it at each invocation.
func TestAuthenticate_PromptsForACloudWithNoStoredPassword(t *testing.T) {
	srv, got := passwordCapture(t)
	cloudsPath := filepath.Join(t.TempDir(), "clouds.yaml")
	yaml := "clouds:\n  keystack:\n    auth:\n      auth_url: " + srv.URL + "/v3\n" +
		"      username: admin\n      project_name: admin\n" +
		"      user_domain_name: Default\n      project_domain_name: Default\n"
	if err := os.WriteFile(cloudsPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OS_CLIENT_CONFIG_FILE", cloudsPath)

	o := &Options{Cloud: "keystack", Timeout: 10 * time.Second}
	o.promptPassword = func(io.Writer) (string, error) { return "typed-password", nil }

	if _, err := o.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if *got != "typed-password" {
		t.Errorf("keystone verified %q, want typed-password", *got)
	}
}

// stdin can only be consumed once, and a prompt must not reappear, so a second
// authentication in the same process reuses what the first one obtained.
func TestPasswordSourcesAreConsultedOnce(t *testing.T) {
	t.Run("stdin", func(t *testing.T) {
		src := strings.NewReader("piped\n")
		o := &Options{PasswordStdin: true, passwordStdinSrc: src}
		if err := o.applyPasswordStdin(); err != nil {
			t.Fatalf("first read: %v", err)
		}
		if err := o.applyPasswordStdin(); err != nil {
			t.Fatalf("second read: %v", err) // an exhausted reader would error
		}
		if o.Password != "piped" {
			t.Errorf("password = %q, want piped", o.Password)
		}
	})

	t.Run("prompt", func(t *testing.T) {
		srv, got := passwordCapture(t)
		o := mockOptions(srv.URL + "/v3")
		o.Password = ""
		asked := 0
		o.promptPassword = func(io.Writer) (string, error) {
			asked++
			return "typed-password", nil
		}
		for i := range 2 {
			if _, err := o.Authenticate(context.Background()); err != nil {
				t.Fatalf("Authenticate %d: %v", i, err)
			}
		}
		if asked != 1 {
			t.Errorf("prompted %d times, want 1", asked)
		}
		if *got != "typed-password" {
			t.Errorf("keystone verified %q on the second run", *got)
		}
	})
}
