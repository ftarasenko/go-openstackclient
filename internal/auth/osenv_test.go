package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/pflag"
)

// python-openstackclient parity for the OS_* environment: clouds.yaml
// discovery, the variables that fill a named cloud's gaps, the deprecated
// aliases, and token authentication.

// isolateConfig points every clouds.yaml search location koc consults at empty
// temporary directories, so a developer's own ~/.config/openstack cannot leak
// into a test.
func isolateConfig(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CONFIG_DIRS", filepath.Join(home, "xdg"))
	t.Setenv("SNAP_REAL_HOME", "")
	t.Setenv("OS_CLIENT_CONFIG_FILE", "")
	t.Setenv("OS_CLIENT_SECURE_FILE", "")
	t.Chdir(t.TempDir())
	return home
}

// envOptions builds Options the way the root command does — flags defaulted
// from the environment — and parses args.
func envOptions(t *testing.T, args ...string) *Options {
	t.Helper()
	o := &Options{}
	fs := pflag.NewFlagSet("koc", pflag.ContinueOnError)
	o.AddFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return o
}

func writeConfig(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// cloudNoPassword is the clouds.yaml shape the upstream docs recommend: every
// setting but the secret, which comes from OS_PASSWORD or secure.yaml.
func cloudNoPassword(authURL string) string {
	return "clouds:\n  mycloud:\n    region_name: RegionOne\n    auth:\n      auth_url: " + authURL +
		"\n      username: admin\n      project_name: admin\n" +
		"      user_domain_name: Default\n      project_domain_name: Default\n"
}

// authCapture answers a token request and records the auth body.
func authCapture(t *testing.T) (*httptest.Server, *map[string]any) {
	t.Helper()
	got := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Auth map[string]any `json:"auth"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding the token request: %v", err)
		}
		got = body.Auth
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Subject-Token", "gAAAAAtoken")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(tokenResponse))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func jsonPath(m map[string]any, keys ...string) any {
	var v any = m
	for _, k := range keys {
		mm, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = mm[k]
	}
	return v
}

// The reported bug: OS_CLOUD + OS_CLIENT_CONFIG_FILE naming a cloud with no
// stored password, and the password exported as OS_PASSWORD.
func TestOSPassword_FillsNamedCloudWithoutPassword(t *testing.T) {
	isolateConfig(t)
	srv, got := passwordCapture(t)
	t.Setenv("OS_CLIENT_CONFIG_FILE", writeConfig(t, filepath.Join(t.TempDir(), "clouds.yaml"), cloudNoPassword(srv.URL+"/v3")))
	t.Setenv("OS_CLOUD", "mycloud")
	t.Setenv("OS_PASSWORD", "from-env")

	o := envOptions(t)
	o.promptPassword = func(io.Writer) (string, error) {
		t.Fatal("prompted although OS_PASSWORD was set")
		return "", nil
	}
	if _, err := o.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if *got != "from-env" {
		t.Errorf("keystone verified %q, want OS_PASSWORD's from-env", *got)
	}
}

// A cloud that authenticates by application credential must not be switched to
// password auth by a leftover OS_PASSWORD (gophercloud prefers a password when
// both are present).
func TestOSPassword_DoesNotFillApplicationCredentialCloud(t *testing.T) {
	envOpenrc(t)
	o := envOptions(t, "--os-cloud", "named")
	ao := gophercloud.AuthOptions{
		IdentityEndpoint:            "https://named-cloud.example:5000",
		ApplicationCredentialID:     "ac1",
		ApplicationCredentialSecret: "acsecret",
	}
	o.applyAuthOverrides(&ao)
	if ao.Password != "" || ao.Username != "" {
		t.Errorf("password/username = %q/%q, want both empty on an application credential cloud", ao.Password, ao.Username)
	}
}

func TestOSApplicationCredentialSecret_FillsNamedCloud(t *testing.T) {
	t.Setenv("OS_APPLICATION_CREDENTIAL_SECRET", "env-secret")
	o := envOptions(t, "--os-cloud", "named")
	ao := gophercloud.AuthOptions{ApplicationCredentialID: "ac1"}
	o.applyAuthOverrides(&ao)
	if ao.ApplicationCredentialSecret != "env-secret" {
		t.Errorf("secret = %q, want env-secret", ao.ApplicationCredentialSecret)
	}
}

// OS_REGION_NAME fills a cloud with no region_name, and does not beat one the
// cloud states — clouds.Parse used to read it itself and let it win.
func TestOSRegionName_UnderNamedCloud(t *testing.T) {
	isolateConfig(t)
	t.Setenv("OS_REGION_NAME", "envregion")
	t.Setenv("OS_INTERFACE", "admin")
	t.Setenv("OS_PASSWORD", "pw")
	dir := t.TempDir()

	for _, tc := range []struct {
		name, region, want string
	}{
		{"cloud states its region", "    region_name: cloudregion\n", "cloudregion"},
		{"cloud has none", "", "envregion"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OS_CLIENT_CONFIG_FILE", writeConfig(t, filepath.Join(dir, tc.want+".yaml"),
				"clouds:\n  c:\n"+tc.region+"    auth:\n      auth_url: https://k.example/v3\n      username: u\n"))
			o := envOptions(t, "--os-cloud", "c")
			_, eo, _, err := o.resolveAuth()
			if err != nil {
				t.Fatalf("resolveAuth: %v", err)
			}
			if eo.Region != tc.want {
				t.Errorf("region = %q, want %q", eo.Region, tc.want)
			}
			if eo.Availability != gophercloud.AvailabilityPublic {
				t.Errorf("interface = %q, want public (OS_INTERFACE must not win)", eo.Availability)
			}
		})
	}
}

// openstacksdk uses the only cloud a clouds.yaml defines without OS_CLOUD.
func TestDefaultCloud_SingleCloudNeedsNoOSCloud(t *testing.T) {
	isolateConfig(t)
	srv, got := passwordCapture(t)
	t.Setenv("OS_CLIENT_CONFIG_FILE", writeConfig(t, filepath.Join(t.TempDir(), "clouds.yaml"), cloudNoPassword(srv.URL+"/v3")))
	t.Setenv("OS_PASSWORD", "from-env")

	if _, err := envOptions(t).Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if *got != "from-env" {
		t.Errorf("keystone verified %q, want from-env", *got)
	}
}

func TestDefaultCloud_SeveralCloudsNeedAName(t *testing.T) {
	isolateConfig(t)
	t.Setenv("OS_CLIENT_CONFIG_FILE", writeConfig(t, filepath.Join(t.TempDir(), "clouds.yaml"),
		"clouds:\n  b: {auth: {auth_url: https://b.example}}\n  a: {auth: {auth_url: https://a.example}}\n"))
	_, _, _, err := envOptions(t).resolveAuth()
	if err == nil || !strings.Contains(err.Error(), "several clouds (a, b)") {
		t.Fatalf("err = %v, want the several-clouds error naming a and b", err)
	}
}

// OS_AUTH_URL keeps the environment path even with a one-cloud clouds.yaml
// around, as upstream's "envvars" cloud does.
func TestDefaultCloud_EnvAuthURLWins(t *testing.T) {
	isolateConfig(t)
	t.Setenv("OS_CLIENT_CONFIG_FILE", writeConfig(t, filepath.Join(t.TempDir(), "clouds.yaml"), cloudNoPassword("https://file.example/v3")))
	t.Setenv("OS_AUTH_URL", "https://env.example/v3")
	t.Setenv("OS_USERNAME", "u")
	t.Setenv("OS_PASSWORD", "p")
	o := envOptions(t)
	ao, _, _, err := o.resolveAuth()
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	if o.Cloud != "" || ao.IdentityEndpoint != "https://env.example/v3" {
		t.Errorf("cloud=%q endpoint=%q, want the env path", o.Cloud, ao.IdentityEndpoint)
	}
}

// OS_CLIENT_CONFIG_FILE is prepended to the search path, not the only entry:
// a stale value falls back to the standard locations, which include .yml.
func TestCloudsSearch_MissingOverrideFallsBackAndYml(t *testing.T) {
	home := isolateConfig(t)
	writeConfig(t, filepath.Join(home, ".config", "openstack", "clouds.yml"), cloudNoPassword("https://yml.example/v3"))
	t.Setenv("OS_CLIENT_CONFIG_FILE", filepath.Join(home, "gone", "clouds.yaml"))
	t.Setenv("OS_PASSWORD", "p")

	ao, _, _, err := envOptions(t, "--os-cloud", "mycloud").resolveAuth()
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	if ao.IdentityEndpoint != "https://yml.example/v3" {
		t.Errorf("endpoint = %q, want the clouds.yml one", ao.IdentityEndpoint)
	}
}

func TestCloudsSearch_SiteConfigDir(t *testing.T) {
	home := isolateConfig(t)
	writeConfig(t, filepath.Join(home, "xdg", "openstack", "clouds.yaml"), cloudNoPassword("https://site.example/v3"))
	t.Setenv("OS_PASSWORD", "p")
	ao, _, _, err := envOptions(t, "--os-cloud", "mycloud").resolveAuth()
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	if ao.IdentityEndpoint != "https://site.example/v3" {
		t.Errorf("endpoint = %q, want the XDG_CONFIG_DIRS one", ao.IdentityEndpoint)
	}
}

// OS_CLIENT_SECURE_FILE supplies the password from anywhere, not only from a
// secure.yaml beside clouds.yaml.
func TestCloudsSearch_ClientSecureFile(t *testing.T) {
	isolateConfig(t)
	srv, got := passwordCapture(t)
	t.Setenv("OS_CLIENT_CONFIG_FILE", writeConfig(t, filepath.Join(t.TempDir(), "clouds.yaml"), cloudNoPassword(srv.URL+"/v3")))
	t.Setenv("OS_CLIENT_SECURE_FILE", writeConfig(t, filepath.Join(t.TempDir(), "elsewhere.yaml"),
		"clouds:\n  mycloud:\n    auth:\n      password: from-secure\n"))

	if _, err := envOptions(t, "--os-cloud", "mycloud").Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if *got != "from-secure" {
		t.Errorf("keystone verified %q, want from-secure", *got)
	}
}

// --- deprecated aliases ---

func TestOSTenantAliases(t *testing.T) {
	t.Setenv("OS_PROJECT_NAME", "")
	t.Setenv("OS_PROJECT_ID", "")
	t.Setenv("OS_TENANT_NAME", "tname")
	t.Setenv("OS_TENANT_ID", "tid")
	o := envOptions(t)
	if o.ProjectName != "tname" || o.ProjectID != "tid" {
		t.Errorf("project name/id = %q/%q, want the OS_TENANT_* values", o.ProjectName, o.ProjectID)
	}

	t.Setenv("OS_PROJECT_NAME", "pname")
	if o := envOptions(t); o.ProjectName != "pname" {
		t.Errorf("project name = %q, want OS_PROJECT_NAME to win over OS_TENANT_NAME", o.ProjectName)
	}
}

func TestOSEndpointTypeAlias(t *testing.T) {
	t.Setenv("OS_INTERFACE", "")
	t.Setenv("OS_ENDPOINT_TYPE", "internalURL")
	o := envOptions(t)
	var eo gophercloud.EndpointOpts
	o.applyEndpointOverrides(&eo)
	if eo.Availability != gophercloud.AvailabilityInternal {
		t.Errorf("interface = %q, want internal from OS_ENDPOINT_TYPE=internalURL", eo.Availability)
	}
}

func TestNormalizeInterface(t *testing.T) {
	for in, want := range map[string]string{"": "", "public": "public", "Internal": "internal", "adminURL": "admin", "publicURL": "public"} {
		if got, err := normalizeInterface(in); err != nil || got != want {
			t.Errorf("normalizeInterface(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := normalizeInterface("intrenal"); err == nil {
		t.Error("a misspelt interface must be an error, not a silent public")
	}
}

// --- token auth ---

func TestToken_EnvPathRescopesToProject(t *testing.T) {
	srv, got := authCapture(t)
	o := &Options{AuthURL: srv.URL + "/v3", Token: "tok123", ProjectName: "admin", ProjectDomainName: "Default"}
	if _, err := o.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if m := jsonPath(*got, "identity", "methods"); len(m.([]any)) != 1 || m.([]any)[0] != "token" {
		t.Errorf("methods = %v, want [token]", m)
	}
	if id := jsonPath(*got, "identity", "token", "id"); id != "tok123" {
		t.Errorf("token id = %v", id)
	}
	if n := jsonPath(*got, "scope", "project", "name"); n != "admin" {
		t.Errorf("scope = %v, want project admin", jsonPath(*got, "scope"))
	}
	if d := jsonPath(*got, "scope", "project", "domain", "name"); d != "Default" {
		t.Errorf("scope domain = %v, want Default", d)
	}
}

func TestAuthType(t *testing.T) {
	base := func() gophercloud.AuthOptions {
		return gophercloud.AuthOptions{Username: "u", Password: "p", DomainName: "D", TokenID: "tok"}
	}
	t.Run("inferred: a user beats a token", func(t *testing.T) {
		ao := base()
		if err := (&Options{}).applyAuthType(&ao, ""); err != nil {
			t.Fatal(err)
		}
		if ao.TokenID != "" || ao.Password != "p" {
			t.Errorf("got %+v, want password auth", ao)
		}
	})
	t.Run("explicit token clears the user", func(t *testing.T) {
		ao := base()
		if err := (&Options{AuthType: "v3token"}).applyAuthType(&ao, ""); err != nil {
			t.Fatal(err)
		}
		if ao.TokenID != "tok" || ao.Username != "" || ao.Password != "" || ao.DomainName != "" {
			t.Errorf("got %+v, want token auth only", ao)
		}
	})
	t.Run("cloud auth_type is used", func(t *testing.T) {
		ao := base()
		if err := (&Options{Cloud: "c"}).applyAuthType(&ao, "token"); err != nil {
			t.Fatal(err)
		}
		if ao.Username != "" {
			t.Errorf("got %+v, want the cloud's token auth_type applied", ao)
		}
	})
	t.Run("token type without a token", func(t *testing.T) {
		ao := gophercloud.AuthOptions{Username: "u"}
		if err := (&Options{AuthType: "token"}).applyAuthType(&ao, ""); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("unsupported", func(t *testing.T) {
		ao := base()
		err := (&Options{AuthType: "v3oidcpassword"}).applyAuthType(&ao, "")
		if err == nil || !strings.Contains(err.Error(), "unsupported auth type") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestRedact_TokenAuthRequest(t *testing.T) {
	in := "POST /v3/auth/tokens HTTP/1.1\r\nContent-Type: application/json\r\n\r\n" +
		`{"auth":{"identity":{"methods":["token"],"token":{"id":"tok123"}}}}`
	if out := redact(in); strings.Contains(out, "tok123") {
		t.Errorf("token leaked: %s", out)
	}
}
