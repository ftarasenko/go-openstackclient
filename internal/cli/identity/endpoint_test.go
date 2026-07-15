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

func TestRunEndpointList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/endpoints", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"endpoints":[
			{"id":"e1","region":"RegionOne","service_id":"svc1","interface":"public","enabled":true,"url":"https://nova"}
		]}`))
	})
	// endpoint list resolves service_id → name/type via the catalog.
	fakeServer.Mux.HandleFunc("/services", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"services":[{"id":"svc1","name":"nova","type":"compute"}]}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	// Empty service/interface/region avoids resolve lookups.
	if err := runEndpointList(context.Background(), client, o, "", "", "", &buf); err != nil {
		t.Fatalf("runEndpointList error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/endpoints" {
		t.Errorf("path = %q, want /endpoints", gotPath)
	}
	// The raw service_id is replaced by the resolved name and type.
	for _, want := range []string{"e1", "RegionOne", "nova", "compute", "public", "https://nova"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
	if strings.Contains(buf.String(), "svc1") {
		t.Errorf("output should not contain raw service_id\n---\n%s", buf.String())
	}
}

func TestRunEndpointShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/endpoints/e1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"endpoint":{"id":"e1","region":"RegionOne","service_id":"svc1","interface":"public","enabled":true,"url":"https://nova"}}`))
	})
	// endpoint show enriches with the service name/type via a per-service Get.
	fakeServer.Mux.HandleFunc("/services/svc1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"service":{"id":"svc1","name":"nova","type":"compute"}}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runEndpointShow(context.Background(), client, o, "e1", &buf); err != nil {
		t.Fatalf("runEndpointShow error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/endpoints/e1" {
		t.Errorf("path = %q, want /endpoints/e1", gotPath)
	}
	for _, want := range []string{"https://nova", "nova", "compute"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunEndpointCreate_ResolvesServiceAndBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	// resolveServiceID matches the name client-side; the resolved service_id in
	// the endpoint create body below proves resolution worked.
	fakeServer.Mux.HandleFunc("/services", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"services":[{"id":"svc1","name":"nova","type":"compute"}]}`))
	})
	fakeServer.Mux.HandleFunc("/endpoints", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"endpoint":{"interface":"public","url":"https://nova","service_id":"svc1"}}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"endpoint":{"id":"e-new","region":"","service_id":"svc1","interface":"public","enabled":true,"url":"https://nova"}}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &endpointWriteFlags{}
	var buf bytes.Buffer
	if err := runEndpointCreate(context.Background(), client, o, "nova", "public", "https://nova", f, &buf); err != nil {
		t.Fatalf("runEndpointCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if !strings.Contains(buf.String(), "e-new") {
		t.Errorf("output missing e-new\n---\n%s", buf.String())
	}
}

func TestRunEndpointDelete_Request(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/endpoints/e1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	client := identityClient(fakeServer)
	if err := runEndpointDelete(context.Background(), client, "e1"); err != nil {
		t.Fatalf("runEndpointDelete error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/endpoints/e1" {
		t.Errorf("path = %q, want /endpoints/e1", gotPath)
	}
}

func TestRunEndpointSet_PatchBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/endpoints/e1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"endpoint":{"region":"RegionTwo","url":"https://new"}}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"endpoint":{"id":"e1","region":"RegionTwo","service_id":"svc1","interface":"public","enabled":true,"url":"https://new"}}`))
	})

	client := identityClient(fakeServer)
	// No --service so resolveServiceID short-circuits on the empty ref.
	f := &endpointWriteFlags{url: "https://new", region: "RegionTwo"}
	if err := runEndpointSet(context.Background(), client, "e1", f); err != nil {
		t.Fatalf("runEndpointSet error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}
}
