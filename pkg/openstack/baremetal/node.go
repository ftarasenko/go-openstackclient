package baremetal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/paging"
)

// NodeListOpts is gophercloud's nodes.ListOpts plus the two boolean filters
// it cannot send as false: its plain bools drop the false zero value, so the
// API would return every node. A nil pointer leaves the filter off.
type NodeListOpts struct {
	nodes.ListOpts
	Maintenance *bool
	Associated  *bool
}

// ToNodeListQuery implements nodes.ListOptsBuilder.
func (o NodeListOpts) ToNodeListQuery() (string, error) {
	s, err := o.ListOpts.ToNodeListQuery()
	return o.withBools(s, err)
}

// ToNodeListDetailQuery implements nodes.ListOptsBuilder for /nodes/detail.
func (o NodeListOpts) ToNodeListDetailQuery() (string, error) {
	s, err := o.ListOpts.ToNodeListDetailQuery()
	return o.withBools(s, err)
}

func (o NodeListOpts) withBools(s string, err error) (string, error) {
	if err != nil {
		return "", err
	}
	var kv []string
	if o.Maintenance != nil {
		kv = append(kv, "maintenance="+strconv.FormatBool(*o.Maintenance))
	}
	if o.Associated != nil {
		kv = append(kv, "associated="+strconv.FormatBool(*o.Associated))
	}
	if len(kv) == 0 {
		return s, nil
	}
	sep := "?"
	if strings.Contains(s, "?") {
		sep = "&"
	}
	return s + sep + strings.Join(kv, "&"), nil
}

// ListNodes lists nodes, up to limit (0: all). detail reads /nodes/detail,
// whose items carry every field; the plain list has six. Items are the JSON
// objects ironic sent, so fields newer than gophercloud's Node survive.
func ListNodes(ctx context.Context, c *gophercloud.ServiceClient, opts nodes.ListOptsBuilder,
	detail bool, limit int,
) ([]map[string]any, error) {
	list := nodes.List
	if detail {
		list = nodes.ListDetail
	}
	all, err := paging.Collect(ctx, list(c, opts), limit, pageMaps("nodes"))
	return all, wrap(err, "listing baremetal nodes")
}

// GetNode reads a node by UUID or name.
func GetNode(ctx context.Context, c *gophercloud.ServiceClient, id string) (map[string]any, error) {
	n, err := resultMap(nodes.Get(ctx, c, id).Result)
	return n, wrap(err, "getting baremetal node %s", id)
}

// CreateNode enrolls a node.
func CreateNode(ctx context.Context, c *gophercloud.ServiceClient, opts nodes.CreateOptsBuilder) (map[string]any, error) {
	n, err := resultMap(nodes.Create(ctx, c, opts).Result)
	return n, wrap(err, "creating baremetal node")
}

// UpdateNode applies a JSON patch to a node and returns it. Setting /shard
// is gated by FeatureShards.
func UpdateNode(ctx context.Context, c *gophercloud.ServiceClient, id string, ops nodes.UpdateOpts) (map[string]any, error) {
	n, err := resultMap(nodes.Update(ctx, c, id, ops).Result)
	if err != nil {
		for _, op := range ops {
			if u, ok := op.(nodes.UpdateOperation); ok && u.Path == "/shard" && u.Op != nodes.RemoveOp {
				err = ExplainMicroversion(ctx, c, FeatureShards, err)
				break
			}
		}
	}
	return n, wrap(err, "updating baremetal node %s", id)
}

// DeleteNode deletes a node.
func DeleteNode(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(nodes.Delete(ctx, c, id).ExtractErr(), "deleting baremetal node %s", id)
}

// SetPowerState requests a power state change.
func SetPowerState(ctx context.Context, c *gophercloud.ServiceClient, id string, target nodes.TargetPowerState) error {
	err := nodes.ChangePowerState(ctx, c, id, nodes.PowerStateOpts{Target: target}).ExtractErr()
	return wrap(err, "changing power state of node %s", id)
}

