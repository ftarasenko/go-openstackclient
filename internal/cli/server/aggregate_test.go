package server

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestRunAggregateList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/os-aggregates", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"aggregates":[
			{"id":1,"name":"agg-a","availability_zone":"az1","hosts":["h1","h2"],"metadata":{"az":"az1","ssd":"true"}},
			{"id":2,"name":"agg-b","availability_zone":null,"hosts":[],"metadata":{}}
		]}`))
	})

	client := computeClient(fakeServer, "2.79")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runAggregateList(context.Background(), client, o, true, &buf); err != nil {
		t.Fatalf("runAggregateList: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	out := buf.String()
	for _, want := range []string{"agg-a", "az1", "h1, h2", "ssd='true'", "agg-b"} {
		if !strings.Contains(out, want) {
			t.Errorf("aggregate list output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunAggregateCreate_WithProperty(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var createBody, actionBody map[string]any
	fakeServer.Mux.HandleFunc("/os-aggregates", func(w http.ResponseWriter, r *http.Request) {
		createBody = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"aggregate":{"id":7,"name":"agg-new","availability_zone":"az1","hosts":[],"metadata":{}}}`))
	})
	fakeServer.Mux.HandleFunc("/os-aggregates/7/action", func(w http.ResponseWriter, r *http.Request) {
		actionBody = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"aggregate":{"id":7,"name":"agg-new","availability_zone":"az1","hosts":[],"metadata":{"ssd":"true"}}}`))
	})
	fakeServer.Mux.HandleFunc("/os-aggregates/7", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"aggregate":{"id":7,"name":"agg-new","availability_zone":"az1","hosts":[],"metadata":{"ssd":"true"}}}`))
	})

	client := computeClient(fakeServer, "2.79")
	o := &output.Options{Format: output.FormatTable}
	f := &aggregateCreateFlags{zone: "az1", properties: []string{"ssd=true"}}
	var buf bytes.Buffer
	if err := runAggregateCreate(context.Background(), client, o, "agg-new", f, &buf); err != nil {
		t.Fatalf("runAggregateCreate: %v", err)
	}
	agg, _ := createBody["aggregate"].(map[string]any)
	if agg["name"] != "agg-new" || agg["availability_zone"] != "az1" {
		t.Errorf("create body aggregate = %v, want name=agg-new zone=az1", agg)
	}
	sm, _ := actionBody["set_metadata"].(map[string]any)
	meta, _ := sm["metadata"].(map[string]any)
	if meta["ssd"] != "true" {
		t.Errorf("set_metadata body = %v, want metadata.ssd=true", actionBody)
	}
	if !strings.Contains(buf.String(), "ssd='true'") {
		t.Errorf("create output missing property:\n%s", buf.String())
	}
}

func TestRunAggregateAddHost_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/os-aggregates/5/action", func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"aggregate":{"id":5,"name":"agg-a","availability_zone":"az1","hosts":["cmp-1"],"metadata":{}}}`))
	})

	client := computeClient(fakeServer, "2.79")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	// A numeric ref is used verbatim, sparing the test from mocking the list.
	if err := runAggregateAddHost(context.Background(), client, o, "5", "cmp-1", &buf); err != nil {
		t.Fatalf("runAggregateAddHost: %v", err)
	}
	ah, _ := gotBody["add_host"].(map[string]any)
	if ah["host"] != "cmp-1" {
		t.Errorf("add_host body = %v, want host=cmp-1", gotBody)
	}
	if !strings.Contains(buf.String(), "cmp-1") {
		t.Errorf("add host output missing host:\n%s", buf.String())
	}
}

// TestAggregateCommandPaths guards the two-word OSC nouns
// "aggregate add host" and "aggregate remove host".
func TestAggregateCommandPaths(t *testing.T) {
	agg := newAggregateCommand(nil, nil)
	for _, tc := range []struct{ path []string }{
		{[]string{"add", "host", "agg-a", "cmp-1"}},
		{[]string{"remove", "host", "agg-a", "cmp-1"}},
	} {
		leaf, rest, err := agg.Find(tc.path)
		if err != nil {
			t.Fatalf("Find(%v): %v", tc.path, err)
		}
		if leaf.Name() != "host" {
			t.Errorf("Find(%v) resolved to %q, want leaf %q", tc.path, leaf.Name(), "host")
		}
		if err := leaf.Args(leaf, rest); err != nil {
			t.Errorf("Find(%v) left args %v, which fail the leaf's Args check: %v", tc.path, rest, err)
		}
	}
}
