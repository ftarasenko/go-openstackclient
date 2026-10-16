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
// discovery, the variables layered over a named cloud, the deprecated
// aliases, and the auth types.

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

// A cloud whose auth_type names application credentials keeps them even with a
// leftover OS_PASSWORD exported: gophercloud would otherwise prefer the
// password, where keystoneauth's v3applicationcredential plugin ignores it.
func TestOSPassword_DoesNotSwitchApplicationCredentialCloud(t *testing.T) {
	envOpenrc(t)
	o := envOptions(t, "--os-cloud", "named")
	ao := gophercloud.AuthOptions{
		IdentityEndpoint:            "https://named-cloud.example:5000",
		ApplicationCredentialID:     "ac1",
		ApplicationCredentialSecret: "acsecret",
	}
	o.applyAuthOverrides(&ao)
	if err := o.applyAuthType(&ao, "v3applicationcredential"); err != nil {
		t.Fatal(err)
	}
	if ao.Password != "" || ao.ApplicationCredentialID != "ac1" {
		t.Errorf("got %+v, want application credential auth with no password", ao)
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

// OS_REGION_NAME and OS_INTERFACE beat the cloud's region_name / interface.
func TestOSRegionName_UnderNamedCloud(t *testing.T) {
	isolateConfig(t)
	t.Setenv("OS_REGION_NAME", "envregion")
	t.Setenv("OS_INTERFACE", "admin")
	t.Setenv("OS_PASSWORD", "pw")
	t.Setenv("OS_CLIENT_CONFIG_FILE", writeConfig(t, filepath.Join(t.TempDir(), "clouds.yaml"),
		"clouds:\n  c:\n    region_name: cloudregion\n    interface: internal\n    auth:\n      auth_url: https://k.example/v3\n      username: u\n"))
	_, eo, _, err := envOptions(t, "--os-cloud", "c").resolveAuth()
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	if eo.Region != "envregion" || eo.Availability != gophercloud.AvailabilityAdmin {
		t.Errorf("region/interface = %q/%q, want envregion/admin", eo.Region, eo.Availability)
	}

	t.Setenv("OS_REGION_NAME", "")
	t.Setenv("OS_INTERFACE", "")
	if _, eo, _, err = envOptions(t, "--os-cloud", "c").resolveAuth(); err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	if eo.Region != "cloudregion" || eo.Availability != gophercloud.AvailabilityInternal {
		t.Errorf("region/interface = %q/%q, want the cloud's cloudregion/internal", eo.Region, eo.Availability)
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

// --- endpoint overrides and the Keystone-less auth types ---

func TestCollectEndpointOverrides(t *testing.T) {
	t.Setenv("OS_COMPUTE_ENDPOINT_OVERRIDE", "https://env-nova.example/v2.1")
	t.Setenv("OS_IMAGE_ENDPOINT", "https://plain-glance.example")
	t.Setenv("OS_IMAGE_ENDPOINT_OVERRIDE", "https://glance.example")
	got := collectEndpointOverrides(map[string]string{
		"compute":  "https://cloud-nova.example/v2.1",
		"volumev3": "https://cinder.example/v3",
	})
	for k, want := range map[string]string{
		"compute":  "https://env-nova.example/v2.1", // the environment beats clouds.yaml
		"image":    "https://glance.example",        // _ENDPOINT_OVERRIDE beats _ENDPOINT
		"volumev3": "https://cinder.example/v3",
	} {
		if got[k] != want {
			t.Errorf("override[%s] = %q, want %q", k, got[k], want)
		}
	}
	// OS_ENDPOINT and OS_ENDPOINT_TYPE are not per-service overrides.
	t.Setenv("OS_ENDPOINT", "https://x.example")
	t.Setenv("OS_ENDPOINT_TYPE", "internal")
	if _, ok := collectEndpointOverrides(nil)[""]; ok {
		t.Error("OS_ENDPOINT was read as an override for the empty service type")
	}
}

func TestEndpointLocator_AliasesAndFallthrough(t *testing.T) {
	o := &Options{endpointOverrides: map[string]string{"volumev3": "https://cinder.example/v3"}}
	loc := o.endpointLocator(func(gophercloud.EndpointOpts) (string, error) { return "https://catalog.example/", nil })

	eo := gophercloud.EndpointOpts{}
	eo.ApplyDefaults("block-storage")
	if u, _ := loc(eo); u != "https://cinder.example/v3/" {
		t.Errorf("block-storage = %q, want the volumev3 override", u)
	}
	eo = gophercloud.EndpointOpts{}
	eo.ApplyDefaults("compute")
	if u, _ := loc(eo); u != "https://catalog.example/" {
		t.Errorf("compute = %q, want the catalog's", u)
	}
}

// serviceCapture answers any request and records its auth headers.
func serviceCapture(t *testing.T) (*httptest.Server, *http.Header) {
	t.Helper()
	got := http.Header{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		got.Set("X-Path", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"nodes":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func getNodes(t *testing.T, sc *gophercloud.ServiceClient) {
	t.Helper()
	resp, err := sc.Get(context.Background(), sc.ServiceURL("nodes"), nil, nil)
	if err != nil {
		t.Fatalf("GET nodes: %v", err)
	}
	_ = resp.Body.Close()
}

func TestKeystoneless(t *testing.T) {
	for _, tc := range []struct {
		name               string
		o                  Options
		wantToken, wantBas string
	}{
		{name: "none", o: Options{AuthType: "noauth"}},
		{name: "admin_token", o: Options{AuthType: "token_endpoint", Token: "static"}, wantToken: "static"},
		{name: "http_basic", o: Options{AuthType: "http_basic", Username: "bm", Password: "pw"}, wantBas: "Basic Ym06cHc="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateConfig(t)
			srv, got := serviceCapture(t)
			o := tc.o
			o.Endpoint = srv.URL
			c, err := o.Authenticate(context.Background())
			if err != nil {
				t.Fatalf("Authenticate: %v", err)
			}
			sc, err := c.Baremetal()
			if err != nil {
				t.Fatalf("Baremetal: %v", err)
			}
			getNodes(t, sc)
			if p := got.Get("X-Path"); p != "/v1/nodes" {
				t.Errorf("path = %q, want /v1/nodes under OS_ENDPOINT", p)
			}
			if v := got.Get("X-Auth-Token"); v != tc.wantToken {
				t.Errorf("X-Auth-Token = %q, want %q", v, tc.wantToken)
			}
			if v := got.Get("Authorization"); v != tc.wantBas {
				t.Errorf("Authorization = %q, want %q", v, tc.wantBas)
			}
		})
	}
}

func TestKeystoneless_NeedsAnEndpoint(t *testing.T) {
	isolateConfig(t)
	_, err := (&Options{AuthType: "none"}).Authenticate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "needs the service URL") {
		t.Fatalf("err = %v", err)
	}
}

// clouds.Parse panics on an entry with no auth section, the usual shape for
// auth_type none.
func TestKeystoneless_CloudWithoutAuthSection(t *testing.T) {
	isolateConfig(t)
	srv, got := serviceCapture(t)
	t.Setenv("OS_CLIENT_CONFIG_FILE", writeConfig(t, filepath.Join(t.TempDir(), "clouds.yaml"),
		"clouds:\n  bm:\n    auth_type: none\n    baremetal_endpoint_override: "+srv.URL+"\n"))
	c, err := envOptions(t).Authenticate(context.Background())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	sc, err := c.Baremetal()
	if err != nil {
		t.Fatalf("Baremetal: %v", err)
	}
	getNodes(t, sc)
	if p := got.Get("X-Path"); p != "/v1/nodes" {
		t.Errorf("path = %q", p)
	}
}

// With Keystone, an override replaces just that service's catalog entry.
func TestEndpointOverride_KeystonePath(t *testing.T) {
	isolateConfig(t)
	srv, _ := passwordCapture(t)
	t.Setenv("OS_COMPUTE_ENDPOINT_OVERRIDE", "https://nova.example/v2.1")
	o := mockOptions(srv.URL + "/v3")
	c, err := o.Authenticate(context.Background())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	sc, err := c.Compute()
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if sc.Endpoint != "https://nova.example/v2.1/" {
		t.Errorf("compute endpoint = %q, want the override", sc.Endpoint)
	}
	if _, err := c.Image(); err == nil {
		t.Error("image resolved although the catalog is empty and it has no override")
	}
}

// --- TOTP, trusts and the tenant flags ---

func TestTOTPAuthTypes(t *testing.T) {
	user := func() gophercloud.AuthOptions {
		return gophercloud.AuthOptions{Username: "u", Password: "p", Passcode: "123456", DomainName: "D"}
	}
	ao := user()
	if err := (&Options{AuthType: "v3totp"}).applyAuthType(&ao, ""); err != nil || ao.Password != "" || ao.Passcode != "123456" {
		t.Errorf("v3totp: %v, %+v; want passcode only", err, ao)
	}
	ao = user()
	if err := (&Options{AuthType: "v3multifactor"}).applyAuthType(&ao, ""); err != nil || ao.Password != "p" || ao.Passcode != "123456" {
		t.Errorf("v3multifactor: %v, %+v; want password and passcode", err, ao)
	}
	// Inferred password auth ignores a passcode, as osc-lib's does.
	ao = user()
	if err := (&Options{}).applyAuthType(&ao, ""); err != nil || ao.Passcode != "" {
		t.Errorf("inferred: %v, %+v; want the passcode dropped", err, ao)
	}
	ao = gophercloud.AuthOptions{Username: "u"}
	if err := (&Options{AuthType: "v3totp"}).applyAuthType(&ao, ""); err == nil {
		t.Error("v3totp without a passcode must be an error")
	}
}

func TestTrustScope(t *testing.T) {
	ao := gophercloud.AuthOptions{Username: "u", TenantName: "p", Scope: &gophercloud.AuthScope{ProjectName: "p", DomainName: "D"}}
	(&Options{TrustID: "tr1"}).applyAuthOverrides(&ao)
	if ao.Scope == nil || ao.Scope.TrustID != "tr1" || ao.Scope.ProjectName != "" || ao.TenantName != "" {
		t.Errorf("scope = %+v, tenant %q; want trust tr1 only", ao.Scope, ao.TenantName)
	}
}

func TestTenantFlags(t *testing.T) {
	t.Setenv("OS_PROJECT_NAME", "")
	o := envOptions(t, "--os-tenant-name", "tname", "--os-tenant-id", "tid")
	o.applyTenantAliases()
	if o.ProjectName != "tname" || o.ProjectID != "tid" {
		t.Errorf("project = %q/%q, want the --os-tenant-* values", o.ProjectName, o.ProjectID)
	}
	o = envOptions(t, "--os-tenant-name", "tname", "--os-project-name", "pname")
	o.applyTenantAliases()
	if o.ProjectName != "pname" {
		t.Errorf("project = %q, want --os-project-name to win", o.ProjectName)
	}
}
