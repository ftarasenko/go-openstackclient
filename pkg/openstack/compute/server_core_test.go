package compute_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/keypairs"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"

	"github.com/ftarasenko/koc/pkg/openstack/compute"
)

const otherServerID = "6a7b8c9d-0e1f-4a2b-8c3d-4e5f6a7b8c9d"

func TestServerListOpts_Query(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts compute.ServerListOpts
		want string
	}{
		{"plain", compute.ServerListOpts{ListOpts: servers.ListOpts{Name: "web"}}, "?name=web"},
		{"deleted wins", compute.ServerListOpts{Deleted: true, ExcludeDeleted: true}, "?deleted=true"},
		{"exclude", compute.ServerListOpts{ListOpts: servers.ListOpts{ChangesSince: "2026-01-01T00:00:00Z"}, ExcludeDeleted: true},
			"?changes-since=2026-01-01T00%3A00%3A00Z&deleted=false"},
		{"extension", compute.ServerListOpts{CreatedSince: "a", CreatedBefore: "b", DeletedSince: "c", DeletedBefore: "d", ChangesBefore: "e"},
			"?changes-before=e&created-before=b&created-since=a&deleted-before=d&deleted-since=c"},
	} {
		got, err := tc.opts.ToServerListQuery()
		if err != nil || got != tc.want {
			t.Errorf("%s: = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

// twoPages serves a listing at path in two pages and records each query.
func twoPages(t *testing.T, path string) (*gophercloud.ServiceClient, *[]string) {
	t.Helper()
	fs, sc := nova(t, "2.1")
	var queries []string
	fs.Mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Query().Get("marker") == "" {
			answer(w, http.StatusOK, `{"servers":[{"id":"`+serverID+`","name":"web","OS-EXT-SRV-ATTR:host":"cmp-01"}],
			  "servers_links":[{"rel":"next","href":"`+sc.Endpoint+strings.TrimPrefix(path, "/")+`?marker=`+serverID+`"}]}`)
			return
		}
		answer(w, http.StatusOK, `{"servers":[{"id":"`+otherServerID+`","name":"db"}]}`)
	})
	return sc, &queries
}

func TestListServers_PagesAndCaps(t *testing.T) {
	sc, queries := twoPages(t, "/servers/detail")
	ctx := context.Background()
	all, err := compute.ListServers(ctx, sc, compute.ServerListOpts{ListOpts: servers.ListOpts{AllTenants: true}}, 0)
	if err != nil || len(all) != 2 || all[1].Name != "db" {
		t.Fatalf("= %+v, %v", all, err)
	}
	if strings.Join(*queries, " ") != "all_tenants=true marker="+serverID {
		t.Errorf("queries = %q", *queries)
	}

	*queries = nil
	all, err = compute.ListServers(ctx, sc, servers.ListOpts{}, 1)
	if err != nil || len(all) != 1 || len(*queries) != 1 {
		t.Fatalf("limit 1 = %+v, %v after %q", all, err, *queries)
	}
}

func TestListServerNames_UsesTheSimpleListing(t *testing.T) {
	sc, queries := twoPages(t, "/servers")
	all, err := compute.ListServerNames(context.Background(), sc, servers.ListOpts{Name: "web"}, 0)
	if err != nil || len(all) != 2 || all[0].ID != serverID {
		t.Fatalf("= %+v, %v", all, err)
	}
	if (*queries)[0] != "name=web" {
		t.Errorf("queries = %q", *queries)
	}
}

func TestListServersRaw_KeepsEveryAttribute(t *testing.T) {
	sc, _ := twoPages(t, "/servers/detail")
	all, raw, err := compute.ListServersRaw(context.Background(), sc, servers.ListOpts{}, 0)
	if err != nil || len(all) != 2 || len(raw) != 2 {
		t.Fatalf("= %+v, %v, %v", all, raw, err)
	}
	if raw[serverID]["OS-EXT-SRV-ATTR:host"] != "cmp-01" || raw[otherServerID]["name"] != "db" {
		t.Errorf("raw = %v", raw)
	}
}

func TestListServers_Errors(t *testing.T) {
	fs, sc := nova(t, "2.1")
	fail := func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusBadRequest, `{"badRequest":{"code":400,"message":"Invalid input"}}`)
	}
	fs.Mux.HandleFunc("/servers/detail", fail)
	fs.Mux.HandleFunc("/servers", fail)
	ctx := context.Background()
	_, detailErr := compute.ListServers(ctx, sc, servers.ListOpts{}, 0)
	_, namesErr := compute.ListServerNames(ctx, sc, servers.ListOpts{}, 0)
	_, _, rawErr := compute.ListServersRaw(ctx, sc, servers.ListOpts{}, 0)
	for _, err := range []error{detailErr, namesErr, rawErr} {
		if err == nil || !strings.HasPrefix(err.Error(), "listing servers: ") || !gophercloud.ResponseCodeIs(err, http.StatusBadRequest) {
			t.Errorf("err = %v", err)
		}
	}
}

