package compute_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servergroups"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/compute"
)

const groupJSON = `{"id":"` + groupID + `","name":"web","policy":"anti-affinity","rules":{"max_server_per_host":2},"members":["` + serverID + `"]}`

func TestServerGroups(t *testing.T) {
	fs, sc := nova(t, "2.64")
	var rec calls
	fs.Mux.HandleFunc("/os-server-groups", rec.handler(func(r *http.Request, _ string) (int, string) {
		if r.Method == http.MethodPost {
			return http.StatusOK, `{"server_group":` + groupJSON + `}`
		}
		return http.StatusOK, `{"server_groups":[` + groupJSON + `,{"id":"g2","name":"db"}]}`
	}))
	fs.Mux.HandleFunc("/os-server-groups/"+groupID, rec.handler(func(r *http.Request, _ string) (int, string) {
		if r.Method == http.MethodDelete {
			return http.StatusNoContent, ""
		}
		return http.StatusOK, `{"server_group":` + groupJSON + `}`
	}))
	ctx := context.Background()

	all, err := compute.ListServerGroups(ctx, sc, servergroups.ListOpts{AllProjects: true}, 1)
	if err != nil || len(all) != 1 || all[0].Name != "web" {
		t.Fatalf("ListServerGroups = %+v, %v", all, err)
	}
	rec.only(t)

	g, err := compute.GetServerGroup(ctx, sc, groupID)
	if err != nil || g.Rules == nil || g.Rules.MaxServerPerHost != 2 {
		t.Fatalf("GetServerGroup = %+v, %v", g, err)
	}
	rec.only(t)

	_, err = compute.CreateServerGroup(ctx, sc, servergroups.CreateOpts{Name: "web", Policy: "anti-affinity",
		Rules: &servergroups.Rules{MaxServerPerHost: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.only(t); got.Version != "compute 2.64" ||
		got.Body != `{"server_group":{"name":"web","policy":"anti-affinity","rules":{"max_server_per_host":2}}}` {
		t.Errorf("create sent %+v", got)
	}

	if err := compute.DeleteServerGroup(ctx, sc, groupID); err != nil {
		t.Fatal(err)
	}
	if got := rec.only(t); got.Method != http.MethodDelete {
		t.Errorf("delete sent %+v", got)
	}
}

func TestInstanceActions(t *testing.T) {
	fs, sc := nova(t, "2.66")
	var queries []string
	fs.Mux.HandleFunc("/servers/"+serverID+"/os-instance-actions", func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		answer(w, http.StatusOK, `{"instanceActions":[{"action":"reboot","request_id":"req-2"},{"action":"create","request_id":"req-1"}]}`)
	})
	fs.Mux.HandleFunc("/servers/"+serverID+"/os-instance-actions/req-1", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusOK, `{"instanceAction":{"action":"create","request_id":"req-1",
		  "events":[{"event":"compute__do_build_and_run_instance","result":"Success","traceback":null},{"event":"x","result":null}]}}`)
	})
	ctx := context.Background()

	all, err := compute.ListInstanceActions(ctx, sc, serverID, compute.InstanceActionListOpts{
		Marker: "req-9", ChangesSince: "2026-01-01T00:00:00Z", ChangesBefore: "2026-02-01T00:00:00Z"}, 1)
	if err != nil || len(all) != 1 || all[0].RequestID != "req-2" {
		t.Fatalf("ListInstanceActions = %+v, %v", all, err)
	}
	want := "changes-before=2026-02-01T00%3A00%3A00Z&changes-since=2026-01-01T00%3A00%3A00Z&limit=1&marker=req-9"
	if len(queries) != 1 || queries[0] != want {
		t.Errorf("queries = %q, want %q", queries, want)
	}

	m, err := compute.GetInstanceAction(ctx, sc, serverID, "req-1")
	if evs, ok := m["events"].([]any); err != nil || m["action"] != "create" || !ok || len(evs) != 2 {
		t.Fatalf("GetInstanceAction = %v, %v", m, err)
	}
	events, err := compute.ListInstanceActionEvents(ctx, sc, serverID, "req-1")
	if err != nil || len(events) != 2 || events[0].Result != "Success" || events[1].Result != "" {
		t.Fatalf("ListInstanceActionEvents = %+v, %v", events, err)
	}
}

