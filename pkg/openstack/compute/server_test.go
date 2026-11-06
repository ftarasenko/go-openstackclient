package compute_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/attachinterfaces"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/remoteconsoles"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"

	"github.com/ftarasenko/koc/pkg/openstack/compute"
)

const (
	serverID = "1b2c3d4e-5f6a-4b7c-8d9e-0f1a2b3c4d5e"
	groupID  = "2c3d4e5f-6a7b-4c8d-9e0f-1a2b3c4d5e6f"
	volID    = "3d4e5f6a-7b8c-4d9e-0f1a-2b3c4d5e6f7a"
	portID   = "4e5f6a7b-8c9d-4e0f-1a2b-3c4d5e6f7a8b"
	netID    = "5f6a7b8c-9d0e-4f1a-2b3c-4d5e6f7a8b9c"
)

// call is one request a fake served: method, path, microversion header and
// compact body.
type call struct{ Method, Path, Version, Body string }

// calls records the requests a fake served and answers each with the status
// and body reply picks.
type calls struct {
	mu   sync.Mutex
	seen []call
}

func (c *calls) handler(reply func(r *http.Request, body string) (int, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		var v any
		if json.Unmarshal(raw, &v) == nil {
			b, _ := json.Marshal(v)
			body = string(b)
		}
		c.mu.Lock()
		c.seen = append(c.seen, call{r.Method, r.URL.Path, r.Header.Get("OpenStack-API-Version"), body})
		c.mu.Unlock()
		status, out := reply(r, body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-OpenStack-Nova-API-Version", "2.93")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, out)
	}
}

func (c *calls) only(t *testing.T) call {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.seen) != 1 {
		t.Fatalf("want one request, got %+v", c.seen)
	}
	got := c.seen[0]
	c.seen = nil
	return got
}