// SetMaintenance puts a node into maintenance mode; reason may be empty.
func SetMaintenance(ctx context.Context, c *gophercloud.ServiceClient, id, reason string) error {
	err := nodes.SetMaintenance(ctx, c, id, nodes.MaintenanceOpts{Reason: reason}).ExtractErr()
	return wrap(err, "setting maintenance on node %s", id)
}

// UnsetMaintenance takes a node out of maintenance mode.
func UnsetMaintenance(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(nodes.UnsetMaintenance(ctx, c, id).ExtractErr(), "unsetting maintenance on node %s", id)
}

// SetBootDevice sets a node's boot device.
func SetBootDevice(ctx context.Context, c *gophercloud.ServiceClient, id string, opts nodes.BootDeviceOpts) error {
	return wrap(nodes.SetBootDevice(ctx, c, id, opts).ExtractErr(), "setting boot device for node %s", id)
}

// GetBootDevice reads a node's boot device.
func GetBootDevice(ctx context.Context, c *gophercloud.ServiceClient, id string) (*nodes.BootDeviceOpts, error) {
	bd, err := nodes.GetBootDevice(ctx, c, id).Extract()
	return bd, wrap(err, "getting boot device for node %s", id)
}

// ValidateNode asks each of a node's interfaces whether it is ready.
func ValidateNode(ctx context.Context, c *gophercloud.ServiceClient, id string) (*nodes.NodeValidation, error) {
	v, err := nodes.Validate(ctx, c, id).Extract()
	return v, wrap(err, "validating node %s", id)
}

// InjectNMI injects a non-maskable interrupt.
func InjectNMI(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(nodes.InjectNMI(ctx, c, id).ExtractErr(), "injecting NMI into node %s", id)
}

// GetNodeConsole reads a node's console state (console_enabled, console_info).
func GetNodeConsole(ctx context.Context, c *gophercloud.ServiceClient, node string) (map[string]any, error) {
	var body map[string]any
	err := ironicGet(ctx, c, joinPath("nodes", node, "states", "console"), &body)
	return body, wrap(err, "getting console of baremetal node %s", node)
}

// SetNodeConsole turns a node's serial console on or off.
func SetNodeConsole(ctx context.Context, c *gophercloud.ServiceClient, node string, on bool) error {
	err := ironicPut(ctx, c, joinPath("nodes", node, "states", "console"), map[string]any{"enabled": on})
	return wrap(err, "turning console %s for baremetal node %s", onOff(on), node)
}

