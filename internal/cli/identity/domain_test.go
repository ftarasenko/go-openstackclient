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

func TestRunDomainList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/domains", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"domains":[
			{"id":"d1","name":"itkey","enabled":true,"description":"main"},
			{"id":"d2","name":"default","enabled":false,"description":""}
		]}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runDomainList(context.Background(), client, o, &buf); err != nil {
		t.Fatalf("runDomainList error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/domains" {
		t.Errorf("path = %q, want /domains", gotPath)
	}
	out := buf.String()
	for _, want := range []string{"ID", "Name", "Enabled", "d1", "itkey", "d2", "default", "true", "false"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunDomainShow_ResolvesNameAndGets(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var listName, getPath, getMethod string
	fakeServer.Mux.HandleFunc("/domains", func(w http.ResponseWriter, r *http.Request) {
		listName = r.URL.Query().Get("name")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"domains":[{"id":"d1","name":"itkey"}]}`))
	})
	fakeServer.Mux.HandleFunc("/domains/d1", func(w http.ResponseWriter, r *http.Request) {
		getMethod = r.Method
		getPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"domain":{"id":"d1","name":"itkey","enabled":true,"description":"main"}}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runDomainShow(context.Background(), client, o, "itkey", &buf); err != nil {
		t.Fatalf("runDomainShow error: %v", err)
	}
	if listName != "itkey" {
		t.Errorf("resolve list name = %q, want itkey", listName)
	}
	if getMethod != http.MethodGet {
		t.Errorf("get method = %q, want GET", getMethod)
	}
	if getPath != "/domains/d1" {
		t.Errorf("get path = %q, want /domains/d1", getPath)
	}
	for _, want := range []string{"d1", "itkey", "main"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunDomainCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/domains", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"domain":{"name":"newdom","description":"a new domain"}}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"domain":{"id":"d-new","name":"newdom","enabled":true,"description":"a new domain"}}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &domainWriteFlags{description: "a new domain"}
	var buf bytes.Buffer
	if err := runDomainCreate(context.Background(), client, o, "newdom", f, &buf); err != nil {
		t.Fatalf("runDomainCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	for _, want := range []string{"d-new", "newdom"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunDomainDelete_ResolvesNameThenDeletes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var delMethod, delPath string
	fakeServer.Mux.HandleFunc("/domains", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"domains":[{"id":"d1","name":"itkey"}]}`))
	})
	fakeServer.Mux.HandleFunc("/domains/d1", func(w http.ResponseWriter, r *http.Request) {
		delMethod = r.Method
		delPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	client := identityClient(fakeServer)
	if err := runDomainDelete(context.Background(), client, "itkey"); err != nil {
		t.Fatalf("runDomainDelete error: %v", err)
	}
	if delMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", delMethod)
	}
	if delPath != "/domains/d1" {
		t.Errorf("path = %q, want /domains/d1", delPath)
	}
}

func TestRunDomainSet_ResolvesNameAndPatches(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var patchMethod string
	fakeServer.Mux.HandleFunc("/domains", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"domains":[{"id":"d1","name":"itkey"}]}`))
	})
	fakeServer.Mux.HandleFunc("/domains/d1", func(w http.ResponseWriter, r *http.Request) {
		patchMethod = r.Method
		th.TestJSONRequest(t, r, `{"domain":{"name":"renamed","description":"updated"}}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"domain":{"id":"d1","name":"renamed","enabled":true,"description":"updated"}}`))
	})

	client := identityClient(fakeServer)
	f := &domainWriteFlags{name: "renamed", description: "updated"}
	if err := runDomainSet(context.Background(), client, "itkey", f, true); err != nil {
		t.Fatalf("runDomainSet error: %v", err)
	}
	if patchMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", patchMethod)
	}
}
