package network

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// fakeFlags implements the flagSet interface (Changed) so the set/unset seams
// can be driven without cobra.
type fakeFlags map[string]bool

func (f fakeFlags) Changed(name string) bool { return f[name] }

// writeJSON is a tiny helper for handlers returning a canned body.
func writeJSON(t *testing.T, w http.ResponseWriter, status int, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatalf("writing response body: %v", err)
	}
}

// emptyLookup registers a collection GET handler returning an empty list, so a
// name→ID resolver falls back to treating its argument as an ID (pass-through).
func emptyLookup(t *testing.T, fakeServer th.FakeServer, path, key string) {
	t.Helper()
	fakeServer.Mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("collection %s: method = %q, want GET for lookup", path, r.Method)
		}
		writeJSON(t, w, http.StatusOK, `{"`+key+`":[]}`)
	})
}

// --- network: show / delete / set / unset ------------------------------------

func TestRunNetworkShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	emptyLookup(t, fakeServer, "/networks", "networks")
	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/networks/net-1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		writeJSON(t, w, http.StatusOK, `{"network":{"id":"net-1","name":"public","status":"ACTIVE","mtu":1500}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runNetworkShow(context.Background(), client, o, "net-1", &buf); err != nil {
		t.Fatalf("runNetworkShow: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/networks/net-1" {
		t.Errorf("got %s %s, want GET /networks/net-1", gotMethod, gotPath)
	}
	if out := buf.String(); !strings.Contains(out, "public") || !strings.Contains(out, "1500") {
		t.Errorf("output missing expected values:\n%s", out)
	}
}

func TestRunNetworkDelete_MultipleIDs(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	emptyLookup(t, fakeServer, "/networks", "networks")
	deleted := map[string]bool{}
	fakeServer.Mux.HandleFunc("/networks/", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		deleted[strings.TrimPrefix(r.URL.Path, "/networks/")] = true
		w.WriteHeader(http.StatusNoContent)
	})

	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runNetworkDelete(context.Background(), client, []string{"net-1", "net-2"}, &buf); err != nil {
		t.Fatalf("runNetworkDelete: %v", err)
	}
	if !deleted["net-1"] || !deleted["net-2"] {
		t.Errorf("did not delete both networks: %v", deleted)
	}
	out := buf.String()
	if !strings.Contains(out, "Deleted network net-1") || !strings.Contains(out, "Deleted network net-2") {
		t.Errorf("output missing delete confirmations:\n%s", out)
	}
}

func TestRunNetworkSet_NameAndMTU(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	emptyLookup(t, fakeServer, "/networks", "networks")
	fakeServer.Mux.HandleFunc("/networks/net-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"network":{"name":"renamed","mtu":1400}}`)
		writeJSON(t, w, http.StatusOK, `{"network":{"id":"net-1","name":"renamed","mtu":1400}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &networkSetFlags{name: "renamed", mtu: 1400}
	var buf bytes.Buffer
	if err := runNetworkSet(context.Background(), client, o, "net-1", f, fakeFlags{"name": true, "mtu": true}, &buf); err != nil {
		t.Fatalf("runNetworkSet: %v", err)
	}
	if !strings.Contains(buf.String(), "renamed") {
		t.Errorf("output missing new name:\n%s", buf.String())
	}
}

func TestRunNetworkUnset_Share(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	emptyLookup(t, fakeServer, "/networks", "networks")
	fakeServer.Mux.HandleFunc("/networks/net-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"network":{"shared":false}}`)
		writeJSON(t, w, http.StatusOK, `{"network":{"id":"net-1","name":"private","shared":false}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runNetworkUnset(context.Background(), client, o, "net-1", true, &buf); err != nil {
		t.Fatalf("runNetworkUnset: %v", err)
	}
}

func TestRunNetworkUnset_NoFlagErrors(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/networks", "networks")

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runNetworkUnset(context.Background(), client, o, "net-1", false, &buf); err == nil {
		t.Fatal("expected error when no attribute flag is set")
	}
}

// --- subnet: list / show / delete / set --------------------------------------

func TestRunSubnetList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/subnets", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"subnets":[{"id":"sub-1","name":"sn","network_id":"net-1","cidr":"10.0.0.0/24","ip_version":4}]}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runSubnetList(context.Background(), client, o, &buf); err != nil {
		t.Fatalf("runSubnetList: %v", err)
	}
	for _, want := range []string{"sub-1", "sn", "net-1", "10.0.0.0/24"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("subnet list output missing %q\n%s", want, buf.String())
		}
	}
}

func TestRunSubnetShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	emptyLookup(t, fakeServer, "/subnets", "subnets")
	fakeServer.Mux.HandleFunc("/subnets/sub-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"subnet":{"id":"sub-1","name":"sn","cidr":"10.0.0.0/24","ip_version":4}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runSubnetShow(context.Background(), client, o, "sub-1", &buf); err != nil {
		t.Fatalf("runSubnetShow: %v", err)
	}
	if !strings.Contains(buf.String(), "10.0.0.0/24") {
		t.Errorf("output missing cidr:\n%s", buf.String())
	}
}

func TestRunSubnetDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	emptyLookup(t, fakeServer, "/subnets", "subnets")
	fakeServer.Mux.HandleFunc("/subnets/sub-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		w.WriteHeader(http.StatusNoContent)
	})

	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runSubnetDelete(context.Background(), client, []string{"sub-1"}, &buf); err != nil {
		t.Fatalf("runSubnetDelete: %v", err)
	}
	if !strings.Contains(buf.String(), "Deleted subnet sub-1") {
		t.Errorf("output missing confirmation:\n%s", buf.String())
	}
}

func TestRunSubnetSet_NameAndDHCP(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	emptyLookup(t, fakeServer, "/subnets", "subnets")
	fakeServer.Mux.HandleFunc("/subnets/sub-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"subnet":{"name":"newname","enable_dhcp":false}}`)
		writeJSON(t, w, http.StatusOK, `{"subnet":{"id":"sub-1","name":"newname","enable_dhcp":false}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	// enable_dhcp=false is driven by --no-dhcp (f.noDHCP), not a false f.dhcp.
	f := &subnetSetFlags{name: "newname", noDHCP: true}
	var buf bytes.Buffer
	if err := runSubnetSet(context.Background(), client, o, "sub-1", f, fakeFlags{"name": true, "no-dhcp": true}, &buf); err != nil {
		t.Fatalf("runSubnetSet: %v", err)
	}
}

// --- port: list / show / create / delete / set -------------------------------

func TestRunPortList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"ports":[{"id":"port-1","name":"p","mac_address":"aa:bb:cc:dd:ee:ff","status":"ACTIVE"}]}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runPortList(context.Background(), client, o, &portListFlags{}, &buf); err != nil {
		t.Fatalf("runPortList: %v", err)
	}
	for _, want := range []string{"port-1", "aa:bb:cc:dd:ee:ff", "ACTIVE"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("port list output missing %q\n%s", want, buf.String())
		}
	}
}

func TestRunPortShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	emptyLookup(t, fakeServer, "/ports", "ports")
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1","name":"p","mac_address":"aa:bb:cc:dd:ee:ff"}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runPortShow(context.Background(), client, o, "port-1", &buf); err != nil {
		t.Fatalf("runPortShow: %v", err)
	}
	if !strings.Contains(buf.String(), "aa:bb:cc:dd:ee:ff") {
		t.Errorf("output missing mac:\n%s", buf.String())
	}
}

func TestRunPortCreate_WithFixedIPResolvesSubnet(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// network resolve → empty → pass through as ID.
	emptyLookup(t, fakeServer, "/networks", "networks")
	// subnet resolve by name → one match, exercising name→ID resolution.
	fakeServer.Mux.HandleFunc("/subnets", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"subnets":[{"id":"sub-99","name":"sn"}]}`)
	})
	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"port":{"name":"portA","network_id":"net-1","fixed_ips":[{"subnet_id":"sub-99","ip_address":"10.0.0.5"}]}}`)
		writeJSON(t, w, http.StatusCreated, `{"port":{"id":"port-1","name":"portA","network_id":"net-1"}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &portCreateFlags{network: "net-1", fixedIP: []string{"subnet=sn,ip-address=10.0.0.5"}}
	var buf bytes.Buffer
	if err := runPortCreate(context.Background(), client, o, "portA", f, &buf); err != nil {
		t.Fatalf("runPortCreate: %v", err)
	}
}

func TestRunPortDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	emptyLookup(t, fakeServer, "/ports", "ports")
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		w.WriteHeader(http.StatusNoContent)
	})

	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runPortDelete(context.Background(), client, []string{"port-1"}, &buf); err != nil {
		t.Fatalf("runPortDelete: %v", err)
	}
	if !strings.Contains(buf.String(), "Deleted port port-1") {
		t.Errorf("output missing confirmation:\n%s", buf.String())
	}
}

func TestRunPortSet_Name(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	emptyLookup(t, fakeServer, "/ports", "ports")
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"port":{"name":"renamed"}}`)
		writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1","name":"renamed"}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &portSetFlags{name: "renamed"}
	var buf bytes.Buffer
	if err := runPortSet(context.Background(), client, o, "port-1", f, fakeFlags{"name": true}, &buf); err != nil {
		t.Fatalf("runPortSet: %v", err)
	}
}

// --- router: list / show / create / delete / set / add / remove --------------

func TestRunRouterList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/routers", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"routers":[{"id":"rtr-1","name":"r","status":"ACTIVE","admin_state_up":true}]}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runRouterList(context.Background(), client, o, &buf); err != nil {
		t.Fatalf("runRouterList: %v", err)
	}
	for _, want := range []string{"rtr-1", "r", "ACTIVE", "UP"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("router list output missing %q\n%s", want, buf.String())
		}
	}
}

func TestRunRouterShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	emptyLookup(t, fakeServer, "/routers", "routers")
	fakeServer.Mux.HandleFunc("/routers/rtr-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"router":{"id":"rtr-1","name":"r","status":"ACTIVE"}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runRouterShow(context.Background(), client, o, "rtr-1", &buf); err != nil {
		t.Fatalf("runRouterShow: %v", err)
	}
	if !strings.Contains(buf.String(), "rtr-1") {
		t.Errorf("output missing id:\n%s", buf.String())
	}
}

func TestRunRouterCreate_Disabled(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/routers", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"router":{"name":"r","admin_state_up":false}}`)
		writeJSON(t, w, http.StatusCreated, `{"router":{"id":"rtr-1","name":"r","admin_state_up":false}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &routerCreateFlags{disable: true}
	var buf bytes.Buffer
	if err := runRouterCreate(context.Background(), client, o, "r", f, &buf); err != nil {
		t.Fatalf("runRouterCreate: %v", err)
	}
}

func TestRunRouterDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	emptyLookup(t, fakeServer, "/routers", "routers")
	fakeServer.Mux.HandleFunc("/routers/rtr-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		w.WriteHeader(http.StatusNoContent)
	})

	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runRouterDelete(context.Background(), client, []string{"rtr-1"}, &buf); err != nil {
		t.Fatalf("runRouterDelete: %v", err)
	}
	if !strings.Contains(buf.String(), "Deleted router rtr-1") {
		t.Errorf("output missing confirmation:\n%s", buf.String())
	}
}

func TestRunRouterSet_ExternalGatewayResolvesNetwork(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// The router resolve and the gateway network resolve both list /networks
	// and /routers by name; return empty so args pass through as IDs.
	emptyLookup(t, fakeServer, "/routers", "routers")
	emptyLookup(t, fakeServer, "/networks", "networks")
	fakeServer.Mux.HandleFunc("/routers/rtr-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"router":{"name":"renamed","external_gateway_info":{"network_id":"ext-net"}}}`)
		writeJSON(t, w, http.StatusOK, `{"router":{"id":"rtr-1","name":"renamed"}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &routerSetFlags{name: "renamed", externalGateway: "ext-net"}
	var buf bytes.Buffer
	if err := runRouterSet(context.Background(), client, o, "rtr-1", f, fakeFlags{"name": true}, &buf); err != nil {
		t.Fatalf("runRouterSet: %v", err)
	}
}

func TestRunRouterAddSubnet(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	emptyLookup(t, fakeServer, "/routers", "routers")
	emptyLookup(t, fakeServer, "/subnets", "subnets")
	fakeServer.Mux.HandleFunc("/routers/rtr-1/add_router_interface", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"subnet_id":"sub-1"}`)
		writeJSON(t, w, http.StatusOK, `{"id":"rtr-1","subnet_id":"sub-1","port_id":"port-1"}`)
	})

	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runRouterAddSubnet(context.Background(), client, "rtr-1", "sub-1", &buf); err != nil {
		t.Fatalf("runRouterAddSubnet: %v", err)
	}
	if !strings.Contains(buf.String(), "Added interface for subnet sub-1 to router rtr-1") {
		t.Errorf("output missing confirmation:\n%s", buf.String())
	}
}

