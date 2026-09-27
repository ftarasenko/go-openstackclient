//go:build functional

package functional

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every way an operator hands koc credentials, against a real Keystone. The
// suites run under one default path (KOC_FT_AUTH); this file runs all of them,
// on every cell.
//
// env and clouds.yaml are the default paths and get the widest check: token,
// neutron, and — on core cells — cinder and nova, because devstack's openrc
// exports a bare OS_VOLUME_API_VERSION=3 and ID-only domains, the two things
// koc used to get wrong on the env path.

// token is `koc token issue`: which project and user the credentials scoped to.
type token struct{ project, user string }

func issue(t *testing.T, r runner, args ...string) token {
	t.Helper()
	var m map[string]any
	r.json(t, &m, append(args, "token", "issue")...)
	return token{project: str(m["project_id"]), user: str(m["user_id"])}
}

func openrcValue(c *cloud, key string) string {
	for _, kv := range c.Openrc {
		if k, v, _ := strings.Cut(kv, "="); k == key {
			return v
		}
	}
	return ""
}

func without(kvs []string, keys ...string) []string {
	var out []string
	for _, kv := range kvs {
		k, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, d := range keys {
			drop = drop || k == d
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

func TestAuthPaths(t *testing.T) {
	c := ft(t)
	admin := issue(t, cloudsRunner(c))
	if admin.project == "" || admin.user == "" {
		t.Fatalf("clouds.yaml token has no project/user: %+v", admin)
	}

	flagArgs := []string{
		"--os-auth-url", openrcValue(c, "OS_AUTH_URL"),
		"--os-username", openrcValue(c, "OS_USERNAME"),
		"--os-password", openrcValue(c, "OS_PASSWORD"),
		"--os-project-name", openrcValue(c, "OS_PROJECT_NAME"),
		"--os-user-domain-id", openrcValue(c, "OS_USER_DOMAIN_ID"),
		"--os-project-domain-id", openrcValue(c, "OS_PROJECT_DOMAIN_ID"),
		"--os-region-name", openrcValue(c, "OS_REGION_NAME"),
	}
	paths := []struct {
		name string
		r    runner
		args []string
	}{
		{"clouds.yaml", cloudsRunner(c), nil},
		{"env openrc", openrcRunner(c), nil},
		{"flags only", runner{env: baseEnv()}, flagArgs},
		{"password on stdin", runner{env: append(baseEnv(), without(c.Openrc, "OS_PASSWORD")...)}.
			withStdin(openrcValue(c, "OS_PASSWORD") + "\n"), []string{"--os-password-stdin"}},
	}
	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			if got := issue(t, p.r, p.args...); got != admin {
				t.Errorf("token = %+v, want the same scope as clouds.yaml %+v", got, admin)
			}
			p.r.ok(t, append(p.args, "network", "list")...)
			if c.Features["core"] {
				p.r.ok(t, append(p.args, "volume", "list")...) // cinder: the bare-major microversion
				p.r.ok(t, append(p.args, "server", "list")...)
			}
		})
	}
}

// A named cloud outranks leftover OS_* in the environment, and an explicit flag
// outranks both — the rule auth.Options.override implements.
func TestAuthPrecedence(t *testing.T) {
	c := ft(t)
	admin := issue(t, cloudsRunner(c))

	var projects []map[string]any
	cloudsRunner(c).json(t, &projects, "project", "list")
	demo := ""
	for _, p := range projects {
		if str(p["Name"]) == "demo" {
			demo = str(p["ID"])
		}
	}
	if demo == "" {
		t.Fatal("devstack's demo project is missing")
	}

	leftover := cloudsRunner(c).with("OS_PROJECT_NAME=demo", "OS_USERNAME=demo")
	if got := issue(t, leftover); got != admin {
		t.Errorf("OS_CLOUD with leftover OS_PROJECT_NAME=demo scoped to %+v, want the named cloud's %+v", got, admin)
	}
	// devstack gives admin a role on demo, so the flag can move the scope there.
	if got := issue(t, cloudsRunner(c), "--os-project-name", "demo"); got.project != demo {
		t.Errorf("--os-project-name demo over OS_CLOUD scoped to %s, want demo (%s)", got.project, demo)
	}
}

func TestAuthApplicationCredential(t *testing.T) {
	c := ft(t)
	r := defaultRunner(c)
	admin := issue(t, r)

	name := fmt.Sprintf("ft-appcred-%d", time.Now().UnixNano())
	var cred map[string]any
	r.json(t, &cred, "application", "credential", "create", name)
	t.Cleanup(func() { r.run(t, "application", "credential", "delete", name) })
	id, secret := str(cred["ID"]), str(cred["Secret"])
	if id == "" || secret == "" {
		t.Fatalf("application credential create returned %v", cred)
	}

	authURL, region := openrcValue(c, "OS_AUTH_URL"), openrcValue(c, "OS_REGION_NAME")
	t.Run("env", func(t *testing.T) {
		env := runner{env: append(baseEnv(),
			"OS_AUTH_URL="+authURL, "OS_REGION_NAME="+region,
			"OS_APPLICATION_CREDENTIAL_ID="+id, "OS_APPLICATION_CREDENTIAL_SECRET="+secret)}
		if got := issue(t, env); got.project != admin.project {
			t.Errorf("token = %+v, want the credential's project %s", got, admin.project)
		}
		env.ok(t, "network", "list")
	})
	t.Run("clouds.yaml", func(t *testing.T) {
		cfg := writeFile(t, t.TempDir(), "clouds.yaml", fmt.Sprintf(`clouds:
  ft-appcred:
    auth_type: v3applicationcredential
    region_name: %s
    auth:
      auth_url: %s
      application_credential_id: %s
      application_credential_secret: %s
`, region, authURL, id, secret))
		yaml := runner{env: append(baseEnv(), "OS_CLIENT_CONFIG_FILE="+filepath.Clean(cfg), "OS_CLOUD=ft-appcred")}
		if got := issue(t, yaml); got.project != admin.project {
			t.Errorf("token = %+v, want the credential's project %s", got, admin.project)
		}
		yaml.ok(t, "network", "list")
	})
}

func TestAuthSystemScope(t *testing.T) {
	c := ft(t)
	got := issue(t, defaultRunner(c), "--os-system-scope", "all")
	if got.project != "" || got.user == "" {
		t.Errorf("system-scoped token = %+v, want a user and no project", got)
	}
	defaultRunner(c).ok(t, "--os-system-scope", "all", "endpoint", "list")
}
