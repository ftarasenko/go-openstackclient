package baremetal_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/allocations"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/ports"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetalintrospection/v1/introspection"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/koc/pkg/openstack/baremetal"
	"github.com/ftarasenko/koc/pkg/openstack/resolve"
)

const (
	node    = "1be26c0b-03f2-4d2e-ae87-c02d7f33c123"
	alloc   = "5344a3e2-978a-444e-990a-cbf47c62ef88"
	zedMax  = "1.80"
	newMax  = "1.120"
	nodeObj = `{"uuid":"` + node + `","name":"bm-1"}`
)

// reply is one canned answer; maxVersion, when set, is sent as ironic's
// maximum-version header.
type reply struct {
	status     int
	body       string
	maxVersion string
}

// fake answers "METHOD /path[?query]" from routes, in order, repeating the
// last reply, and records each request's body and ironic version header.
type fake struct {
	routes map[string][]reply
	seq    []string
	bodies map[string]string
	mv     map[string]string
}

func newFake(t *testing.T, typ, microversion string, routes map[string][]reply) (*fake, *gophercloud.ServiceClient) {
	t.Helper()
	fs := th.SetupHTTP()
	t.Cleanup(fs.Teardown)
	f := &fake{routes: routes, bodies: map[string]string{}, mv: map[string]string{}}
	fs.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		f.seq = append(f.seq, key)
		b, _ := io.ReadAll(r.Body)
		f.bodies[key] = string(b)
		f.mv[key] = r.Header.Get("X-OpenStack-Ironic-API-Version")
		replies, ok := f.routes[key]
		if !ok || len(replies) == 0 {
			t.Errorf("unexpected request %s", key)
			w.WriteHeader(http.StatusTeapot)
			return
		}
		rep := replies[0]
		if len(replies) > 1 {
			f.routes[key] = replies[1:]
		}
		if rep.maxVersion != "" {
			w.Header().Set(baremetal.MaxVersionHeader, rep.maxVersion)
		}
		if rep.body != "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
	})
	sc := fakeclient.ServiceClient(fs)
	sc.Type = typ
	sc.Microversion = microversion
	return f, sc
}

func ironic(t *testing.T, microversion string, routes map[string][]reply) (*fake, *gophercloud.ServiceClient) {
	t.Helper()
	return newFake(t, "baremetal", microversion, routes)
}

func one(status int, body string) []reply { return []reply{{status: status, body: body}} }

func old(status int) []reply {
	return []reply{{status: status, body: `{"error_message":"{}"}`, maxVersion: zedMax}}
}

// jsonEq compares a recorded request body with want as JSON values.
func jsonEq(t *testing.T, want, got string) {
	t.Helper()
	var w, g any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want: %v", err)
	}
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("body %q: %v", got, err)
	}
	th.AssertDeepEquals(t, w, g)
}

func wantMicroversion(t *testing.T, err error, f baremetal.Feature) *baremetal.MicroversionError {
	t.Helper()
	var me *baremetal.MicroversionError
	if !errors.As(err, &me) {
		t.Fatalf("err = %v, want a *MicroversionError", err)
	}
	if me.Feature != f {
		t.Errorf("Feature = %q, want %q", me.Feature, f)
	}
	return me
}

func TestListNodes_SendsFalseFiltersAndCaps(t *testing.T) {
	no := false
	f, c := ironic(t, "1.80", map[string][]reply{
		"GET /nodes/detail?provision_state=available&maintenance=false&associated=false": one(http.StatusOK,
			`{"nodes":[`+nodeObj+`,{"uuid":"n2"}]}`),
	})
	list, err := baremetal.ListNodes(context.Background(), c, baremetal.NodeListOpts{
		ListOpts:    nodes.ListOpts{ProvisionState: nodes.Available},
		Maintenance: &no, Associated: &no,
	}, true, 1)
	th.AssertNoErr(t, err)
	th.AssertEquals(t, 1, len(list))
	th.AssertEquals(t, "bm-1", list[0]["name"])
	for _, v := range f.mv {
		th.AssertEquals(t, "1.80", v)
	}
}