func TestRunRouterRemoveSubnet(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	emptyLookup(t, fakeServer, "/routers", "routers")
	emptyLookup(t, fakeServer, "/subnets", "subnets")
	fakeServer.Mux.HandleFunc("/routers/rtr-1/remove_router_interface", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"subnet_id":"sub-1"}`)
		writeJSON(t, w, http.StatusOK, `{"id":"rtr-1","subnet_id":"sub-1","port_id":"port-1"}`)
	})

	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runRouterRemoveSubnet(context.Background(), client, "rtr-1", "sub-1", &buf); err != nil {
		t.Fatalf("runRouterRemoveSubnet: %v", err)
	}
	if !strings.Contains(buf.String(), "Removed interface for subnet sub-1 from router rtr-1") {
		t.Errorf("output missing confirmation:\n%s", buf.String())
	}
}

// --- floating ip: list / show / create / delete / set / unset ----------------

func TestRunFloatingIPList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/floatingips", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"floatingips":[{"id":"fip-1","floating_ip_address":"1.2.3.4","status":"ACTIVE"}]}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runFloatingIPList(context.Background(), client, o, &buf); err != nil {
		t.Fatalf("runFloatingIPList: %v", err)
	}
	for _, want := range []string{"fip-1", "1.2.3.4", "ACTIVE"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("floating IP list output missing %q\n%s", want, buf.String())
		}
	}
}

func TestRunFloatingIPShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// resolveFloatingIPID lists by floating_ip_address; return empty → the arg
	// is treated as an ID.
	fakeServer.Mux.HandleFunc("/floatingips", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"floatingips":[]}`)
	})
	fakeServer.Mux.HandleFunc("/floatingips/fip-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"floatingip":{"id":"fip-1","floating_ip_address":"1.2.3.4"}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runFloatingIPShow(context.Background(), client, o, "fip-1", &buf); err != nil {
		t.Fatalf("runFloatingIPShow: %v", err)
	}
	if !strings.Contains(buf.String(), "1.2.3.4") {
		t.Errorf("output missing address:\n%s", buf.String())
	}
}

