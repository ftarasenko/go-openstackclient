package ratelimit_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/ratelimit"
)

func fake(t *testing.T, status int, body string, query *string) *gophercloud.ServiceClient {
	t.Helper()
	fs := th.SetupHTTP()
	t.Cleanup(fs.Teardown)
	fs.Mux.HandleFunc("/limits", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		*query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
	return fakeclient.ServiceClient(fs)
}

func TestList(t *testing.T) {
	var query string
	sc := fake(t, http.StatusOK, `{"limits":{"absolute":{},"rate":[
		{"uri":"*","regex":".*","limit":[
			{"verb":"POST","value":10,"remaining":9,"unit":"MINUTE","next-available":"2026-01-02T03:04:05Z"},
			{"verb":"GET","value":100,"remaining":100,"unit":"MINUTE"}]},
		{"uri":"*/volumes","regex":"^/volumes","limit":[{"verb":"DELETE","value":5,"remaining":5,"unit":"HOUR"}]}]}}`, &query)
	got, err := ratelimit.List(context.Background(), sc, "")
	if err != nil || len(got) != 3 {
		t.Fatalf("List = %+v, %v; want one entry per verb", got, err)
	}
	want := ratelimit.Limit{URI: "*", Regex: ".*", Verb: "POST", Value: 10, Remaining: 9, Unit: "MINUTE", NextAvailable: "2026-01-02T03:04:05Z"}
	if got[0] != want || got[2].URI != "*/volumes" || got[2].Verb != "DELETE" {
		t.Errorf("limits = %+v", got)
	}
	if query != "" {
		t.Errorf("query = %q; want none without a project", query)
	}
}

func TestList_ProjectIsEscaped(t *testing.T) {
	var query string
	sc := fake(t, http.StatusOK, `{"limits":{"rate":[]}}`, &query)
	got, err := ratelimit.List(context.Background(), sc, "a b&c")
	if err != nil || len(got) != 0 {
		t.Fatalf("List = %+v, %v; want none, not an error", got, err)
	}
	if query != "tenant_id=a+b%26c" {
		t.Errorf("query = %q", query)
	}
}

func TestList_Error(t *testing.T) {
	var query string
	sc := fake(t, http.StatusForbidden, `{"forbidden":{"message":"no"}}`, &query)
	_, err := ratelimit.List(context.Background(), sc, "")
	if !gophercloud.ResponseCodeIs(err, http.StatusForbidden) || !strings.HasPrefix(err.Error(), "getting rate limits: ") {
		t.Errorf("List = %v; want the 403, named", err)
	}
}