// SetNodeSecureBoot turns a node's UEFI secure boot on or off.
func SetNodeSecureBoot(ctx context.Context, c *gophercloud.ServiceClient, node string, on bool) error {
	err := ironicPut(ctx, c, joinPath("nodes", node, "states", "secure_boot"), map[string]any{"target": on})
	return wrap(err, "turning secure boot %s for baremetal node %s", onOff(on), node)
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// ListVIFs lists the virtual interfaces attached to a node.
func ListVIFs(ctx context.Context, c *gophercloud.ServiceClient, id string) ([]nodes.VIF, error) {
	vifs, err := nodes.ListVirtualInterfaces(ctx, c, id).Extract()
	return vifs, wrap(err, "listing virtual interfaces of node %s", id)
}

// AttachVIF attaches a virtual interface to a node.
func AttachVIF(ctx context.Context, c *gophercloud.ServiceClient, id string, opts nodes.VirtualInterfaceOptsBuilder) error {
	err := nodes.AttachVirtualInterface(ctx, c, id, opts).ExtractErr()
	return wrap(err, "attaching a virtual interface to node %s", id)
}

// DetachVIF detaches a virtual interface from a node.
func DetachVIF(ctx context.Context, c *gophercloud.ServiceClient, id, vif string) error {
	err := nodes.DetachVirtualInterface(ctx, c, id, vif).ExtractErr()
	return wrap(err, "detaching VIF %s from node %s", vif, id)
}

// ListBIOSSettings lists a node's BIOS settings; detail adds their
// registry (type, bounds, flags).
func ListBIOSSettings(ctx context.Context, c *gophercloud.ServiceClient, id string, detail bool) ([]nodes.BIOSSetting, error) {
	s, err := nodes.ListBIOSSettings(ctx, c, id, nodes.ListBIOSSettingsOpts{Detail: detail}).Extract()
	return s, wrap(err, "listing BIOS settings of node %s", id)
}

// GetBIOSSetting reads one BIOS setting. Ironic keys the answer by the
// setting's name.
func GetBIOSSetting(ctx context.Context, c *gophercloud.ServiceClient, id, name string) (*nodes.BIOSSetting, error) {
	s, err := extractBIOSSetting(nodes.GetBIOSSetting(ctx, c, id, name), name)
	return s, wrap(err, "getting BIOS setting %s of node %s", name, id)
}

func extractBIOSSetting(res nodes.GetBIOSSettingResult, name string) (*nodes.BIOSSetting, error) {
	if res.Err != nil {
		return nil, res.Err
	}
	raw, err := json.Marshal(res.Body)
	if err != nil {
		return nil, err
	}
	var byName map[string]nodes.BIOSSetting
	if err := json.Unmarshal(raw, &byName); err != nil {
		return nil, err
	}
	if s, ok := byName[name]; ok {
		return &s, nil
	}
	if len(byName) == 1 {
		for _, s := range byName {
			return &s, nil
		}
	}
	return nil, fmt.Errorf("response contained no setting named %q", name)
}

// ListFirmware lists a node's firmware components (FeatureNodeFirmware).
func ListFirmware(ctx context.Context, c *gophercloud.ServiceClient, id string) ([]nodes.FirmwareComponent, error) {
	fw, err := nodes.ListFirmware(ctx, c, id).Extract()
	return fw, ExplainMicroversion(ctx, c, FeatureNodeFirmware, wrap(err, "listing firmware components of node %s", id))
}

// GetNodeInventory reads the hardware inventory inspection recorded
// (FeatureNodeInventory).
func GetNodeInventory(ctx context.Context, c *gophercloud.ServiceClient, id string) (*nodes.InventoryData, error) {
	inv, err := nodes.GetInventory(ctx, c, id).Extract()
	return inv, wrap(ExplainMicroversion(ctx, c, FeatureNodeInventory, err), "getting inventory for node %s", id)
}

// GetNodeInventoryRaw is GetNodeInventory's body as ironic sent it, with the
// plugin data gophercloud's type does not model.
func GetNodeInventoryRaw(ctx context.Context, c *gophercloud.ServiceClient, id string) (any, error) {
	res := nodes.GetInventory(ctx, c, id)
	return res.Body, wrap(ExplainMicroversion(ctx, c, FeatureNodeInventory, res.Err), "getting inventory for node %s", id)
}

// ListNodeTraits lists a node's traits.
func ListNodeTraits(ctx context.Context, c *gophercloud.ServiceClient, node string) ([]string, error) {
	var body struct {
		Traits []string `json:"traits"`
	}
	err := ironicGet(ctx, c, joinPath("nodes", node, "traits"), &body)
	return body.Traits, wrap(err, "listing traits of baremetal node %s", node)
}

// AddNodeTrait adds one trait to a node.
func AddNodeTrait(ctx context.Context, c *gophercloud.ServiceClient, node, trait string) error {
	return wrap(ironicPut(ctx, c, joinPath("nodes", node, "traits", trait), nil), "failed to add trait %s", trait)
}

// RemoveNodeTrait removes one trait from a node.
func RemoveNodeTrait(ctx context.Context, c *gophercloud.ServiceClient, node, trait string) error {
	return wrap(ironicDelete(ctx, c, joinPath("nodes", node, "traits", trait)), "failed to remove trait %s", trait)
}

// RemoveNodeTraits removes every trait from a node.
func RemoveNodeTraits(ctx context.Context, c *gophercloud.ServiceClient, node string) error {
	return wrap(ironicDelete(ctx, c, joinPath("nodes", node, "traits")), "removing all traits of baremetal node %s", node)
}

// ListNodeHistory lists a node's history events; detail adds each event's
// full record.
func ListNodeHistory(ctx context.Context, c *gophercloud.ServiceClient, node string, detail bool) ([]map[string]any, error) {
	q := url.Values{}
	if detail {
		q.Set("detail", "True")
	}
	events, err := ironicList(ctx, c, joinPath("nodes", node, "history"), "history", q, 0)
	return events, wrap(err, "listing history of baremetal node %s", node)
}

// GetNodeHistoryEvent reads one history event.
func GetNodeHistoryEvent(ctx context.Context, c *gophercloud.ServiceClient, node, event string) (map[string]any, error) {
	var body map[string]any
	err := ironicGet(ctx, c, joinPath("nodes", node, "history", event), &body)
	return body, wrap(err, "getting history event %s of baremetal node %s", event, node)
}

// ListNodeChildren lists the UUIDs of a node's child nodes
// (FeatureNodeChildren).
func ListNodeChildren(ctx context.Context, c *gophercloud.ServiceClient, node string) ([]string, error) {
	var body struct {
		Children []string `json:"children"`
	}
	err := ironicGet(ctx, c, joinPath("nodes", node, "children"), &body)
	return body.Children, wrap(ExplainMicroversion(ctx, c, FeatureNodeChildren, err), "listing children of baremetal node %s", node)
}

// PassthruMethod describes one vendor passthru method of a node's driver.
type PassthruMethod struct {
	HTTPMethods []string `json:"http_methods"`
	Async       bool     `json:"async"`
	Description string   `json:"description"`
	Attach      bool     `json:"attach"`
}

// ListPassthruMethods lists a node's vendor passthru methods by name.
func ListPassthruMethods(ctx context.Context, c *gophercloud.ServiceClient, node string) (map[string]PassthruMethod, error) {
	var methods map[string]PassthruMethod
	err := ironicGet(ctx, c, joinPath("nodes", node, "vendor_passthru", "methods"), &methods)
	return methods, wrap(err, "listing vendor passthru methods of baremetal node %s", node)
}

// CallPassthru calls a vendor passthru method and returns the response body
// as sent. body is sent for every method but GET and DELETE.
func CallPassthru(ctx context.Context, c *gophercloud.ServiceClient, node, method, httpMethod string,
	body map[string]any,
) ([]byte, error) {
	opts := &gophercloud.RequestOpts{
		OkCodes:          []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent},
		KeepResponseBody: true,
	}
	if httpMethod != http.MethodGet && httpMethod != http.MethodDelete {
		opts.JSONBody = body
	}
	resp, err := c.Request(ctx, httpMethod, c.ServiceURL(joinPath("nodes", node, "vendor_passthru", method)), opts)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		return nil, wrap(err, "calling vendor passthru %s on baremetal node %s", method, node)
	}
	return io.ReadAll(resp.Body)
}