func TestVolumeAttachments(t *testing.T) {
	fs, sc := nova(t, "2.89")
	var rec calls
	fs.Mux.HandleFunc("/servers/"+serverID+"/os-volume_attachments", rec.handler(func(_ *http.Request, _ string) (int, string) {
		return http.StatusOK, `{"volumeAttachments":[{"device":"/dev/vdb","serverId":"` + serverID + `","volumeId":"` + volID +
			`","delete_on_termination":true,"attachment_id":"att-1","bdm_uuid":"bdm-1"}]}`
	}))
	fs.Mux.HandleFunc("/servers/"+serverID+"/os-volume_attachments/"+volID, rec.handler(func(_ *http.Request, _ string) (int, string) {
		return http.StatusAccepted, ""
	}))
	ctx := context.Background()

	list, err := compute.ListVolumeAttachments(ctx, sc, serverID)
	if err != nil || len(list) != 1 || list[0].AttachmentID != "att-1" || list[0].BDMID != "bdm-1" ||
		list[0].DeleteOnTermination == nil || !*list[0].DeleteOnTermination {
		t.Fatalf("ListVolumeAttachments = %+v, %v", list, err)
	}
	rec.only(t)

	if err := compute.SetVolumeDeleteOnTermination(ctx, sc, serverID, volID, false); err != nil {
		t.Fatal(err)
	}
	if got := rec.only(t); got.Method != http.MethodPut ||
		got.Body != `{"volumeAttachment":{"delete_on_termination":false,"volumeId":"`+volID+`"}}` {
		t.Errorf("update sent %+v", got)
	}
}

func TestGetServerPassword_Encrypted(t *testing.T) {
	fs, sc := nova(t, "2.93")
	fs.Mux.HandleFunc("/servers/"+serverID+"/os-server-password", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusOK, `{"password":"ZXhhbXBsZQ=="}`)
	})
	pw, err := compute.GetServerPassword(context.Background(), sc, serverID, nil)
	if err != nil || pw != "ZXhhbXBsZQ==" {
		t.Fatalf("= %q, %v", pw, err)
	}
}

func TestServerRestCalls_ErrorsNameTheIDs(t *testing.T) {
	fs, sc := nova(t, "2.93")
	notFound := func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusNotFound, `{"itemNotFound":{"code":404,"message":"not found"}}`)
	}
	for _, p := range []string{"/os-server-groups", "/os-server-groups/" + groupID, "/servers/" + serverID + "/os-instance-actions",
		"/servers/" + serverID + "/os-instance-actions/req-1", "/servers/" + serverID + "/os-volume_attachments",
		"/servers/" + serverID + "/os-volume_attachments/" + volID, "/servers/" + serverID + "/os-server-password"} {
		fs.Mux.HandleFunc(p, notFound)
	}
	ctx := context.Background()
	ignore := func(_ any, err error) error { return err }
	for want, err := range map[string]error{
		"listing server groups":                                             ignore(compute.ListServerGroups(ctx, sc, servergroups.ListOpts{}, 0)),
		"showing server group " + groupID:                                   ignore(compute.GetServerGroup(ctx, sc, groupID)),
		`creating server group "web"`:                                       ignore(compute.CreateServerGroup(ctx, sc, servergroups.CreateOpts{Name: "web", Policies: []string{"affinity"}})),
		"deleting server group " + groupID:                                  compute.DeleteServerGroup(ctx, sc, groupID),
		"listing events of server " + serverID:                              ignore(compute.ListInstanceActions(ctx, sc, serverID, compute.InstanceActionListOpts{}, 0)),
		"showing event req-1 of server " + serverID:                         ignore(compute.GetInstanceAction(ctx, sc, serverID, "req-1")),
		"listing volume attachments of server " + serverID:                  ignore(compute.ListVolumeAttachments(ctx, sc, serverID)),
		"updating attachment of volume " + volID + " on server " + serverID: compute.SetVolumeDeleteOnTermination(ctx, sc, serverID, volID, true),
		"reading the admin password of server " + serverID:                  ignore(compute.GetServerPassword(ctx, sc, serverID, nil)),
	} {
		if err == nil || !strings.HasPrefix(err.Error(), want+": ") || !gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
			t.Errorf("err = %v, want %q wrapping the 404", err, want)
		}
	}
	if _, err := compute.ListInstanceActionEvents(ctx, sc, serverID, "req-1"); err == nil ||
		!strings.HasPrefix(err.Error(), "showing event req-1 of server "+serverID+": ") {
		t.Errorf("events err = %v", err)
	}
}