func TestServerActions_Bodies(t *testing.T) {
	fs, sc := nova(t, "2.93")
	var rec calls
	fs.Mux.HandleFunc("/servers/"+serverID+"/action", rec.handler(func(_ *http.Request, _ string) (int, string) {
		return http.StatusAccepted, ""
	}))
	ctx := context.Background()
	cases := []struct {
		name string
		call func() error
		body string
	}{
		{"start", func() error { return compute.StartServer(ctx, sc, serverID) }, `{"os-start":null}`},
		{"stop", func() error { return compute.StopServer(ctx, sc, serverID) }, `{"os-stop":null}`},
		{"pause", func() error { return compute.PauseServer(ctx, sc, serverID) }, `{"pause":null}`},
		{"unpause", func() error { return compute.UnpauseServer(ctx, sc, serverID) }, `{"unpause":null}`},
		{"suspend", func() error { return compute.SuspendServer(ctx, sc, serverID) }, `{"suspend":null}`},
		{"resume", func() error { return compute.ResumeServer(ctx, sc, serverID) }, `{"resume":null}`},
		{"lock", func() error { return compute.LockServer(ctx, sc, serverID) }, `{"lock":null}`},
		{"unlock", func() error { return compute.UnlockServer(ctx, sc, serverID) }, `{"unlock":null}`},
		{"unrescue", func() error { return compute.UnrescueServer(ctx, sc, serverID) }, `{"unrescue":null}`},
		{"reboot", func() error { return compute.RebootServer(ctx, sc, serverID, servers.HardReboot) }, `{"reboot":{"type":"HARD"}}`},
		{"resize", func() error { return compute.ResizeServer(ctx, sc, serverID, "42") }, `{"resize":{"flavorRef":"42"}}`},
		{"confirm", func() error { return compute.ConfirmResize(ctx, sc, serverID) }, `{"confirmResize":null}`},
		{"revert", func() error { return compute.RevertResize(ctx, sc, serverID) }, `{"revertResize":null}`},
		{"shelve", func() error { return compute.ShelveServer(ctx, sc, serverID) }, `{"shelve":null}`},
		{"offload", func() error { return compute.ShelveOffloadServer(ctx, sc, serverID) }, `{"shelveOffload":null}`},
		{"unshelve", func() error { return compute.UnshelveServer(ctx, sc, serverID, compute.UnshelveOpts{}) }, `{"unshelve":null}`},
		{"unshelve host", func() error {
			return compute.UnshelveServer(ctx, sc, serverID, compute.UnshelveOpts{Host: "cmp-01"})
		}, `{"unshelve":{"host":"cmp-01"}}`},
		{"unshelve az host", func() error {
			return compute.UnshelveServer(ctx, sc, serverID,
				compute.UnshelveOpts{UnshelveOpts: servers.UnshelveOpts{AvailabilityZone: "az1"}, Host: "cmp-01"})
		}, `{"unshelve":{"availability_zone":"az1","host":"cmp-01"}}`},
		{"add sg", func() error { return compute.AddSecurityGroup(ctx, sc, serverID, "web") }, `{"addSecurityGroup":{"name":"web"}}`},
		{"remove sg", func() error { return compute.RemoveSecurityGroup(ctx, sc, serverID, "web") }, `{"removeSecurityGroup":{"name":"web"}}`},
		{"add group", func() error { return compute.AddServerGroup(ctx, sc, serverID, groupID) },
			`{"addServerGroup":{"server_group_id":"` + groupID + `"}}`},
		{"remove group", func() error { return compute.RemoveServerGroup(ctx, sc, serverID) }, `{"removeServerGroup":null}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err != nil {
				t.Fatal(err)
			}
			got := rec.only(t)
			if got.Method != http.MethodPost || got.Body != tc.body || got.Version != "compute 2.93" {
				t.Errorf("request = %+v, want POST %s at compute 2.93", got, tc.body)
			}
		})
	}
}

func TestFloatingIPActions_PinnedBelow244(t *testing.T) {
	fs, sc := nova(t, "latest")
	var rec calls
	fs.Mux.HandleFunc("/servers/"+serverID+"/action", rec.handler(func(_ *http.Request, _ string) (int, string) {
		return http.StatusAccepted, ""
	}))
	ctx := context.Background()
	if err := compute.AddFloatingIP(ctx, sc, serverID, "203.0.113.5", "10.0.0.5"); err != nil {
		t.Fatal(err)
	}
	if got := rec.only(t); got.Version != "compute 2.43" ||
		got.Body != `{"addFloatingIp":{"address":"203.0.113.5","fixed_address":"10.0.0.5"}}` {
		t.Errorf("add = %+v", got)
	}
	if err := compute.AddFloatingIP(ctx, sc, serverID, "203.0.113.5", ""); err != nil {
		t.Fatal(err)
	}
	if got := rec.only(t); got.Body != `{"addFloatingIp":{"address":"203.0.113.5"}}` {
		t.Errorf("add without fixed = %+v", got)
	}
	if err := compute.RemoveFloatingIP(ctx, sc, serverID, "203.0.113.5"); err != nil {
		t.Fatal(err)
	}
	if got := rec.only(t); got.Version != "compute 2.43" || got.Body != `{"removeFloatingIp":{"address":"203.0.113.5"}}` {
		t.Errorf("remove = %+v", got)
	}
	if sc.Microversion != "latest" {
		t.Errorf("the pin must not leak into the caller's client: %q", sc.Microversion)
	}
}

func TestSupportsMicroversion(t *testing.T) {
	for _, tc := range []struct {
		have, want string
		ok         bool
	}{
		{"latest", "2.96", true},
		{"", "2.2", false},
		{"2.57", "2.57", true},
		{"2.56", "2.57", false},
		{"2.100", "2.57", true},
		{"3.0", "2.99", true},
		{"1.99", "2.1", false},
		{"bogus", "2.1", false},
	} {
		if got := compute.SupportsMicroversion(&gophercloud.ServiceClient{Microversion: tc.have}, tc.want); got != tc.ok {
			t.Errorf("SupportsMicroversion(%q, %q) = %v", tc.have, tc.want, got)
		}
	}
}

func TestRebuildServer_UserData(t *testing.T) {
	fs, sc := nova(t, "2.93")
	var rec calls
	fs.Mux.HandleFunc("/servers/"+serverID+"/action", rec.handler(func(_ *http.Request, _ string) (int, string) {
		return http.StatusAccepted, `{"server":{"id":"` + serverID + `","name":"web","status":"REBUILD"}}`
	}))
	ctx := context.Background()
	base := servers.RebuildOpts{ImageRef: "img"}
	data := "aGk="
	for _, tc := range []struct {
		opts servers.RebuildOptsBuilder
		body string
	}{
		{base, `{"rebuild":{"imageRef":"img"}}`},
		{compute.RebuildOpts{RebuildOptsBuilder: base, UserData: &data}, `{"rebuild":{"imageRef":"img","user_data":"aGk="}}`},
		{compute.RebuildOpts{RebuildOptsBuilder: base, ClearUserData: true}, `{"rebuild":{"imageRef":"img","user_data":null}}`},
	} {
		s, err := compute.RebuildServer(ctx, sc, serverID, tc.opts)
		if err != nil || s.Status != "REBUILD" {
			t.Fatalf("RebuildServer = %+v, %v", s, err)
		}
		if got := rec.only(t); got.Body != tc.body {
			t.Errorf("body = %s, want %s", got.Body, tc.body)
		}
	}
}

func TestRebuildOpts_RejectsAnUnexpectedBody(t *testing.T) {
	_, err := compute.RebuildOpts{RebuildOptsBuilder: oddRebuild{}, ClearUserData: true}.ToServerRebuildMap()
	if err == nil || !strings.Contains(err.Error(), "unexpected rebuild request body") {
		t.Fatalf("err = %v", err)
	}
}

type oddRebuild struct{}

func (oddRebuild) ToServerRebuildMap() (map[string]any, error) {
	return map[string]any{"rebuild": "not an object"}, nil
}

func TestAttachVolume(t *testing.T) {
	fs, sc := nova(t, "2.93")
	var rec calls
	status := http.StatusAccepted
	fs.Mux.HandleFunc("/servers/"+serverID+"/os-volume_attachments", rec.handler(func(_ *http.Request, _ string) (int, string) {
		return status, ""
	}))
	fs.Mux.HandleFunc("/servers/"+serverID+"/os-volume_attachments/"+volID, rec.handler(func(_ *http.Request, _ string) (int, string) {
		return http.StatusAccepted, ""
	}))
	ctx := context.Background()
	keep := false
	if err := compute.AttachVolume(ctx, sc, serverID, compute.VolumeAttachOpts{VolumeID: volID, Device: "/dev/vdb", Tag: "data", DeleteOnTermination: &keep}); err != nil {
		t.Fatal(err)
	}
	if got := rec.only(t); got.Body != `{"volumeAttachment":{"delete_on_termination":false,"device":"/dev/vdb","tag":"data","volumeId":"`+volID+`"}}` {
		t.Errorf("body = %s", got.Body)
	}
	status = http.StatusOK
	if err := compute.AttachVolume(ctx, sc, serverID, compute.VolumeAttachOpts{VolumeID: volID}); err != nil {
		t.Fatalf("a 200 is an attach too: %v", err)
	}
	if got := rec.only(t); got.Body != `{"volumeAttachment":{"volumeId":"`+volID+`"}}` {
		t.Errorf("body = %s", got.Body)
	}
	if err := compute.DetachVolume(ctx, sc, serverID, volID); err != nil {
		t.Fatal(err)
	}
	if got := rec.only(t); got.Method != http.MethodDelete {
		t.Errorf("detach = %+v", got)
	}
}

func TestAttachVolume_BadOpts(t *testing.T) {
	_, sc := nova(t, "2.93")
	err := compute.AttachVolume(context.Background(), sc, serverID, badAttach{})
	if err == nil || !strings.HasPrefix(err.Error(), "attaching volume to server "+serverID+": ") {
		t.Fatalf("err = %v", err)
	}
}

type badAttach struct{}

func (badAttach) ToVolumeAttachmentCreateMap() (map[string]any, error) { return nil, errors.New("bad") }

func TestConsole(t *testing.T) {
	fs, sc := nova(t, "2.93")
	var rec calls
	fs.Mux.HandleFunc("/servers/"+serverID+"/action", rec.handler(func(_ *http.Request, _ string) (int, string) {
		return http.StatusOK, `{"output":"login:"}`
	}))
	fs.Mux.HandleFunc("/servers/"+serverID+"/remote-consoles", rec.handler(func(_ *http.Request, body string) (int, string) {
		return http.StatusOK, strings.Replace(body, `}}`, `,"url":"https://console.example.com/x"}}`, 1)
	}))
	ctx := context.Background()
	out, err := compute.ConsoleOutput(ctx, sc, serverID, 20)
	if err != nil || out != "login:" {
		t.Fatalf("ConsoleOutput = %q, %v", out, err)
	}
	if got := rec.only(t); got.Body != `{"os-getConsoleOutput":{"length":20}}` {
		t.Errorf("body = %s", got.Body)
	}
	for typ, proto := range map[remoteconsoles.ConsoleType]string{
		remoteconsoles.ConsoleTypeNoVNC:      "vnc",
		remoteconsoles.ConsoleTypeXVPVNC:     "vnc",
		remoteconsoles.ConsoleTypeSPICEHTML5: "spice",
		remoteconsoles.ConsoleTypeSerial:     "serial",
		remoteconsoles.ConsoleTypeWebMKS:     "mks",
	} {
		rc, err := compute.CreateRemoteConsole(ctx, sc, serverID, typ)
		if err != nil || rc.Protocol != proto || rc.URL == "" {
			t.Fatalf("%s: %+v, %v", typ, rc, err)
		}
		rec.only(t)
	}
}

func TestRescueAndImage(t *testing.T) {
	fs, sc := nova(t, "2.93")
	var rec calls
	fs.Mux.HandleFunc("/servers/"+serverID+"/action", rec.handler(func(_ *http.Request, body string) (int, string) {
		if strings.Contains(body, "rescue") {
			return http.StatusOK, `{"adminPass":"generated"}`
		}
		return http.StatusAccepted, `{"image_id":"6a7b8c9d-0e1f-4a2b-3c4d-5e6f7a8b9c0d"}`
	}))
	ctx := context.Background()
	pass, err := compute.RescueServer(ctx, sc, serverID, servers.RescueOpts{RescueImageRef: "img"})
	if err != nil || pass != "generated" {
		t.Fatalf("RescueServer = %q, %v", pass, err)
	}
	if got := rec.only(t); got.Body != `{"rescue":{"rescue_image_ref":"img"}}` {
		t.Errorf("body = %s", got.Body)
	}
	id, err := compute.CreateServerImage(ctx, sc, serverID, servers.CreateImageOpts{Name: "snap"})
	if err != nil || id != "6a7b8c9d-0e1f-4a2b-3c4d-5e6f7a8b9c0d" {
		t.Fatalf("CreateServerImage = %q, %v", id, err)
	}
	if got := rec.only(t); got.Body != `{"createImage":{"name":"snap"}}` {
		t.Errorf("body = %s", got.Body)
	}
}

func TestInterfaces(t *testing.T) {
	fs, sc := nova(t, "2.93")
	var rec calls
	iface := `{"port_id":"` + portID + `","net_id":"` + netID + `","mac_addr":"fa:16:3e:00:00:01","port_state":"ACTIVE","fixed_ips":[{"ip_address":"10.0.0.5"}]}`
	fs.Mux.HandleFunc("/servers/"+serverID+"/os-interface", rec.handler(func(r *http.Request, _ string) (int, string) {
		if r.Method == http.MethodGet {
			return http.StatusOK, `{"interfaceAttachments":[` + iface + `]}`
		}
		return http.StatusOK, `{"interfaceAttachment":` + iface + `}`
	}))
	fs.Mux.HandleFunc("/servers/"+serverID+"/os-interface/"+portID, rec.handler(func(_ *http.Request, _ string) (int, string) {
		return http.StatusAccepted, ""
	}))
	ctx := context.Background()
	all, err := compute.ListInterfaces(ctx, sc, serverID)
	if err != nil || len(all) != 1 || all[0].FixedIPs[0].IPAddress != "10.0.0.5" {
		t.Fatalf("ListInterfaces = %+v, %v", all, err)
	}
	rec.only(t)
	got, err := compute.AttachInterface(ctx, sc, serverID,
		compute.InterfaceAttachOpts{CreateOpts: attachinterfaces.CreateOpts{NetworkID: netID}, Tag: "data"})
	if err != nil || got.PortID != portID {
		t.Fatalf("AttachInterface = %+v, %v", got, err)
	}
	if c := rec.only(t); c.Body != `{"interfaceAttachment":{"net_id":"`+netID+`","tag":"data"}}` {
		t.Errorf("body = %s", c.Body)
	}
	if _, err := compute.AttachInterface(ctx, sc, serverID, compute.InterfaceAttachOpts{CreateOpts: attachinterfaces.CreateOpts{PortID: portID}}); err != nil {
		t.Fatal(err)
	}
	if c := rec.only(t); c.Body != `{"interfaceAttachment":{"port_id":"`+portID+`"}}` {
		t.Errorf("an untagged attach keeps gophercloud's body: %s", c.Body)
	}
	if err := compute.DetachInterface(ctx, sc, serverID, portID); err != nil {
		t.Fatal(err)
	}
	if c := rec.only(t); c.Method != http.MethodDelete {
		t.Errorf("detach = %+v", c)
	}
}

func TestServerCallErrorsNameTheServer(t *testing.T) {
	fs, sc := nova(t, "2.93")
	fs.Mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusInternalServerError, `{"computeFault":{"code":500,"message":"boom"}}`)
	})
	ctx := context.Background()
	cases := map[string]error{
		"starting server " + serverID:                                      compute.StartServer(ctx, sc, serverID),
		"stopping server " + serverID:                                      compute.StopServer(ctx, sc, serverID),
		"pausing server " + serverID:                                       compute.PauseServer(ctx, sc, serverID),
		"unpausing server " + serverID:                                     compute.UnpauseServer(ctx, sc, serverID),
		"suspending server " + serverID:                                    compute.SuspendServer(ctx, sc, serverID),
		"resuming server " + serverID:                                      compute.ResumeServer(ctx, sc, serverID),
		"locking server " + serverID:                                       compute.LockServer(ctx, sc, serverID),
		"unlocking server " + serverID:                                     compute.UnlockServer(ctx, sc, serverID),
		"unrescuing server " + serverID:                                    compute.UnrescueServer(ctx, sc, serverID),
		"rebooting server " + serverID:                                     compute.RebootServer(ctx, sc, serverID, servers.SoftReboot),
		"resizing server " + serverID:                                      compute.ResizeServer(ctx, sc, serverID, "1"),
		"confirming resize of server " + serverID:                          compute.ConfirmResize(ctx, sc, serverID),
		"reverting resize of server " + serverID:                           compute.RevertResize(ctx, sc, serverID),
		"shelving server " + serverID:                                      compute.ShelveServer(ctx, sc, serverID),
		"unshelving server " + serverID:                                    compute.UnshelveServer(ctx, sc, serverID, compute.UnshelveOpts{}),
		"attaching volume to server " + serverID:                           compute.AttachVolume(ctx, sc, serverID, compute.VolumeAttachOpts{VolumeID: volID}),
		"detaching volume " + volID + " from server " + serverID:           compute.DetachVolume(ctx, sc, serverID, volID),
		`associating floating IP "203.0.113.5" with server ` + serverID:    compute.AddFloatingIP(ctx, sc, serverID, "203.0.113.5", ""),
		`disassociating floating IP "203.0.113.5" from server ` + serverID: compute.RemoveFloatingIP(ctx, sc, serverID, "203.0.113.5"),
		`adding security group "web" to server ` + serverID:                compute.AddSecurityGroup(ctx, sc, serverID, "web"),
		`removing security group "web" from server ` + serverID:            compute.RemoveSecurityGroup(ctx, sc, serverID, "web"),
		"adding server " + serverID + " to server group " + groupID:        compute.AddServerGroup(ctx, sc, serverID, groupID),
		"removing server " + serverID + " from its server group":           compute.RemoveServerGroup(ctx, sc, serverID),
		"detaching port " + portID + " from server " + serverID:            compute.DetachInterface(ctx, sc, serverID, portID),
		"shelving server " + serverID + " (offload)":                       compute.ShelveOffloadServer(ctx, sc, serverID),
	}
	_, err := compute.RebuildServer(ctx, sc, serverID, servers.RebuildOpts{ImageRef: "img"})
	cases["rebuilding server "+serverID] = err
	_, err = compute.ConsoleOutput(ctx, sc, serverID, 0)
	cases["fetching console log for server "+serverID] = err
	_, err = compute.CreateRemoteConsole(ctx, sc, serverID, remoteconsoles.ConsoleTypeNoVNC)
	cases["creating console for server "+serverID] = err
	_, err = compute.RescueServer(ctx, sc, serverID, servers.RescueOpts{})
	cases["rescuing server "+serverID] = err
	_, err = compute.CreateServerImage(ctx, sc, serverID, servers.CreateImageOpts{Name: "snap"})
	cases["creating an image from server "+serverID] = err
	_, err = compute.ListInterfaces(ctx, sc, serverID)
	cases["listing interfaces of server "+serverID] = err
	_, err = compute.AttachInterface(ctx, sc, serverID, attachinterfaces.CreateOpts{PortID: portID})
	cases["attaching an interface to server "+serverID] = err

	for prefix, err := range cases {
		prefix = strings.TrimSuffix(prefix, " (offload)")
		var unexpected gophercloud.ErrUnexpectedResponseCode
		if err == nil || !strings.HasPrefix(err.Error(), prefix+": ") || !errors.As(err, &unexpected) {
			t.Errorf("%s: err = %v", prefix, err)
		}
	}
}

// serverStates answers GET /servers/{id} with each body in turn, repeating the
// last.
func serverStates(t *testing.T, bodies ...string) (*gophercloud.ServiceClient, *int) {
	t.Helper()
	fs, sc := nova(t, "2.93")
	var mu sync.Mutex
	reads := 0
	fs.Mux.HandleFunc("/servers/"+serverID, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		i := min(reads, len(bodies)-1)
		reads++
		mu.Unlock()
		if bodies[i] == "404" {
			answer(w, http.StatusNotFound, `{"itemNotFound":{"code":404,"message":"gone"}}`)
			return
		}
		if bodies[i] == "500" {
			answer(w, http.StatusInternalServerError, `{"computeFault":{"code":500,"message":"boom"}}`)
			return
		}
		answer(w, http.StatusOK, `{"server":{"id":"`+serverID+`",`+bodies[i]+`}}`)
	})
	return sc, &reads
}

func state(status, task string) string {
	t := "null"
	if task != "" {
		t = `"` + task + `"`
	}
	return `"status":"` + status + `","OS-EXT-STS:task_state":` + t
}

func TestWaitForServerStatus(t *testing.T) {
	ctx := context.Background()
	t.Run("waits out the task", func(t *testing.T) {
		sc, reads := serverStates(t, state("SHELVED", "shelving_offloading"), state("SHELVED_OFFLOADED", ""))
		if err := compute.WaitForServerStatus(ctx, sc, serverID, []string{"SHELVED", "SHELVED_OFFLOADED"}, time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if *reads != 2 {
			t.Errorf("reads = %d, want 2", *reads)
		}
	})
	t.Run("error status", func(t *testing.T) {
		sc, _ := serverStates(t, state("ERROR", ""))
		err := compute.WaitForServerStatus(ctx, sc, serverID, []string{"ACTIVE"}, time.Millisecond)
		if err == nil || err.Error() != `server entered ERROR status while waiting for "ACTIVE"` {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		sc, _ := serverStates(t, state("ACTIVE", "migrating"))
		dctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		err := compute.WaitForServerStatus(dctx, sc, serverID, []string{"ACTIVE"}, time.Millisecond)
		if !errors.Is(err, context.DeadlineExceeded) ||
			!strings.HasPrefix(err.Error(), `waiting for status "ACTIVE" (last status "ACTIVE (task migrating)"): `) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("failed read", func(t *testing.T) {
		sc, _ := serverStates(t, "500")
		err := compute.WaitForServerStatus(ctx, sc, serverID, []string{"ACTIVE"}, time.Millisecond)
		var unexpected gophercloud.ErrUnexpectedResponseCode
		if !errors.As(err, &unexpected) || strings.HasPrefix(err.Error(), "waiting") {
			t.Fatalf("a failed read is returned as is: %v", err)
		}
	})
	t.Run("deadline before the first read", func(t *testing.T) {
		sc, _ := serverStates(t, state("BUILD", ""))
		dctx, cancel := context.WithCancel(ctx)
		cancel()
		err := compute.WaitForServerStatus(dctx, sc, serverID, []string{"ACTIVE"}, time.Millisecond)
		if !errors.Is(err, context.Canceled) || err.Error() != `waiting for status "ACTIVE": context canceled` {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestWaitForServerDeleted(t *testing.T) {
	ctx := context.Background()
	t.Run("gone", func(t *testing.T) {
		sc, reads := serverStates(t, state("ACTIVE", "deleting"), state("DELETED", ""), "404")
		if err := compute.WaitForServerDeleted(ctx, sc, serverID, time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if *reads != 3 {
			t.Errorf("reads = %d, want 3", *reads)
		}
	})
	t.Run("error is not a failure", func(t *testing.T) {
		sc, _ := serverStates(t, state("ERROR", ""), "404")
		if err := compute.WaitForServerDeleted(ctx, sc, serverID, time.Millisecond); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("soft deleted", func(t *testing.T) {
		sc, _ := serverStates(t, state("SOFT_DELETED", ""))
		if err := compute.WaitForServerDeleted(ctx, sc, serverID, time.Millisecond); !errors.Is(err, compute.ErrServerSoftDeleted) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		sc, _ := serverStates(t, state("ACTIVE", "deleting"))
		dctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		err := compute.WaitForServerDeleted(dctx, sc, serverID, time.Millisecond)
		if !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(err.Error(), `waiting for deletion (last status "ACTIVE"): `) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("failed read", func(t *testing.T) {
		sc, _ := serverStates(t, "500")
		var unexpected gophercloud.ErrUnexpectedResponseCode
		if err := compute.WaitForServerDeleted(ctx, sc, serverID, time.Millisecond); !errors.As(err, &unexpected) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("deadline before the first read", func(t *testing.T) {
		sc, _ := serverStates(t, state("ACTIVE", ""))
		dctx, cancel := context.WithCancel(ctx)
		cancel()
		err := compute.WaitForServerDeleted(dctx, sc, serverID, time.Millisecond)
		if !errors.Is(err, context.Canceled) || err.Error() != "waiting for deletion: context canceled" {
			t.Fatalf("err = %v", err)
		}
	})
}
