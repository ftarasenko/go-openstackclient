package network

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/floatingips"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

func TestParseExtraProperties_ConvertsEachUpstreamType(t *testing.T) {
	got, err := parseExtraProperties([]string{
		"name=plain,value=text",
		"type=str,name=s,value=x=y",
		"type=int,name=n,value=42",
		"type=bool,name=b,value=True",
		"type=bool,name=nb,value=yes",
		"type=list,name=l,value=a;b;c",
		"type=dict,name=d,value=k1:v1;k2:v2;tail",
	}, false)
	if err != nil {
		t.Fatalf("parseExtraProperties: %v", err)
	}
	want := map[string]any{
		"plain": "text",
		"s":     "x=y",
		"n":     42,
		"b":     true,
		// str2bool: anything but "true" is false.
		"nb": false,
		"l":  []string{"a", "b", "c"},
		// A ';' piece without ':' belongs to the previous value (str2dict).
		"d": map[string]string{"k1": "v1", "k2": "v2;tail"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseExtraProperties =\n%#v\nwant\n%#v", got, want)
	}
}

func TestParseExtraProperties_Errors(t *testing.T) {
	for _, spec := range []string{
		"value=x",                   // no name
		"name=x",                    // no value on create/set
		"type=float,name=x,value=1", // unsupported type
		"type=int,name=x,value=one", // not an int
		"name=x,value=1,color=red",  // unknown key
		"type=dict,name=d,value=nokey",
		"garbage",
	} {
		if _, err := parseExtraProperties([]string{spec}, false); err == nil {
			t.Errorf("parseExtraProperties(%q) = nil error, want one", spec)
		}
	}
}

func TestParseExtraProperties_UnsetSendsNull(t *testing.T) {
	got, err := parseExtraProperties([]string{"name=a", "name=b,value=ignored"}, true)
	if err != nil {
		t.Fatalf("parseExtraProperties: %v", err)
	}
	if !reflect.DeepEqual(got, map[string]any{"a": nil, "b": nil}) {
		t.Errorf("unset properties = %#v", got)
	}
}

func TestBodyExt_ExtraAttributesWinOverTypedOnes(t *testing.T) {
	desc := "typed"
	b, err := withFloatingIPUpdateAttrs(floatingips.UpdateOpts{Description: &desc},
		map[string]any{"description": "extra", "qos_policy_id": nil}).ToFloatingIPUpdateMap()
	if err != nil {
		t.Fatalf("ToFloatingIPUpdateMap: %v", err)
	}
	raw, _ := json.Marshal(b)
	if string(raw) != `{"floatingip":{"description":"extra","qos_policy_id":null}}` {
		t.Errorf("body = %s", raw)
	}
}

func TestWithQueryValues_RepeatsKeys(t *testing.T) {
	q, err := withQueryValues("?status=ACTIVE", nil, url.Values{"router_id": {"r1", "r2"}})
	if err != nil {
		t.Fatalf("withQueryValues: %v", err)
	}
	got, _ := url.ParseQuery(strings.TrimPrefix(q, "?"))
	if !reflect.DeepEqual(got["router_id"], []string{"r1", "r2"}) || got.Get("status") != "ACTIVE" {
		t.Errorf("query = %q", q)
	}
}

func TestTagWriteFlags_SetAndUnsetArithmetic(t *testing.T) {
	cur := []string{"b", "a"}
	for _, tc := range []struct {
		name string
		f    tagWriteFlags
		set  bool
		want []string
	}{
		{"set adds and sorts", tagWriteFlags{tags: []string{"c", "a"}}, true, []string{"a", "b", "c"}},
		{"set no-tag clears", tagWriteFlags{noTag: true}, true, []string{}},
		{"set no-tag+tag overwrites", tagWriteFlags{noTag: true, tags: []string{"z"}}, true, []string{"z"}},
		{"unset removes", tagWriteFlags{tags: []string{"a", "missing"}}, false, []string{"b"}},
		{"unset all-tag clears", tagWriteFlags{allTag: true}, false, []string{}},
	} {
		var got []string
		if tc.set {
			got = tc.f.forSet(cur)
		} else {
			got = tc.f.forUnset(cur)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestReplaceTags_PutsOnlyOnChange(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	puts := 0
	fakeServer.Mux.HandleFunc("/networks/net-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["a","b"]}`)
		puts++
		writeJSON(t, w, http.StatusOK, `{"tags":["a","b"]}`)
	})
	client := networkClient(fakeServer)
	ctx := context.Background()

	got, err := applyTagsForSet(ctx, client, tagResourceNetworks, "net-1", []string{"a"}, &tagWriteFlags{tags: []string{"b"}})
	if err != nil || !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("applyTagsForSet = %v, %v", got, err)
	}
	// Already carrying the requested set: nothing is sent.
	if _, err := applyTagsForSet(ctx, client, tagResourceNetworks, "net-1", []string{"b", "a"}, &tagWriteFlags{tags: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	// No tag flag at all: nothing is sent.
	if _, err := applyTagsForUnset(ctx, client, tagResourceNetworks, "net-1", []string{"a"}, &tagWriteFlags{}); err != nil {
		t.Fatal(err)
	}
	if puts != 1 {
		t.Errorf("tag PUTs = %d, want 1", puts)
	}
}

// gophercloud reads the If-Match header off the top-level fields of the builder
// handed to Update. Every update adapter re-declares RevisionNumber so a
// wrapped update keeps neutron's revision guard.
func TestUpdateAdapters_KeepTheIfMatchGuard(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/floatingips/fip-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestHeader(t, r, "If-Match", "revision_number=7")
		th.TestJSONRequest(t, r, `{"floatingip":{"qos_policy_id":null}}`)
		writeJSON(t, w, http.StatusOK, `{"floatingip":{"id":"fip-1"}}`)
	})
	rev := 7
	opts := floatingips.UpdateOpts{RevisionNumber: &rev}
	b := withFloatingIPUpdateAttrs(opts, map[string]any{"qos_policy_id": nil})
	if b.RevisionNumber == nil || *b.RevisionNumber != 7 {
		t.Fatalf("adapter RevisionNumber = %v, want 7", b.RevisionNumber)
	}
	if err := floatingips.Update(context.Background(), networkClient(fakeServer), "fip-1", b).Err; err != nil {
		t.Fatalf("Update: %v", err)
	}
}

func TestExplainMissingExtension_NamesTheAbsentExtension(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/extensions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"extensions":[{"alias":"qos"},{"alias":"router"}]}`)
	})
	fakeServer.Mux.HandleFunc("/routers", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, `{"NeutronError":{"message":"Unrecognized attribute(s) 'enable_ndp_proxy'"}}`)
	})
	client := networkClient(fakeServer)
	ctx := context.Background()
	resp, httpErr := client.Post(ctx, client.ServiceURL("routers"), map[string]any{}, nil, nil)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if httpErr == nil {
		t.Fatal("expected the mock 400")
	}

	err := explainMissingExtension(ctx, client, httpErr, map[string]any{
		"enable_ndp_proxy": true, "qos_policy_id": "q", "name": "r1",
	})
	msg := err.Error()
	if !strings.Contains(msg, "l3-ext-ndp-proxy (for enable_ndp_proxy)") {
		t.Errorf("error does not name the missing extension:\n%s", msg)
	}
	if strings.Contains(msg, "qos (") {
		t.Errorf("error blames an enabled extension:\n%s", msg)
	}
	var unwrapped gophercloud.ErrUnexpectedResponseCode
	if !errors.As(err, &unwrapped) || unwrapped.Actual != http.StatusBadRequest {
		t.Error("the original HTTP error must stay wrapped")
	}

	// Nothing to explain: no extension attribute, or a non-HTTP error.
	if got := explainMissingExtension(ctx, client, httpErr, map[string]any{"name": "r1"}); got.Error() != httpErr.Error() {
		t.Errorf("error without extension attributes was rewritten: %v", got)
	}
	plain := errors.New("boom")
	if got := explainMissingExtension(ctx, client, plain, map[string]any{"enable_ndp_proxy": true}); got.Error() != "boom" {
		t.Errorf("non-HTTP error was rewritten: %v", got)
	}
}