func TestGetServer(t *testing.T) {
	fs, sc := nova(t, "2.93")
	fs.Mux.HandleFunc("/servers/"+serverID, func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusOK, `{"server":{"id":"`+serverID+`","name":"web","OS-EXT-SRV-ATTR:user_data":"aG9zdG5hbWUK"}}`)
	})
	ctx := context.Background()
	s, err := compute.GetServer(ctx, sc, serverID)
	if err != nil || s.Name != "web" {
		t.Fatalf("GetServer = %+v, %v", s, err)
	}
	m, err := compute.GetServerRaw(ctx, sc, serverID)
	if err != nil || m["OS-EXT-SRV-ATTR:user_data"] != "aG9zdG5hbWUK" {
		t.Fatalf("GetServerRaw = %v, %v", m, err)
	}
}

func TestEncodeUserData(t *testing.T) {
	// "hostname\n" already decodes as base64, which is why it must be encoded
	// before servers.CreateOpts sees it.
	if got := string(compute.EncodeUserData([]byte("hostname\n"))); got != "aG9zdG5hbWUK" {
		t.Errorf("= %q", got)
	}
	if got := compute.EncodeUserData(nil); len(got) != 0 {
		t.Errorf("empty = %q", got)
	}
}

func TestCreateServer(t *testing.T) {
	fs, sc := nova(t, "2.74")
	var rec calls
	fs.Mux.HandleFunc("/servers", rec.handler(func(_ *http.Request, _ string) (int, string) {
		return http.StatusAccepted, `{"server":{"id":"` + serverID + `","adminPass":"generated"}}`
	}))
	base := servers.CreateOpts{Name: "web", FlavorRef: "1", UserData: compute.EncodeUserData([]byte("hostname\n"))}
	opts := compute.ServerCreateOptsExt{
		CreateOptsBuilder: keypairs.CreateOptsExt{CreateOptsBuilder: base, KeyName: "ops"},
		Host:              "cmp-01",
		BlockDevice:       []map[string]any{{"uuid": volID, "source_type": "volume", "destination_type": "volume", "boot_index": 0}},
	}
	s, err := compute.CreateServer(context.Background(), sc, opts, servers.SchedulerHintOpts{Group: groupID})
	if err != nil || s.ID != serverID || s.AdminPass != "generated" {
		t.Fatalf("= %+v, %v", s, err)
	}
	got := rec.only(t)
	want := `{"os:scheduler_hints":{"group":"` + groupID + `"},"server":{"block_device_mapping_v2":[{"boot_index":0,` +
		`"destination_type":"volume","source_type":"volume","uuid":"` + volID + `"}],"flavorRef":"1","host":"cmp-01",` +
		`"imageRef":"","key_name":"ops","name":"web","user_data":"aG9zdG5hbWUK"}}`
	if got.Method != http.MethodPost || got.Version != "compute 2.74" || got.Body != want {
		t.Errorf("sent %+v\nwant body %s", got, want)
	}
}