func TestGetNode_WrapsWithTheRef(t *testing.T) {
	_, c := ironic(t, "latest", map[string][]reply{"GET /nodes/bm-1": {{status: http.StatusNotFound, maxVersion: newMax}}})
	_, err := baremetal.GetNode(context.Background(), c, "bm-1")
	if err == nil || !strings.HasPrefix(err.Error(), "getting baremetal node bm-1: ") {
		t.Fatalf("err = %v", err)
	}
	var unexpected gophercloud.ErrUnexpectedResponseCode
	if !errors.As(err, &unexpected) {
		t.Errorf("the API error does not survive the wrap: %v", err)
	}
}

func TestSetProvisionState_SplicesRunbookAndRamdisk(t *testing.T) {
	path := "PUT /nodes/" + node + "/states/provision"
	f, c := ironic(t, "latest", map[string][]reply{path: one(http.StatusAccepted, "")})
	err := baremetal.SetProvisionState(context.Background(), c, node, baremetal.ProvisionStateOpts{
		ProvisionStateOpts: nodes.ProvisionStateOpts{Target: nodes.TargetService},
		Runbook:            "rb-1",
		DisableRamdisk:     true,
	})
	th.AssertNoErr(t, err)
	jsonEq(t, `{"target":"service","runbook":"rb-1","disable_ramdisk":true}`, f.bodies[path])
	th.AssertEquals(t, "latest", f.mv[path])
}

func TestSetProvisionState_ExplainsAPinBelowService(t *testing.T) {
	path := "PUT /nodes/" + node + "/states/provision"
	_, c := ironic(t, "1.80", map[string][]reply{path: one(http.StatusBadRequest, `{}`)})
	err := baremetal.SetProvisionState(context.Background(), c, node,
		nodes.ProvisionStateOpts{Target: nodes.TargetService})
	me := wantMicroversion(t, err, baremetal.FeatureNodeService)
	th.AssertEquals(t, "1.80", me.Pinned)
	if !strings.Contains(err.Error(), "but the client pins 1.80: changing provision state of node "+node) {
		t.Errorf("err = %v", err)
	}
}

func TestSetProvisionState_UnholdOnAnOldCloud(t *testing.T) {
	path := "PUT /nodes/" + node + "/states/provision"
	_, c := ironic(t, "latest", map[string][]reply{path: old(http.StatusBadRequest)})
	err := baremetal.SetProvisionState(context.Background(), c, node,
		nodes.ProvisionStateOpts{Target: nodes.TargetUnhold})
	th.AssertEquals(t, zedMax, wantMicroversion(t, err, baremetal.FeatureNodeUnhold).CloudMax)
}

func TestExplainMicroversion(t *testing.T) {
	apiErr := func(status int, maxVersion string) error {
		h := http.Header{}
		if maxVersion != "" {
			h.Set(baremetal.MaxVersionHeader, maxVersion)
		}
		return gophercloud.ErrUnexpectedResponseCode{Actual: status, ResponseHeader: h}
	}
	cases := []struct {
		name     string
		pin      string
		err      error
		doc      []reply // GET / (the version document), when the header is absent
		cloudMax string
		explain  bool
	}{
		{name: "old cloud by header", pin: "latest", err: apiErr(http.StatusNotFound, zedMax), cloudMax: zedMax, explain: true},
		{name: "new cloud: a real 404", pin: "latest", err: apiErr(http.StatusNotFound, newMax)},
		{name: "a 500 is not a version problem", pin: "latest", err: apiErr(http.StatusInternalServerError, zedMax)},
		{name: "not an API error", pin: "latest", err: errors.New("dial tcp: refused")},
		{name: "pin below the feature", pin: "1.80", err: apiErr(http.StatusMethodNotAllowed, newMax), explain: true},
		{
			name: "max from the version document", pin: "latest", err: apiErr(http.StatusNotAcceptable, ""),
			doc: one(http.StatusOK, `{"version":{"version":"1.80"}}`), cloudMax: zedMax, explain: true,
		},
		{
			name: "unreadable maximum", pin: "", err: apiErr(http.StatusBadRequest, ""),
			doc: one(http.StatusInternalServerError, `{}`), explain: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routes := map[string][]reply{}
			if tc.doc != nil {
				routes["GET /"] = tc.doc
			}
			_, c := ironic(t, tc.pin, routes)
			got := baremetal.ExplainMicroversion(context.Background(), c, baremetal.FeatureRunbooks, tc.err)
			if !tc.explain {
				var me *baremetal.MicroversionError
				if errors.As(got, &me) || got.Error() != tc.err.Error() {
					t.Fatalf("got %v, want the error unchanged", got)
				}
				return
			}
			me := wantMicroversion(t, got, baremetal.FeatureRunbooks)
			th.AssertEquals(t, tc.cloudMax, me.CloudMax)
			var unexpected gophercloud.ErrUnexpectedResponseCode
			if !errors.As(got, &unexpected) {
				t.Error("the API error is not in the chain")
			}
			again := baremetal.ExplainMicroversion(context.Background(), c, baremetal.FeatureShards, got)
			if wantMicroversion(t, again, baremetal.FeatureRunbooks) != me {
				t.Errorf("a second explanation replaced the first: %v", again)
			}
		})
	}
	if baremetal.ExplainMicroversion(context.Background(), nil, baremetal.FeatureShards, nil) != nil {
		t.Error("nil error must stay nil")
	}
}

