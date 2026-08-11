package network

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const (
	addressScopeID = "aaaa1111-1111-1111-1111-aaaaaaaaaaaa"
	addressGroupID = "bbbb2222-2222-2222-2222-bbbbbbbbbbbb"
)

func TestRunAddressScopeList_SharedFilterSendsBothSides(t *testing.T) {
	for _, tc := range []struct {
		shared, noShared bool
		want             string
	}{
		{true, false, "shared=true"},
		{false, true, "shared=false"},
		{false, false, ""},
	} {
		fakeServer := th.SetupHTTP()

		var gotQuery string
		fakeServer.Mux.HandleFunc("/address-scopes", func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"address_scopes": [{"id": "` + addressScopeID + `", "name": "public",
			  "ip_version": 4, "shared": true}]}`))
		})

		var out bytes.Buffer
		o := &output.Options{Format: "value"}
		err := runAddressScopeList(context.Background(), networkClient(fakeServer), o, "", 0, tc.shared, tc.noShared, &out)
		if err != nil {
			t.Fatalf("runAddressScopeList returned error: %v", err)
		}
		// ListOpts.Shared is a *bool, so --no-share reaches neutron as
		// shared=false instead of being dropped as a zero value.
		if tc.want == "" {
			if strings.Contains(gotQuery, "shared") {
				t.Errorf("no filter requested but query was %q", gotQuery)
			}
		} else if !strings.Contains(gotQuery, tc.want) {
			t.Errorf("query %q is missing %q", gotQuery, tc.want)
		}
		fakeServer.Teardown()
	}
}

func TestRunAddressScopeSet_NoShareSendsExplicitFalse(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/address-scopes/"+addressScopeID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"address_scope": {"id": "` + addressScopeID + `", "name": "public",
		  "ip_version": 4, "shared": false}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runAddressScopeSet(context.Background(), networkClient(fakeServer), o, addressScopeID,
		"", false, true, false, &out)
	if err != nil {
		t.Fatalf("runAddressScopeSet returned error: %v", err)
	}
	scope := body["address_scope"].(map[string]any)
	th.AssertEquals(t, false, scope["shared"])
	if _, present := scope["name"]; present {
		t.Errorf("name sent although --name was not given: %#v", scope)
	}
}

func TestRunAddressGroupCreate_EmptyAddressesStillSent(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/address-groups", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"address_group": {"id": "` + addressGroupID + `", "name": "trusted",
		  "addresses": []}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runAddressGroupCreate(context.Background(), networkClient(fakeServer), o, "trusted", "", "", nil, &out)
	if err != nil {
		t.Fatalf("runAddressGroupCreate returned error: %v", err)
	}
	// gophercloud tags addresses `required`, and neutron wants the key present,
	// so a nil slice has to serialize as an empty array rather than vanish.
	group := body["address_group"].(map[string]any)
	addresses, present := group["addresses"]
	if !present {
		t.Fatalf("addresses omitted from the create body: %#v", group)
	}
	th.AssertDeepEquals(t, []any{}, addresses)
}

func TestRunAddressGroupSet_AddressesUseTheDedicatedAction(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var paths []string
	var addBody map[string]any
	fakeServer.Mux.HandleFunc("/address-groups/"+addressGroupID, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"address_group": {"id": "` + addressGroupID + `", "name": "trusted",
		  "addresses": ["192.0.2.0/24"]}}`))
	})
	fakeServer.Mux.HandleFunc("/address-groups/"+addressGroupID+"/add_addresses", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if err := json.NewDecoder(r.Body).Decode(&addBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"address_group": {"id": "` + addressGroupID + `", "addresses": ["192.0.2.0/24"]}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runAddressGroupSet(context.Background(), networkClient(fakeServer), o, addressGroupID,
		"", "", []string{"192.0.2.0/24"}, false, false, &out)
	if err != nil {
		t.Fatalf("runAddressGroupSet returned error: %v", err)
	}
	// Neutron's plain update has no addresses field at all; adding one goes
	// through the add_addresses action.
	var sawAdd bool
	for _, p := range paths {
		if strings.Contains(p, "add_addresses") {
			sawAdd = true
		}
	}
	if !sawAdd {
		t.Fatalf("--address did not use the add_addresses action; requests were %v", paths)
	}
	th.AssertDeepEquals(t, []any{"192.0.2.0/24"}, addBody["addresses"])
}

func TestRunAddressGroupRemoveAddresses_UsesRemoveAction(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotPath string
	fakeServer.Mux.HandleFunc("/address-groups/"+addressGroupID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"address_group": {"id": "` + addressGroupID + `", "addresses": []}}`))
	})
	fakeServer.Mux.HandleFunc("/address-groups/"+addressGroupID+"/remove_addresses", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"address_group": {"id": "` + addressGroupID + `", "addresses": []}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runAddressGroupRemoveAddresses(context.Background(), networkClient(fakeServer), o, addressGroupID,
		[]string{"192.0.2.0/24"}, &out)
	if err != nil {
		t.Fatalf("runAddressGroupRemoveAddresses returned error: %v", err)
	}
	th.AssertEquals(t, "/address-groups/"+addressGroupID+"/remove_addresses", gotPath)
}

// Neither noun had a name→ID resolver, so `address scope show <name>` put the
// name straight into the URL and neutron answered 404 for a resource that
// `address scope list` had just shown.
func TestRunAddressScopeShow_ResolvesName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var listQuery string
	fakeServer.Mux.HandleFunc("/address-scopes", func(w http.ResponseWriter, r *http.Request) {
		listQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"address_scopes": [{"id": "` + addressScopeID + `", "name": "scope-a"}]}`))
	})
	fakeServer.Mux.HandleFunc("/address-scopes/"+addressScopeID, func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, "GET", r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"address_scope": {"id": "` + addressScopeID + `", "name": "scope-a", "ip_version": 4}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runAddressScopeShow(context.Background(), networkClient(fakeServer), o, "scope-a", &out); err != nil {
		t.Fatalf("runAddressScopeShow returned error: %v", err)
	}
	if !strings.Contains(listQuery, "name=scope-a") {
		t.Errorf("query %q did not filter by name", listQuery)
	}
	if !strings.Contains(out.String(), addressScopeID) {
		t.Errorf("output is missing the resolved scope:\n%s", out.String())
	}
}

func TestRunAddressGroupShow_ResolvesName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/address-groups", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"address_groups": [{"id": "` + addressGroupID + `", "name": "group-a"}]}`))
	})
	fakeServer.Mux.HandleFunc("/address-groups/"+addressGroupID, func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, "GET", r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"address_group": {"id": "` + addressGroupID + `", "name": "group-a",
		  "addresses": ["192.0.2.0/24"]}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runAddressGroupShow(context.Background(), networkClient(fakeServer), o, "group-a", &out); err != nil {
		t.Fatalf("runAddressGroupShow returned error: %v", err)
	}
	if !strings.Contains(out.String(), addressGroupID) {
		t.Errorf("output is missing the resolved group:\n%s", out.String())
	}
}