func TestRunFloatingIPCreate_ResolvesNetwork(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"networks":[]}`)
	})
	fakeServer.Mux.HandleFunc("/floatingips", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"floatingip":{"floating_network_id":"ext-net"}}`)
		writeJSON(t, w, http.StatusCreated, `{"floatingip":{"id":"fip-1","floating_network_id":"ext-net","floating_ip_address":"1.2.3.4"}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &floatingIPCreateFlags{}
	var buf bytes.Buffer
	if err := runFloatingIPCreate(context.Background(), client, o, "ext-net", f, &buf); err != nil {
		t.Fatalf("runFloatingIPCreate: %v", err)
	}
}

func TestRunFloatingIPDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/floatingips", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"floatingips":[]}`)
	})
	fakeServer.Mux.HandleFunc("/floatingips/fip-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		w.WriteHeader(http.StatusNoContent)
	})

	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runFloatingIPDelete(context.Background(), client, []string{"fip-1"}, &buf); err != nil {
		t.Fatalf("runFloatingIPDelete: %v", err)
	}
	if !strings.Contains(buf.String(), "Deleted floating IP fip-1") {
		t.Errorf("output missing confirmation:\n%s", buf.String())
	}
}

func TestRunFloatingIPSet_AssociatesPort(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/floatingips", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"floatingips":[]}`)
	})
	emptyLookup(t, fakeServer, "/ports", "ports")
	fakeServer.Mux.HandleFunc("/floatingips/fip-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"floatingip":{"port_id":"port-1"}}`)
		writeJSON(t, w, http.StatusOK, `{"floatingip":{"id":"fip-1","port_id":"port-1"}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &floatingIPSetFlags{port: "port-1"}
	var buf bytes.Buffer
	if err := runFloatingIPSet(context.Background(), client, o, "fip-1", f, &buf); err != nil {
		t.Fatalf("runFloatingIPSet: %v", err)
	}
}

func TestRunFloatingIPUnset_DisassociatesPort(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/floatingips", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"floatingips":[]}`)
	})
	fakeServer.Mux.HandleFunc("/floatingips/fip-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"floatingip":{"port_id":null}}`)
		writeJSON(t, w, http.StatusOK, `{"floatingip":{"id":"fip-1"}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runFloatingIPUnset(context.Background(), client, o, "fip-1", true, &buf); err != nil {
		t.Fatalf("runFloatingIPUnset: %v", err)
	}
}

func TestRunFloatingIPUnset_NoPortErrors(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/floatingips", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"floatingips":[]}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runFloatingIPUnset(context.Background(), client, o, "fip-1", false, &buf); err == nil {
		t.Fatal("expected error when --port not given")
	}
}

// --- agent: show / delete / set ----------------------------------------------

func TestRunAgentShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/agents/agent-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"agent":{"id":"agent-1","agent_type":"L3 agent","host":"cmp1","binary":"neutron-l3-agent"}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runAgentShow(context.Background(), client, o, "agent-1", &buf); err != nil {
		t.Fatalf("runAgentShow: %v", err)
	}
	if !strings.Contains(buf.String(), "L3 agent") {
		t.Errorf("output missing agent type:\n%s", buf.String())
	}
}

func TestRunAgentDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/agents/agent-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		w.WriteHeader(http.StatusNoContent)
	})

	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runAgentDelete(context.Background(), client, []string{"agent-1"}, &buf); err != nil {
		t.Fatalf("runAgentDelete: %v", err)
	}
	if !strings.Contains(buf.String(), "Deleted agent agent-1") {
		t.Errorf("output missing confirmation:\n%s", buf.String())
	}
}

func TestRunAgentSet_Disable(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/agents/agent-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"agent":{"admin_state_up":false}}`)
		writeJSON(t, w, http.StatusOK, `{"agent":{"id":"agent-1","admin_state_up":false}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &agentSetFlags{disable: true}
	var buf bytes.Buffer
	if err := runAgentSet(context.Background(), client, o, "agent-1", f, fakeFlags{"disable": true}, &buf); err != nil {
		t.Fatalf("runAgentSet: %v", err)
	}
}

func TestRunAgentSet_NoFlagErrors(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runAgentSet(context.Background(), client, o, "agent-1", &agentSetFlags{}, fakeFlags{}, &buf); err == nil {
		t.Fatal("expected error when neither --enable nor --disable is set")
	}
}

// --- security group: list / show / create / delete / set ---------------------

func TestRunSecurityGroupList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/security-groups", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"security_groups":[{"id":"sg-1","name":"default","description":"desc"}]}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runSecurityGroupList(context.Background(), client, o, &buf); err != nil {
		t.Fatalf("runSecurityGroupList: %v", err)
	}
	for _, want := range []string{"sg-1", "default", "desc"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("security group list output missing %q\n%s", want, buf.String())
		}
	}
}

func TestRunSecurityGroupShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	emptyLookup(t, fakeServer, "/security-groups", "security_groups")
	fakeServer.Mux.HandleFunc("/security-groups/sg-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"security_group":{"id":"sg-1","name":"default"}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runSecurityGroupShow(context.Background(), client, o, "sg-1", &buf); err != nil {
		t.Fatalf("runSecurityGroupShow: %v", err)
	}
	if !strings.Contains(buf.String(), "default") {
		t.Errorf("output missing name:\n%s", buf.String())
	}
}

func TestRunSecurityGroupCreate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/security-groups", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"security_group":{"name":"web","description":"web tier"}}`)
		writeJSON(t, w, http.StatusCreated, `{"security_group":{"id":"sg-1","name":"web","description":"web tier"}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runSecurityGroupCreate(context.Background(), client, o, "web", "web tier", &buf); err != nil {
		t.Fatalf("runSecurityGroupCreate: %v", err)
	}
}

func TestRunSecurityGroupDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	emptyLookup(t, fakeServer, "/security-groups", "security_groups")
	fakeServer.Mux.HandleFunc("/security-groups/sg-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		w.WriteHeader(http.StatusNoContent)
	})

	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runSecurityGroupDelete(context.Background(), client, []string{"sg-1"}, &buf); err != nil {
		t.Fatalf("runSecurityGroupDelete: %v", err)
	}
	if !strings.Contains(buf.String(), "Deleted security group sg-1") {
		t.Errorf("output missing confirmation:\n%s", buf.String())
	}
}

func TestRunSecurityGroupSet_NameAndDescription(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	emptyLookup(t, fakeServer, "/security-groups", "security_groups")
	fakeServer.Mux.HandleFunc("/security-groups/sg-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"security_group":{"name":"renamed","description":"new desc"}}`)
		writeJSON(t, w, http.StatusOK, `{"security_group":{"id":"sg-1","name":"renamed","description":"new desc"}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &secGroupSetFlags{name: "renamed", description: "new desc"}
	var buf bytes.Buffer
	if err := runSecurityGroupSet(context.Background(), client, o, "sg-1", f, fakeFlags{"name": true, "description": true}, &buf); err != nil {
		t.Fatalf("runSecurityGroupSet: %v", err)
	}
}

// --- security group rule: list / show / delete -------------------------------

func TestRunSecurityGroupRuleList_FilteredByGroup(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// resolveSecGroupID → empty → group arg passes through as ID and becomes a
	// security_group_id filter on the rule list.
	emptyLookup(t, fakeServer, "/security-groups", "security_groups")
	fakeServer.Mux.HandleFunc("/security-group-rules", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		th.TestFormValues(t, r, map[string]string{"security_group_id": "sg-1"})
		writeJSON(t, w, http.StatusOK, `{"security_group_rules":[{"id":"rule-1","direction":"ingress","ethertype":"IPv4","protocol":"tcp","port_range_min":80,"port_range_max":80,"security_group_id":"sg-1"}]}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runSecurityGroupRuleList(context.Background(), client, o, "sg-1", &buf); err != nil {
		t.Fatalf("runSecurityGroupRuleList: %v", err)
	}
	for _, want := range []string{"rule-1", "ingress", "tcp", "80"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("rule list output missing %q\n%s", want, buf.String())
		}
	}
}

func TestRunSecurityGroupRuleShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/security-group-rules/rule-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"security_group_rule":{"id":"rule-1","direction":"ingress","ethertype":"IPv4","protocol":"tcp"}}`)
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runSecurityGroupRuleShow(context.Background(), client, o, "rule-1", &buf); err != nil {
		t.Fatalf("runSecurityGroupRuleShow: %v", err)
	}
	if !strings.Contains(buf.String(), "rule-1") {
		t.Errorf("output missing id:\n%s", buf.String())
	}
}

func TestRunSecurityGroupRuleDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/security-group-rules/rule-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		w.WriteHeader(http.StatusNoContent)
	})

	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runSecurityGroupRuleDelete(context.Background(), client, []string{"rule-1"}, &buf); err != nil {
		t.Fatalf("runSecurityGroupRuleDelete: %v", err)
	}
	if !strings.Contains(buf.String(), "Deleted security group rule rule-1") {
		t.Errorf("output missing confirmation:\n%s", buf.String())
	}
}