func TestMicroversionError_Text(t *testing.T) {
	inner := errors.New("boom")
	f := baremetal.FeatureNodeFirmware
	th.AssertEquals(t, "1.86", f.Microversion())
	th.AssertEquals(t, "OpenStack 2023.2", f.Release())
	th.AssertEquals(t, "", baremetal.Feature("nope").Microversion())
	for _, tc := range []struct {
		e    baremetal.MicroversionError
		want string
	}{
		{baremetal.MicroversionError{Feature: f, Pinned: "1.80", Err: inner},
			"node firmware requires ironic API 1.86 (OpenStack 2023.2), but the client pins 1.80: boom"},
		{baremetal.MicroversionError{Feature: f, Err: inner},
			"node firmware requires ironic API 1.86 (OpenStack 2023.2), which this cloud may not support: boom"},
		{baremetal.MicroversionError{Feature: f, CloudMax: "1.80", Err: inner},
			"node firmware requires ironic API 1.86 (OpenStack 2023.2); this cloud supports up to 1.80: boom"},
	} {
		th.AssertEquals(t, tc.want, tc.e.Error())
	}
}

func TestGatedNodeCalls(t *testing.T) {
	ctx := context.Background()
	base := "/nodes/" + node
	_, c := ironic(t, "latest", map[string][]reply{
		"GET " + base + "/firmware":        old(http.StatusNotFound),
		"GET " + base + "/inventory":       old(http.StatusNotFound),
		"GET " + base + "/children":        old(http.StatusNotFound),
		"PATCH " + base:                    old(http.StatusBadRequest),
		"POST " + base + "/vmedia":         old(http.StatusNotFound),
		"POST /ports":                      old(http.StatusBadRequest),
		"POST /portgroups":                 old(http.StatusBadRequest),
		"GET /shards":                      old(http.StatusNotFound),
		"GET /runbooks":                    old(http.StatusNotFound),
		"POST /runbooks":                   old(http.StatusBadRequest),
		"GET /runbooks/rb-1/traits":        old(http.StatusNotFound),
		"GET /inspection_rules":            old(http.StatusNotFound),
		"POST /inspection_rules":           old(http.StatusMethodNotAllowed),
		"PATCH /ports/" + node:             old(http.StatusBadRequest),
		"PATCH /portgroups/pg-1":           old(http.StatusBadRequest),
		"PATCH /runbooks/rb-1":             old(http.StatusBadRequest),
		"PUT /runbooks/rb-1/traits/CUSTOM": old(http.StatusNotFound),
	})
	shard := nodes.UpdateOpts{nodes.UpdateOperation{Op: nodes.AddOp, Path: "/shard", Value: "s1"}}
	var named baremetal.Patch
	named.Replace("/name", "p1")
	var physnet baremetal.Patch
	physnet.Add("/physical_network", "net1")
	var desc baremetal.Patch
	desc.Remove("/description")

	_, errFirmware := baremetal.ListFirmware(ctx, c, node)
	_, errInventory := baremetal.GetNodeInventory(ctx, c, node)
	_, errChildren := baremetal.ListNodeChildren(ctx, c, node)
	_, errShard := baremetal.UpdateNode(ctx, c, node, shard)
	errVmedia := baremetal.AttachVirtualMedia(ctx, c, node, baremetal.VirtualMediaOpts{DeviceType: "cdrom", ImageURL: "http://example.com/x.iso"})
	_, errPort := baremetal.CreatePort(ctx, c, baremetal.PortCreateOpts{CreateOpts: ports.CreateOpts{NodeUUID: node, Address: "52:54:00:00:00:01"}, Name: "p1"})
	_, errPortName := baremetal.UpdatePort(ctx, c, node, ports.UpdateOpts{ports.UpdateOperation{Op: ports.ReplaceOp, Path: "/name", Value: "p1"}})
	_, errGroup := baremetal.CreatePortGroup(ctx, c, map[string]any{"node_uuid": node, "physical_network": "n", "category": "c"})
	_, errGroupSet := baremetal.UpdatePortGroup(ctx, c, "pg-1", physnet)
	_, errShards := baremetal.ListShards(ctx, c)
	_, errRunbooks := baremetal.ListRunbooks(ctx, c, nil, 0)
	_, errRunbookDesc := baremetal.CreateRunbook(ctx, c, map[string]any{"name": "CUSTOM_RB", "description": "d"})
	_, errRunbookSet := baremetal.UpdateRunbook(ctx, c, "rb-1", desc)
	_, errRunbookTraits := baremetal.ListRunbookTraits(ctx, c, "rb-1")
	errRunbookTrait := baremetal.AddRunbookTrait(ctx, c, "rb-1", "CUSTOM")
	_, errRules := baremetal.ListInspectionRules(ctx, c, nil, 0)
	_, errRuleCreate := baremetal.CreateInspectionRule(ctx, c, map[string]any{"actions": []any{}})

	for _, tc := range []struct {
		err    error
		f      baremetal.Feature
		prefix string
	}{
		{errFirmware, baremetal.FeatureNodeFirmware, ""},
		{errInventory, baremetal.FeatureNodeInventory, "getting inventory for node " + node},
		{errChildren, baremetal.FeatureNodeChildren, ""},
		{errShard, baremetal.FeatureShards, "updating baremetal node " + node},
		{errVmedia, baremetal.FeatureNodeVmedia, ""},
		{errPort, baremetal.FeaturePortName, "creating baremetal port"},
		{errPortName, baremetal.FeaturePortName, "updating baremetal port " + node},
		{errGroup, baremetal.FeaturePortGroupPhysnet, "creating baremetal port group"},
		{errGroupSet, baremetal.FeaturePortGroupPhysnet, "updating baremetal port group pg-1"},
		{errShards, baremetal.FeatureShards, ""},
		{errRunbooks, baremetal.FeatureRunbooks, "listing baremetal runbooks"},
		{errRunbookDesc, baremetal.FeatureRunbookDescription, "creating baremetal runbook"},
		{errRunbookSet, baremetal.FeatureRunbookDescription, "updating baremetal runbook rb-1"},
		{errRunbookTraits, baremetal.FeatureRunbookTraits, ""},
		{errRunbookTrait, baremetal.FeatureRunbookTraits, ""},
		{errRules, baremetal.FeatureInspectionRules, "listing inspection rules"},
		{errRuleCreate, baremetal.FeatureInspectionRules, "creating inspection rule"},
	} {
		me := wantMicroversion(t, tc.err, tc.f)
		th.AssertEquals(t, zedMax, me.CloudMax)
		if tc.prefix != "" && !strings.HasPrefix(tc.err.Error(), tc.prefix+": ") {
			t.Errorf("err = %v, want prefix %q", tc.err, tc.prefix)
		}
	}
}