// VirtualMediaOpts is the body of a virtual media attach.
type VirtualMediaOpts struct {
	DeviceType string `json:"device_type"`
	// ImageDownloadSource is http, local or swift; empty leaves ironic's
	// default.
	ImageDownloadSource string `json:"image_download_source,omitempty"`
	ImageURL            string `json:"image_url"`
}

// AttachVirtualMedia attaches an image to a node as virtual media
// (FeatureNodeVmedia).
func AttachVirtualMedia(ctx context.Context, c *gophercloud.ServiceClient, node string, opts VirtualMediaOpts) error {
	err := ironicPost(ctx, c, joinPath("nodes", node, "vmedia"), opts, nil)
	return wrap(ExplainMicroversion(ctx, c, FeatureNodeVmedia, err), "attaching virtual media to baremetal node %s", node)
}

// DetachVirtualMedia detaches a node's virtual media of deviceTypes, or all
// of it when none is named (FeatureNodeVmedia).
func DetachVirtualMedia(ctx context.Context, c *gophercloud.ServiceClient, node string, deviceTypes []string) error {
	path := joinPath("nodes", node, "vmedia")
	if len(deviceTypes) > 0 {
		path += "?" + url.Values{"device_types": {strings.Join(deviceTypes, ",")}}.Encode()
	}
	err := ironicDelete(ctx, c, path)
	return wrap(ExplainMicroversion(ctx, c, FeatureNodeVmedia, err), "detaching virtual media from baremetal node %s", node)
}
