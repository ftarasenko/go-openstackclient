package server

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

// TestRunServerSet_UpdateAttributes covers the attributes that ride the single
// server PUT: name, description (2.19+) and hostname (2.90+).
func TestRunServerSet_UpdateAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var puts int
	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID, func(w http.ResponseWriter, r *http.Request) {
		puts++
		gotMethod = r.Method
		gotBody = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"server":{"id":"` + serverUUID + `","name":"renamed"}}`))
	})

	client := computeClient(fakeServer, "2.90")
	f := &serverSetFlags{name: "renamed", description: "web front end", hostname: "web-1.internal"}

	var buf bytes.Buffer
	if err := runServerSet(context.Background(), client, serverUUID, f, &buf); err != nil {
		t.Fatalf("runServerSet: %v", err)
	}
	if puts != 1 {
		t.Errorf("server PUT count = %d, want 1 (all attributes in one update)", puts)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	srv, ok := gotBody["server"].(map[string]any)
	if !ok {
		t.Fatalf("body missing server object: %v", gotBody)
	}
	for field, want := range map[string]string{
		"name":        "renamed",
		"description": "web front end",
		"hostname":    "web-1.internal",
	} {
		if srv[field] != want {
			t.Errorf("server.%s = %v, want %q", field, srv[field], want)
		}
	}
}

// TestRunServerSet_NoUpdateWithoutAttributes ensures "server set --state" alone
// does not issue a pointless empty server PUT.
func TestRunServerSet_NoUpdateWithoutAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var puts int
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID, func(w http.ResponseWriter, _ *http.Request) {
		puts++
		w.WriteHeader(http.StatusOK)
	})
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "2.79")
	var buf bytes.Buffer
	if err := runServerSet(context.Background(), client, serverUUID, &serverSetFlags{state: "error"}, &buf); err != nil {
		t.Fatalf("runServerSet: %v", err)
	}
	if puts != 0 {
		t.Errorf("server PUT count = %d, want 0", puts)
	}
}

func TestRunServerSet_State(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "2.79")
	var buf bytes.Buffer
	if err := runServerSet(context.Background(), client, serverUUID, &serverSetFlags{state: "active"}, &buf); err != nil {
		t.Fatalf("runServerSet: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	reset, ok := gotBody["os-resetState"].(map[string]any)
	if !ok {
		t.Fatalf("body missing os-resetState: %v", gotBody)
	}
	if reset["state"] != "active" {
		t.Errorf("os-resetState.state = %v, want active", reset["state"])
	}
}

func TestRunServerSet_Tags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	got := map[string]string{}
	for _, tag := range []string{"prod", "web"} {
		fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/tags/"+tag, func(w http.ResponseWriter, r *http.Request) {
			got[tag] = r.Method
			// Nova answers 204 when the tag was already present.
			w.WriteHeader(http.StatusCreated)
		})
	}

	client := computeClient(fakeServer, "2.90")
	f := &serverSetFlags{tags: []string{"prod", "web"}}
	var buf bytes.Buffer
	if err := runServerSet(context.Background(), client, serverUUID, f, &buf); err != nil {
		t.Fatalf("runServerSet: %v", err)
	}
	if got["prod"] != http.MethodPut || got["web"] != http.MethodPut {
		t.Errorf("tag methods = %v, want both PUT", got)
	}
}

func TestRunServerSet_Password(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "2.79")
	f := &serverSetFlags{password: "s3cr3t"}
	var buf bytes.Buffer
	if err := runServerSet(context.Background(), client, serverUUID, f, &buf); err != nil {
		t.Fatalf("runServerSet: %v", err)
	}
	change, ok := gotBody["changePassword"].(map[string]any)
	if !ok {
		t.Fatalf("body missing changePassword: %v", gotBody)
	}
	if change["adminPass"] != "s3cr3t" {
		t.Errorf("adminPass = %v, want s3cr3t", change["adminPass"])
	}
}

func TestRunServerSet_NoPassword(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/os-server-password", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	})

	client := computeClient(fakeServer, "2.79")
	f := &serverSetFlags{noPassword: true}
	var buf bytes.Buffer
	if err := runServerSet(context.Background(), client, serverUUID, f, &buf); err != nil {
		t.Fatalf("runServerSet: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
}

// TestRunServerSet_Validation checks that bad values and too-low microversions
// fail before any request reaches nova.
func TestRunServerSet_Validation(t *testing.T) {
	tests := []struct {
		name         string
		microversion string
		flags        *serverSetFlags
		wantErr      string
	}{
		{"bad state", "2.90", &serverSetFlags{state: "shutoff"}, `invalid --state "shutoff"`},
		{"empty tag", "2.90", &serverSetFlags{tags: []string{""}}, "invalid --tag"},
		{"slash in tag", "2.90", &serverSetFlags{tags: []string{"a/b"}}, "invalid --tag"},
		{"description too old", "2.18", &serverSetFlags{description: "x"}, "--description requires compute API microversion 2.19"},
		{"tag too old", "2.25", &serverSetFlags{tags: []string{"prod"}}, "--tag requires compute API microversion 2.26"},
		{"hostname too old", "2.79", &serverSetFlags{hostname: "h"}, "--hostname requires compute API microversion 2.90"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			fakeServer.Mux.HandleFunc("/", func(_ http.ResponseWriter, r *http.Request) {
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			})

			client := computeClient(fakeServer, tt.microversion)
			var buf bytes.Buffer
			err := runServerSet(context.Background(), client, serverUUID, tt.flags, &buf)
			if err == nil {
				t.Fatalf("runServerSet: want error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