func TestUngatedWritesAreNotExplained(t *testing.T) {
	ctx := context.Background()
	_, c := ironic(t, "latest", map[string][]reply{
		"PATCH /nodes/" + node: old(http.StatusBadRequest),
		"POST /ports":          old(http.StatusBadRequest),
		"POST /portgroups":     old(http.StatusBadRequest),
	})
	unshard := nodes.UpdateOpts{nodes.UpdateOperation{Op: nodes.RemoveOp, Path: "/shard"}}
	_, errNode := baremetal.UpdateNode(ctx, c, node, unshard)
	_, errPort := baremetal.CreatePort(ctx, c, ports.CreateOpts{NodeUUID: node, Address: "52:54:00:00:00:01"})
	_, errGroup := baremetal.CreatePortGroup(ctx, c, map[string]any{"node_uuid": node})
	for _, err := range []error{errNode, errPort, errGroup} {
		var me *baremetal.MicroversionError
		if err == nil || errors.As(err, &me) {
			t.Errorf("err = %v, want a plain API error", err)
		}
	}
}

func TestFindPortGroupByAddress_IsStrict(t *testing.T) {
	const mac = "52:54:00:00:00:01"
	path := "GET /portgroups/detail?" + url.Values{"address": {mac}}.Encode()
	_, c := ironic(t, "latest", map[string][]reply{path: {
		{status: http.StatusOK, body: `{"portgroups":[]}`},
		{status: http.StatusOK, body: `{"portgroups":[{"uuid":"a"},{"uuid":"b"}]}`},
		{status: http.StatusOK, body: `{"portgroups":[{"uuid":"a"}]}`},
		{status: http.StatusServiceUnavailable, body: `{}`},
	}})
	ctx := context.Background()

	_, err := baremetal.FindPortGroupByAddress(ctx, c, mac)
	var nf *resolve.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("zero matches: %v", err)
	}
	_, err = baremetal.FindPortGroupByAddress(ctx, c, mac)
	var amb *resolve.AmbiguousError
	if !errors.As(err, &amb) || amb.Count != 2 {
		t.Fatalf("two matches: %v", err)
	}
	pg, err := baremetal.FindPortGroupByAddress(ctx, c, mac)
	th.AssertNoErr(t, err)
	th.AssertEquals(t, "a", pg["uuid"])
	_, err = baremetal.FindPortGroupByAddress(ctx, c, mac)
	if err == nil || !strings.HasPrefix(err.Error(), "getting baremetal port group with address "+mac+": ") {
		t.Fatalf("list failure: %v", err)
	}
}

