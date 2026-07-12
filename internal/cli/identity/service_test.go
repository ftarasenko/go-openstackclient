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

func TestRunServiceList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/services", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"services":[
			{"id":"svc1","name":"nova","type":"compute","enabled":true,"description":"Nova"},
			{"id":"svc2","name":"glance","type":"image","enabled":false,"description":""}
		]}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runServiceList(context.Background(), client, o, &buf); err != nil {
		t.Fatalf("runServiceList error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/services" {
		t.Errorf("path = %q, want /services", gotPath)
	}
	for _, want := range []string{"svc1", "nova", "compute", "svc2", "glance", "image"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunServiceShow_ResolvesNameAndGets(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var listName, getPath string
	fakeServer.Mux.HandleFunc("/services", func(w http.ResponseWriter, r *http.Request) {
		listName = r.URL.Query().Get("name")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"services":[{"id":"svc1","name":"nova","type":"compute"}]}`))
	})
	fakeServer.Mux.HandleFunc("/services/svc1", func(w http.ResponseWriter, r *http.Request) {
		getPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"service":{"id":"svc1","name":"nova","type":"compute","enabled":true,"description":"Nova"}}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runServiceShow(context.Background(), client, o, "nova", &buf); err != nil {
		t.Fatalf("runServiceShow error: %v", err)
	}
	if listName != "nova" {
		t.Errorf("resolve name = %q, want nova", listName)
	}
	if getPath != "/services/svc1" {
		t.Errorf("get path = %q, want /services/svc1", getPath)
	}
	for _, want := range []string{"svc1", "nova", "compute"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}
