package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// rebuildBody runs a rebuild against a mock and returns the decoded "rebuild"
// object plus the raw request body, which is what distinguishes an absent
// user_data from an explicit JSON null.
func rebuildBody(t *testing.T, microversion string, f *serverRebuildFlags) (map[string]any, string, error) {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)

	var raw string
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading rebuild body: %v", err)
		}
		raw = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"server":{"id":"` + serverUUID + `","name":"web-1","status":"REBUILD"}}`))
	})

	var buf bytes.Buffer
	err := runServerRebuild(context.Background(), computeClient(fakeServer, microversion),
		&output.Options{Format: output.FormatTable}, serverUUID, f, &buf)
	if err != nil {
		return nil, raw, err
	}
	var decoded map[string]any
	if raw != "" {
		var body map[string]any
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatalf("decoding rebuild body %q: %v", raw, err)
		}
		decoded, _ = body["rebuild"].(map[string]any)
	}
	return decoded, raw, nil
}

// TestRunServerRebuild_UserData covers nova 2.57's addition: a file replaces the
// server's user data, --no-user-data clears it with a JSON null, and an
// untouched rebuild still omits the field so the server keeps what it has.
func TestRunServerRebuild_UserData(t *testing.T) {
	t.Run("file replaces it", func(t *testing.T) {
		path := userDataFixture(t, "user-data", []byte("runcmd\nls\n"))
		rebuild, _, err := rebuildBody(t, "2.93", &serverRebuildFlags{image: "img-new", userData: path})
		if err != nil {
			t.Fatalf("runServerRebuild: %v", err)
		}
		// The same encoding regression as create: this payload is valid base64
		// once the newlines are dropped.
		if got, want := rebuild["user_data"], "cnVuY21kCmxzCg=="; got != want {
			t.Errorf("user_data = %v, want %v", got, want)
		}
	})

	t.Run("no-user-data clears it", func(t *testing.T) {
		rebuild, raw, err := rebuildBody(t, "2.93", &serverRebuildFlags{image: "img-new", noUserData: true})
		if err != nil {
			t.Fatalf("runServerRebuild: %v", err)
		}
		value, present := rebuild["user_data"]
		if !present || value != nil {
			t.Errorf("user_data = %v (present %v), want an explicit null", value, present)
		}
		// Belt and braces: a decoded nil is indistinguishable from a missing
		// key in some shapes, so assert the literal null reached the wire.
		if !strings.Contains(raw, `"user_data":null`) {
			t.Errorf("request body = %s, want an explicit \"user_data\":null", raw)
		}
	})

	t.Run("untouched keeps it", func(t *testing.T) {
		rebuild, _, err := rebuildBody(t, "2.93", &serverRebuildFlags{image: "img-new"})
		if err != nil {
			t.Fatalf("runServerRebuild: %v", err)
		}
		if _, present := rebuild["user_data"]; present {
			t.Error("a rebuild without the user-data flags must omit the field")
		}
	})

	t.Run("empty file is refused", func(t *testing.T) {
		path := userDataFixture(t, "empty", nil)
		_, raw, err := rebuildBody(t, "2.93", &serverRebuildFlags{image: "img-new", userData: path})
		if err == nil || !strings.Contains(err.Error(), "--no-user-data") {
			t.Fatalf("error = %v, want it to point at --no-user-data", err)
		}
		if raw != "" {
			t.Errorf("nothing should have been sent, got %s", raw)
		}
	})
}

// TestRunServerRebuild_UserDataMicroversion pins the gate to nova's 2.57 rather
// than OSC's 2.54, which its own help text contradicts and nova's schema
// rejects. Below the gate nothing is sent at all.
func TestRunServerRebuild_UserDataMicroversion(t *testing.T) {
	path := userDataFixture(t, "user-data", []byte("#cloud-config\n"))
	for _, tc := range []struct {
		name string
		f    *serverRebuildFlags
		flag string
	}{
		{"user-data", &serverRebuildFlags{image: "img-new", userData: path}, "--user-data"},
		{"no-user-data", &serverRebuildFlags{image: "img-new", noUserData: true}, "--no-user-data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, raw, err := rebuildBody(t, "2.56", tc.f)
			if err == nil || !strings.Contains(err.Error(), tc.flag) ||
				!strings.Contains(err.Error(), rebuildUserDataMicroversion) {
				t.Fatalf("error = %v, want it to name %s and %s", err, tc.flag, rebuildUserDataMicroversion)
			}
			if raw != "" {
				t.Errorf("nothing should have been sent below the microversion, got %s", raw)
			}
			// 2.57 exactly is enough.
			if _, _, err := rebuildBody(t, rebuildUserDataMicroversion, tc.f); err != nil {
				t.Errorf("runServerRebuild at %s: %v", rebuildUserDataMicroversion, err)
			}
		})
	}
}

// TestServerRebuild_UserDataFlagParity pins the option surface against OSC's
// RebuildServer parser, where the two are one mutually exclusive group.
func TestServerRebuild_UserDataFlagParity(t *testing.T) {
	root := NewCommand(&auth.Options{}, &output.Options{})
	leaf, _, err := root.Find([]string{"rebuild"})
	if err != nil || leaf == nil {
		t.Fatalf("server rebuild: not found: %v", err)
	}
	for _, name := range []string{"user-data", "no-user-data"} {
		if leaf.Flags().Lookup(name) == nil {
			t.Fatalf("koc server rebuild: missing --%s", name)
		}
	}
	// The refusal comes from cobra's flag-group validation, which runs before
	// RunE, so this never reaches auth.
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"rebuild", "--image", "img-new", "--user-data", "/dev/null", "--no-user-data", serverUUID})
	if err := root.Execute(); err == nil ||
		!strings.Contains(err.Error(), "none of the others can be") {
		t.Errorf("error = %v, want cobra's mutual-exclusion refusal", err)
	}
}