func TestPortGroupMembers_GroupsByPortGroup(t *testing.T) {
	path := "GET /ports?" + url.Values{"fields": {"uuid,portgroup_uuid"}, "node": {node}}.Encode()
	_, c := ironic(t, "latest", map[string][]reply{path: one(http.StatusOK,
		`{"ports":[{"uuid":"p1","portgroup_uuid":"g1"},{"uuid":"p2","portgroup_uuid":"g1"},{"uuid":"p3"}]}`)})
	m, err := baremetal.PortGroupMembers(context.Background(), c, node)
	th.AssertNoErr(t, err)
	th.AssertDeepEquals(t, map[string][]string{"g1": {"p1", "p2"}}, m)
}

func TestNodeUUID_ReadsTheNode(t *testing.T) {
	_, c := ironic(t, "latest", map[string][]reply{"GET /nodes/bm-1?fields=uuid": one(http.StatusOK, nodeObj)})
	id, err := baremetal.NodeUUID(context.Background(), c, "bm-1")
	th.AssertNoErr(t, err)
	th.AssertEquals(t, node, id)
}

func TestDeployTemplate_PatchBody(t *testing.T) {
	f, c := ironic(t, "latest", map[string][]reply{"PATCH /deploy_templates/CUSTOM_DT": one(http.StatusOK, `{"name":"CUSTOM_DT"}`)})
	var p baremetal.Patch
	p.Replace("/name", "CUSTOM_DT")
	p.Add("/extra/a~1b", "1")
	p.Remove("/extra/old")
	dt, err := baremetal.UpdateDeployTemplate(context.Background(), c, "CUSTOM_DT", p)
	th.AssertNoErr(t, err)
	th.AssertEquals(t, "CUSTOM_DT", dt["name"])
	jsonEq(t, `[{"op":"replace","path":"/name","value":"CUSTOM_DT"},`+
		`{"op":"add","path":"/extra/a~1b","value":"1"},{"op":"remove","path":"/extra/old"}]`,
		f.bodies["PATCH /deploy_templates/CUSTOM_DT"])
}

