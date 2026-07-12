package identity

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestRunCatalogList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/auth/catalog", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"catalog":[
			{"id":"c1","name":"nova","type":"compute","endpoints":[
				{"id":"e1","interface":"public","url":"https://nova","region":"RegionOne"}
			]}
		]}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runCatalogList(context.Background(), client, o, &buf); err != nil {
		t.Fatalf("runCatalogList error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/auth/catalog" {
		t.Errorf("path = %q, want /auth/catalog", gotPath)
	}
	for _, want := range []string{"nova", "compute", "public", "https://nova", "RegionOne"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunGroupList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/groups", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"groups":[
			{"id":"g1","name":"admins","domain_id":"default","description":"admin group"}
		]}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	// Empty domain avoids the resolve lookup.
	if err := runGroupList(context.Background(), client, o, "", &buf); err != nil {
		t.Fatalf("runGroupList error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/groups" {
		t.Errorf("path = %q, want /groups", gotPath)
	}
	for _, want := range []string{"g1", "admins", "default", "admin group"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunRegionList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/regions", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"regions":[
			{"id":"RegionOne","parent_region_id":"","description":"primary region"}
		]}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runRegionList(context.Background(), client, o, &buf); err != nil {
		t.Fatalf("runRegionList error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/regions" {
		t.Errorf("path = %q, want /regions", gotPath)
	}
	for _, want := range []string{"RegionOne", "primary region"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunTokenIssue_RequestHeaderAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const token = "abc-token-123"
	var gotMethod, gotPath, gotSubject string
	fakeServer.Mux.HandleFunc("/auth/tokens", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotSubject = r.Header.Get("X-Subject-Token")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"token":{
			"expires_at":"2026-12-31T23:59:59Z",
			"user":{"id":"u1","name":"admin"},
			"project":{"id":"p1","name":"demo"}
		}}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runTokenIssue(context.Background(), client, o, token, &buf); err != nil {
		t.Fatalf("runTokenIssue error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/auth/tokens" {
		t.Errorf("path = %q, want /auth/tokens", gotPath)
	}
	if gotSubject != token {
		t.Errorf("X-Subject-Token = %q, want %q", gotSubject, token)
	}
	// ID falls back to the submitted token (introspection does not echo the header).
	for _, want := range []string{token, "2026-12-31T23:59:59Z", "p1", "u1"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}