func TestServerCreateOptsExt_LeavesAPlainCreateAlone(t *testing.T) {
	plain := servers.CreateOpts{Name: "web", FlavorRef: "1"}
	want, _ := plain.ToServerCreateMap()
	got, err := compute.ServerCreateOptsExt{CreateOptsBuilder: plain}.ToServerCreateMap()
	if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("= %v, %v; want %v", got, err, want)
	}
}

func TestServerWrites(t *testing.T) {
	fs, sc := nova(t, "2.90")
	var rec calls
	ok := rec.handler(func(r *http.Request, _ string) (int, string) {
		switch {
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/tags"):
			return http.StatusNoContent, ""
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/os-server-password"):
			return http.StatusNoContent, ""
		case r.Method == http.MethodDelete:
			return http.StatusNoContent, ""
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/tags/"):
			return http.StatusCreated, ""
		case strings.HasSuffix(r.URL.Path, "/action"):
			return http.StatusAccepted, ""
		case strings.Contains(r.URL.Path, "/metadata"):
			return http.StatusOK, `{"metadata":{"k":"v"}}`
		}
		return http.StatusOK, `{"server":{"id":"` + serverID + `","name":"web"}}`
	})
	for _, p := range []string{"", "/action", "/metadata", "/metadata/k", "/os-server-password", "/tags", "/tags/blue"} {
		fs.Mux.HandleFunc("/servers/"+serverID+p, ok)
	}
	ctx := context.Background()
	desc, empty := "front end", ""
	cases := []struct {
		name string
		call func() error
		want call
	}{
		{"delete", func() error { return compute.DeleteServer(ctx, sc, serverID) },
			call{Method: "DELETE", Path: "/servers/" + serverID}},
		{"force delete", func() error { return compute.ForceDeleteServer(ctx, sc, serverID) },
			call{Method: "POST", Path: "/servers/" + serverID + "/action", Body: `{"forceDelete":""}`}},
		{"update", func() error {
			_, err := compute.UpdateServer(ctx, sc, serverID, compute.ServerUpdateOpts{Name: "web2", Description: &desc, Hostname: &empty})
			return err
		}, call{Method: "PUT", Path: "/servers/" + serverID, Body: `{"server":{"description":"front end","hostname":"","name":"web2"}}`}},
		{"az", func() error { return compute.SetServerAvailabilityZone(ctx, sc, serverID, "az2") },
			call{Method: "PUT", Path: "/servers/" + serverID, Body: `{"server":{"availability_zone":"az2"}}`}},
		{"metadata", func() error {
			_, err := compute.UpdateServerMetadata(ctx, sc, serverID, map[string]string{"k": "v"})
			return err
		}, call{Method: "POST", Path: "/servers/" + serverID + "/metadata", Body: `{"metadata":{"k":"v"}}`}},
		{"replace metadata", func() error {
			_, err := compute.ReplaceServerMetadata(ctx, sc, serverID, map[string]string{})
			return err
		}, call{Method: "PUT", Path: "/servers/" + serverID + "/metadata", Body: `{"metadata":{}}`}},
		{"delete metadatum", func() error { return compute.DeleteServerMetadatum(ctx, sc, serverID, "k") },
			call{Method: "DELETE", Path: "/servers/" + serverID + "/metadata/k"}},
		{"password", func() error { return compute.ChangeAdminPassword(ctx, sc, serverID, "example") },
			call{Method: "POST", Path: "/servers/" + serverID + "/action", Body: `{"changePassword":{"adminPass":"example"}}`}},
		{"clear password", func() error { return compute.ClearServerPassword(ctx, sc, serverID) },
			call{Method: "DELETE", Path: "/servers/" + serverID + "/os-server-password"}},
		{"reset state", func() error { return compute.ResetServerState(ctx, sc, serverID, servers.StateError) },
			call{Method: "POST", Path: "/servers/" + serverID + "/action", Body: `{"os-resetState":{"state":"error"}}`}},
		{"add tag", func() error { return compute.AddServerTag(ctx, sc, serverID, "blue") },
			call{Method: "PUT", Path: "/servers/" + serverID + "/tags/blue"}},
		{"delete tag", func() error { return compute.DeleteServerTag(ctx, sc, serverID, "blue") },
			call{Method: "DELETE", Path: "/servers/" + serverID + "/tags/blue"}},
		{"delete tags", func() error { return compute.DeleteServerTags(ctx, sc, serverID) },
			call{Method: "DELETE", Path: "/servers/" + serverID + "/tags"}},
	}
	for _, tc := range cases {
		if err := tc.call(); err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		got := rec.only(t)
		tc.want.Version = "compute 2.90"
		if got != tc.want {
			t.Errorf("%s sent %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestServerCalls_ErrorsNameTheID(t *testing.T) {
	fs, sc := nova(t, "2.93")
	fs.Mux.HandleFunc("/servers", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusForbidden, `{"forbidden":{"code":403,"message":"Quota exceeded"}}`)
	})
	notFound := func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusNotFound, `{"itemNotFound":{"code":404,"message":"Instance could not be found."}}`)
	}
	for _, p := range []string{"", "/action", "/metadata", "/metadata/k", "/os-server-password", "/tags", "/tags/blue"} {
		fs.Mux.HandleFunc("/servers/"+serverID+p, notFound)
	}
	ctx := context.Background()
	ignore := func(_ any, err error) error { return err }
	for want, err := range map[string]error{
		"reading server " + serverID:                             ignore(compute.GetServer(ctx, sc, serverID)),
		"showing server " + serverID:                             ignore(compute.GetServerRaw(ctx, sc, serverID)),
		"deleting server " + serverID:                            compute.DeleteServer(ctx, sc, serverID),
		"updating server " + serverID:                            ignore(compute.UpdateServer(ctx, sc, serverID, compute.ServerUpdateOpts{Name: "x"})),
		"updating availability zone on server " + serverID:       compute.SetServerAvailabilityZone(ctx, sc, serverID, "az2"),
		"updating metadata on server " + serverID:                ignore(compute.UpdateServerMetadata(ctx, sc, serverID, map[string]string{"k": "v"})),
		"replacing metadata on server " + serverID:               ignore(compute.ReplaceServerMetadata(ctx, sc, serverID, nil)),
		`removing metadata "k" from server ` + serverID:          compute.DeleteServerMetadatum(ctx, sc, serverID, "k"),
		"changing admin password on server " + serverID:          compute.ChangeAdminPassword(ctx, sc, serverID, "example"),
		"clearing admin password on server " + serverID:          compute.ClearServerPassword(ctx, sc, serverID),
		`resetting state of server ` + serverID + ` to "active"`: compute.ResetServerState(ctx, sc, serverID, servers.StateActive),
		`adding tag "blue" to server ` + serverID:                compute.AddServerTag(ctx, sc, serverID, "blue"),
		`removing tag "blue" from server ` + serverID:            compute.DeleteServerTag(ctx, sc, serverID, "blue"),
		"removing all tags from server " + serverID:              compute.DeleteServerTags(ctx, sc, serverID),
	} {
		if err == nil || !strings.HasPrefix(err.Error(), want+": ") || !gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
			t.Errorf("err = %v, want %q wrapping the 404", err, want)
		}
	}
	_, err := compute.CreateServer(ctx, sc, servers.CreateOpts{Name: "web", FlavorRef: "1"}, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "creating server: ") || !gophercloud.ResponseCodeIs(err, http.StatusForbidden) {
		t.Errorf("create err = %v", err)
	}
}