func TestAllocations(t *testing.T) {
	ctx := context.Background()
	obj := func(state, lastError string) string {
		return `{"uuid":"` + alloc + `","state":"` + state + `","owner":"proj-1","last_error":"` + lastError + `"}`
	}
	f, c := ironic(t, "latest", map[string][]reply{
		"GET /allocations?state=active&owner=proj-1": one(http.StatusOK, `{"allocations":[`+obj("active", "")+`]}`),
		"POST /allocations":                          one(http.StatusCreated, obj("allocating", "")),
		"PATCH /allocations/" + alloc:                one(http.StatusOK, obj("active", "")),
		"GET /allocations/" + alloc: {
			{status: http.StatusOK, body: obj("allocating", "")},
			{status: http.StatusOK, body: obj("active", "")},
		},
		"GET /allocations/broken":      one(http.StatusOK, obj("error", "no nodes")),
		"DELETE /allocations/" + alloc: one(http.StatusNoContent, ""),
	})

	list, err := baremetal.ListAllocations(ctx, c, baremetal.AllocationListOpts{
		ListOpts: allocations.ListOpts{State: allocations.AllocationState(allocations.Active)}, Owner: "proj-1",
	}, 0)
	th.AssertNoErr(t, err)
	th.AssertEquals(t, "proj-1", list[0].Owner)

	al, err := baremetal.CreateAllocation(ctx, c, baremetal.AllocationCreateOpts{
		CreateOpts: allocations.CreateOpts{ResourceClass: "baremetal"}, Owner: "proj-1",
	})
	th.AssertNoErr(t, err)
	jsonEq(t, `{"resource_class":"baremetal","owner":"proj-1"}`, f.bodies["POST /allocations"])

	settled, err := baremetal.WaitForAllocation(ctx, c, al.UUID, time.Millisecond)
	th.AssertNoErr(t, err)
	th.AssertEquals(t, "active", settled.State)

	_, err = baremetal.WaitForAllocation(ctx, c, "broken", time.Millisecond)
	if err == nil || err.Error() != "allocation broken failed: no nodes" {
		t.Errorf("err = %v", err)
	}

	var p baremetal.Patch
	p.Replace("/name", "a1")
	_, err = baremetal.UpdateAllocation(ctx, c, alloc, p)
	th.AssertNoErr(t, err)
	th.AssertNoErr(t, baremetal.DeleteAllocation(ctx, c, alloc))
}

