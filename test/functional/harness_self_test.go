//go:build functional

package functional

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/cli"
)

// The harness's own tests. They need no cloud, so they run wherever the
// functional tag is built — including offline CI, where every cloud test skips.

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadEnv(t *testing.T) {
	t.Setenv("KOC_FT_AUTH", "") // the nightly sets it per cell; this checks the default
	dir := t.TempDir()
	p := writeFile(t, dir, "functional.env", `OS_CLOUD=devstack-admin
KOC_FT_SERIES=2025.1
KOC_FT_FEATURES=core,net,dns
KOC_FT_BACKEND=ovn
KOC_FT_IMAGE=cirros-0.6.3-x86_64-disk
AWS_ENDPOINT_URL=http://192.0.2.10:8080
AWS_SECRET_ACCESS_KEY=abc=def
`)
	writeFile(t, dir, "openrc.env", "OS_USERNAME=admin\nOS_VOLUME_API_VERSION=3\n")

	c, err := loadEnv(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Series.String() != "2025.1" || c.Backend != "ovn" || c.Cloud != "devstack-admin" || c.Auth != "clouds" {
		t.Errorf("parsed %+v", c)
	}
	if !c.Features["dns"] || c.Features["baremetal"] {
		t.Errorf("features = %v", c.Features)
	}
	// A value keeps everything after the first "=".
	if !slices.Contains(c.S3, "AWS_SECRET_ACCESS_KEY=abc=def") {
		t.Errorf("S3 = %v", c.S3)
	}
	if !slices.Contains(c.Openrc, "OS_VOLUME_API_VERSION=3") {
		t.Errorf("openrc = %v", c.Openrc)
	}

	if c, err := loadEnv(""); c != nil || err != nil {
		t.Errorf("no KOC_FT_ENV = %v, %v; want nil, nil (every test skips)", c, err)
	}
	t.Setenv("KOC_FT_AUTH", "vault")
	if _, err := loadEnv(p); err == nil {
		t.Error("KOC_FT_AUTH=vault: want an error, only clouds and env are default paths")
	}
}

func TestLoadEnv_EnvAuthNeedsOpenrc(t *testing.T) {
	p := writeFile(t, t.TempDir(), "functional.env", "OS_CLOUD=devstack-admin\nKOC_FT_SERIES=zed\n")
	t.Setenv("KOC_FT_AUTH", "env")
	if _, err := loadEnv(p); err == nil {
		t.Error("KOC_FT_AUTH=env without openrc.env: want an error, not an empty environment")
	}
}

func TestSeriesOrder(t *testing.T) {
	zed, err := parseSeries("zed")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		s, min string
		want   bool
	}{
		{"zed", "2024.1", false},
		{"2024.1", "2024.1", true},
		{"2025.2", "2025.1", true},
		{"2026.1", "2025.2", true},
		{"2024.1", "2025.1", false},
	} {
		s, err := parseSeries(tc.s)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.atLeast(tc.min); got != tc.want {
			t.Errorf("%s.atLeast(%s) = %v, want %v", tc.s, tc.min, got, tc.want)
		}
	}
	if zed.String() != "zed" || !zed.atLeast("2022.2") {
		t.Errorf("zed = %v", zed)
	}
	if _, err := parseSeries("caracal"); err == nil {
		t.Error("a release name other than zed must be rejected: up.sh writes numbers")
	}
}

// The runner's environment must not leak a credential from the process that
// runs the tests: every family koc reads is dropped.
func TestBaseEnvScrubsCredentials(t *testing.T) {
	for _, kv := range []string{
		"OS_PASSWORD=leak", "OS_CLOUD=leak", "AWS_SECRET_ACCESS_KEY=leak", "S3_ACCESS_KEY=leak",
		"s3_secret_key=leak", "VAULT_TOKEN=leak", "KOC_FT_AUTH=env", "KUBECONFIG=/leak",
	} {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	t.Setenv("FT_KEEP", "yes")
	got := baseEnv()
	for _, kv := range got {
		if strings.Contains(kv, "leak") || strings.HasPrefix(kv, "KOC_") {
			t.Errorf("baseEnv kept %q", kv)
		}
	}
	if !slices.Contains(got, "FT_KEEP=yes") {
		t.Error("baseEnv dropped an unrelated variable")
	}
}

func TestRecorderResolvesLeafCommands(t *testing.T) {
	r := &recorder{hits: map[string]int{}}
	r.hit([]string{"network", "list", "-f", "json"})
	r.hit([]string{"network", "list", "--long"})
	r.hit([]string{"server", "show", "web-1", "-c", "id"})
	r.hit([]string{"network"})              // a group, not a leaf
	r.hit([]string{"no-such-command", "x"}) // unknown

	if r.hits["koc network list"] != 2 || r.hits["koc server show"] != 1 {
		t.Errorf("hits = %v", r.hits)
	}
	if len(r.hits) != 2 {
		t.Errorf("groups and unknown commands must not count: %v", r.hits)
	}
}

// The gates, against a made-up cloud. Stands down when a real cloud is
// configured: it swaps the package's cloud and extension set, which the cloud
// tests in the same process read.
func TestGates(t *testing.T) {
	if env != nil {
		t.Skip("gate self-test runs only without a cloud")
	}
	env = &cloud{Series: series{2024, 1}, Backend: "ovs", Features: map[string]bool{"net": true}}
	t.Cleanup(func() { env = nil })
	extOnce.Do(func() { extSet = map[string]bool{"trunk": true} })

	for _, tc := range []struct {
		name string
		gate func(*testing.T)
		runs bool
	}{
		{"feature present", func(t *testing.T) { requireFeature(t, "net") }, true},
		{"feature absent", func(t *testing.T) { requireFeature(t, "dns") }, false},
		{"series reached", func(t *testing.T) { requireSeries(t, "2024.1") }, true},
		{"series not reached", func(t *testing.T) { requireSeries(t, "2025.1") }, false},
		{"backend matches", func(t *testing.T) { requireBackend(t, "ovs") }, true},
		{"backend differs", func(t *testing.T) { requireBackend(t, "ovn") }, false},
		{"extension present", func(t *testing.T) { requireExtension(t, "trunk") }, true},
		{"extension absent", func(t *testing.T) { requireExtension(t, "tap-mirror") }, false},
	} {
		ran := false
		t.Run(tc.name, func(t *testing.T) {
			tc.gate(t)
			ran = true
		})
		if ran != tc.runs {
			t.Errorf("%s: test body ran = %v, want %v", tc.name, ran, tc.runs)
		}
	}
}

func TestResolveLeaf(t *testing.T) {
	root := cli.NewRootCommand("functional")
	for _, tc := range []struct {
		args []string
		want string // "" for no leaf
	}{
		{[]string{"network", "list", "-f", "json"}, "koc network list"},
		{[]string{"--os-system-scope", "all", "endpoint", "list"}, "koc endpoint list"},
		{[]string{"--os-cloud=x", "-f", "json", "server", "show", "web-1"}, "koc server show"},
		{[]string{"--debug", "token", "issue"}, "koc token issue"},
		{[]string{"application", "credential", "create", "x"}, "koc application credential create"},
		{[]string{"network"}, ""},
		{[]string{"no-such-command"}, ""},
	} {
		got := ""
		if c := resolveLeaf(root, tc.args); c != nil {
			got = c.CommandPath()
		}
		if got != tc.want {
			t.Errorf("resolveLeaf(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}