func TestWaitForAllocation_GivesUpAtTheDeadline(t *testing.T) {
	_, c := ironic(t, "latest", map[string][]reply{
		"GET /allocations/" + alloc: one(http.StatusOK, `{"uuid":"`+alloc+`","state":"allocating"}`),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := baremetal.WaitForAllocation(ctx, c, alloc, 5*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(err.Error(), "waiting for allocation "+alloc) {
		t.Errorf("err = %v", err)
	}
}

func TestWaitForProvisionState(t *testing.T) {
	ctx := context.Background()
	state := func(p, target, lastError string) reply {
		return reply{status: http.StatusOK, body: `{"uuid":"` + node + `","provision_state":"` + p +
			`","target_provision_state":"` + target + `","last_error":"` + lastError + `"}`}
	}
	_, c := ironic(t, "latest", map[string][]reply{
		"GET /nodes/" + node: {state("deploying", "active", ""), state("active", "", "")},
		"GET /nodes/failing": {state("deploy failed", "", "no disk")},
		"GET /nodes/abort":   {state("inspecting", "manageable", ""), state("inspect failed", "manageable", "aborted")},
	})
	th.AssertNoErr(t, baremetal.WaitForProvisionState(ctx, c, node, nodes.Active, time.Millisecond))

	err := baremetal.WaitForProvisionState(ctx, c, "failing", nodes.Active, time.Millisecond)
	th.AssertEquals(t, `node failing entered failure state "deploy failed": no disk`, err.Error())

	st, lastError, err := baremetal.WaitForProvisionSettled(ctx, c, "abort", time.Millisecond)
	th.AssertNoErr(t, err)
	th.AssertEquals(t, "inspect failed", st)
	th.AssertEquals(t, "aborted", lastError)

	th.AssertEquals(t, true, baremetal.IsProvisionFailure("clean failed"))
	th.AssertEquals(t, true, baremetal.IsProvisionFailure("error"))
	th.AssertEquals(t, false, baremetal.IsProvisionFailure("active"))
}

func TestWaitForProvisionState_StopsAfterRepeatedReadFailures(t *testing.T) {
	_, c := ironic(t, "latest", map[string][]reply{"GET /nodes/" + node: one(http.StatusServiceUnavailable, `{}`)})
	err := baremetal.WaitForProvisionState(context.Background(), c, node, nodes.Active, time.Millisecond)
	if err == nil || !strings.HasPrefix(err.Error(), "polling node "+node+": ") {
		t.Errorf("err = %v", err)
	}
	_, _, err = baremetal.WaitForProvisionSettled(context.Background(), c, node, time.Millisecond)
	if err == nil || !strings.HasPrefix(err.Error(), "polling node "+node+": ") {
		t.Errorf("settled: err = %v", err)
	}
}

func TestIntrospection(t *testing.T) {
	ctx := context.Background()
	f, c := newFake(t, "baremetal-introspection", "", map[string][]reply{
		"POST /introspection/" + node + "?manage_boot=false": one(http.StatusAccepted, ""),
		"GET /introspection/" + node:                         one(http.StatusOK, `{"uuid":"`+node+`","finished":true,"state":"finished"}`),
		"GET /introspection/" + node + "/data":               one(http.StatusOK, `{"cpu_arch":"x86_64","plugin_key":{"a":1}}`),
		"POST /introspection/" + node + "/abort":             one(http.StatusAccepted, ""),
		"GET /introspection":                                 one(http.StatusOK, `{"introspection":[{"uuid":"`+node+`"}]}`),
	})
	th.AssertNoErr(t, baremetal.StartIntrospection(ctx, c, node, introspection.StartOpts{ManageBoot: gophercloud.Disabled}))
	th.AssertEquals(t, "POST /introspection/"+node+"?manage_boot=false", f.seq[0])

	st, err := baremetal.GetIntrospectionStatus(ctx, c, node)
	th.AssertNoErr(t, err)
	th.AssertEquals(t, "finished", st.State)

	data, err := baremetal.GetIntrospectionData(ctx, c, node)
	th.AssertNoErr(t, err)
	th.AssertEquals(t, "x86_64", data.CPUArch)

	raw, err := baremetal.GetIntrospectionDataRaw(ctx, c, node)
	th.AssertNoErr(t, err)
	b, _ := json.Marshal(raw)
	if !strings.Contains(string(b), `"plugin_key"`) {
		t.Errorf("raw data lost the plugin key: %s", b)
	}

	list, err := baremetal.ListIntrospections(ctx, c, introspection.ListIntrospectionsOpts{}, 0)
	th.AssertNoErr(t, err)
	th.AssertEquals(t, 1, len(list))
	th.AssertNoErr(t, baremetal.AbortIntrospection(ctx, c, node))
}

func TestIntrospection_ErrorsNameTheNode(t *testing.T) {
	ctx := context.Background()
	_, c := newFake(t, "baremetal-introspection", "", map[string][]reply{
		"POST /introspection/" + node:          one(http.StatusConflict, `{}`),
		"GET /introspection/" + node + "/data": one(http.StatusNotFound, `{}`),
	})
	err := baremetal.StartIntrospection(ctx, c, node, introspection.StartOpts{})
	if err == nil || !strings.HasPrefix(err.Error(), "starting introspection of node "+node+": ") {
		t.Errorf("start: %v", err)
	}
	_, err = baremetal.GetIntrospectionDataRaw(ctx, c, node)
	if err == nil || !strings.HasPrefix(err.Error(), "getting introspection data for node "+node+": ") {
		t.Errorf("data: %v", err)
	}
}
