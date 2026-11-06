package baremetal

import (
	"context"
	"net/url"
	"strings"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/koc/pkg/openstack/resolve"
)

// The ironic resources gophercloud has no package for — port groups, deploy
// templates, runbooks, inspection rules, volume connectors and targets — are
// read and written as the JSON objects ironic sends and takes. Create takes
// the request body; update takes a Patch.

// resource is one such collection.
type resource struct {
	path string // "volume/connectors"
	key  string // "connectors", the list's member
	kind string // "baremetal volume connector", as messages name it
}

func (r resource) item(id string) string {
	return joinPath(append(strings.Split(r.path, "/"), id)...)
}

// gate explains err for f, or returns it as it is when f is "".
func gate(ctx context.Context, c *gophercloud.ServiceClient, f Feature, err error) error {
	if f == "" {
		return err
	}
	return ExplainMicroversion(ctx, c, f, err)
}

func (r resource) list(ctx context.Context, c *gophercloud.ServiceClient, path string, q url.Values, limit int,
	f Feature,
) ([]map[string]any, error) {
	all, err := ironicList(ctx, c, path, r.key, q, limit)
	return all, wrap(gate(ctx, c, f, err), "listing %ss", r.kind)
}

func (r resource) get(ctx context.Context, c *gophercloud.ServiceClient, id string, f Feature) (map[string]any, error) {
	var m map[string]any
	err := ironicGet(ctx, c, r.item(id), &m)
	return m, wrap(gate(ctx, c, f, err), "getting %s %s", r.kind, id)
}

func (r resource) create(ctx context.Context, c *gophercloud.ServiceClient, body map[string]any, f Feature) (map[string]any, error) {
	var m map[string]any
	err := ironicPost(ctx, c, r.path, body, &m)
	return m, wrap(gate(ctx, c, f, err), "creating %s", r.kind)
}

func (r resource) update(ctx context.Context, c *gophercloud.ServiceClient, id string, ops Patch, f Feature) (map[string]any, error) {
	var m map[string]any
	err := ironicPatch(ctx, c, r.item(id), ops, &m)
	return m, wrap(gate(ctx, c, f, err), "updating %s %s", r.kind, id)
}

func (r resource) delete(ctx context.Context, c *gophercloud.ServiceClient, id string, f Feature) error {
	return wrap(gate(ctx, c, f, ironicDelete(ctx, c, r.item(id))), "deleting %s %s", r.kind, id)
}

// sets reports whether ops adds or replaces path.
func (p Patch) sets(path string) bool {
	for _, op := range p {
		if op.Path == path && op.Op != "remove" {
			return true
		}
	}
	return false
}

// touches reports whether any of ops names path.
func (p Patch) touches(path string) bool {
	for _, op := range p {
		if op.Path == path {
			return true
		}
	}
	return false
}

// --- port groups -------------------------------------------------------------

func portGroups() resource { return resource{"portgroups", "portgroups", "baremetal port group"} }

// ListPortGroups lists port groups matching q (node, address, shard, marker,
// sort_key, sort_dir; empty values are dropped), up to limit (0: all).
// detail reads /portgroups/detail.
func ListPortGroups(ctx context.Context, c *gophercloud.ServiceClient, q url.Values, detail bool, limit int) ([]map[string]any, error) {
	path := "portgroups"
	if detail {
		path = "portgroups/detail"
	}
	return portGroups().list(ctx, c, path, q, limit, "")
}

// GetPortGroup reads a port group by UUID or name.
func GetPortGroup(ctx context.Context, c *gophercloud.ServiceClient, ref string) (map[string]any, error) {
	return portGroups().get(ctx, c, ref, "")
}

// FindPortGroupByAddress reads the one port group with a MAC address: none
// is a *resolve.NotFoundError, several a *resolve.AmbiguousError.
func FindPortGroupByAddress(ctx context.Context, c *gophercloud.ServiceClient, address string) (map[string]any, error) {
	groups, err := ironicList(ctx, c, "portgroups/detail", "portgroups", url.Values{"address": {address}}, 0)
	if err != nil {
		return nil, wrap(err, "getting baremetal port group with address %s", address)
	}
	if len(groups) == 0 {
		return nil, &resolve.NotFoundError{Kind: "baremetal port group", Ref: address}
	}
	if len(groups) > 1 {
		return nil, &resolve.AmbiguousError{Kind: "baremetal port group", Ref: address, Count: len(groups)}
	}
	return groups[0], nil
}

// ListPortGroupPorts lists the ports of a port group.
func ListPortGroupPorts(ctx context.Context, c *gophercloud.ServiceClient, id string) ([]map[string]any, error) {
	members, err := ironicList(ctx, c, joinPath("portgroups", id, "ports"), "ports", nil, 0)
	return members, wrap(err, "listing ports of port group %s", id)
}

// PortGroupMembers maps each port group UUID to its member port UUIDs, read
// in one port listing; node, when set, limits it to that node's ports.
func PortGroupMembers(ctx context.Context, c *gophercloud.ServiceClient, node string) (map[string][]string, error) {
	members, err := ironicList(ctx, c, "ports", "ports", url.Values{"node": {node}, "fields": {"uuid,portgroup_uuid"}}, 0)
	if err != nil {
		return nil, wrap(err, "listing member ports")
	}
	byGroup := map[string][]string{}
	for _, p := range members {
		if g, _ := p["portgroup_uuid"].(string); g != "" {
			id, _ := p["uuid"].(string)
			byGroup[g] = append(byGroup[g], id)
		}
	}
	return byGroup, nil
}

// portGroupFeature is the gated attribute a port group write sets, if any.
func portGroupFeature(physnet, category bool) Feature {
	switch {
	case physnet:
		return FeaturePortGroupPhysnet
	case category:
		return FeaturePortGroupCategory
	}
	return ""
}

// CreatePortGroup creates a port group. physical_network and category are
// gated (FeaturePortGroupPhysnet, FeaturePortGroupCategory).
func CreatePortGroup(ctx context.Context, c *gophercloud.ServiceClient, body map[string]any) (map[string]any, error) {
	f := portGroupFeature(body["physical_network"] != nil, body["category"] != nil)
	return portGroups().create(ctx, c, body, f)
}

// UpdatePortGroup applies a JSON patch to a port group. Setting
// physical_network or category is gated.
func UpdatePortGroup(ctx context.Context, c *gophercloud.ServiceClient, ref string, ops Patch) (map[string]any, error) {
	f := portGroupFeature(ops.sets("/physical_network"), ops.sets("/category"))
	return portGroups().update(ctx, c, ref, ops, f)
}

// DeletePortGroup deletes a port group.
func DeletePortGroup(ctx context.Context, c *gophercloud.ServiceClient, ref string) error {
	return portGroups().delete(ctx, c, ref, "")
}

// NodeUUID returns the UUID of a node named by UUID or name, reading it, so a
// node written into another resource is known to exist.
func NodeUUID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	var n struct {
		UUID string `json:"uuid"`
	}
	if err := ironicGet(ctx, c, joinPath("nodes", ref)+"?fields=uuid", &n); err != nil {
		return "", wrap(err, "resolving baremetal node %s", ref)
	}
	return n.UUID, nil
}

// --- deploy templates --------------------------------------------------------

func deployTemplates() resource {
	return resource{"deploy_templates", "deploy_templates", "baremetal deploy template"}
}

// ListDeployTemplates lists deploy templates matching q (marker, sort_key,
// sort_dir, detail=True), up to limit (0: all).
func ListDeployTemplates(ctx context.Context, c *gophercloud.ServiceClient, q url.Values, limit int) ([]map[string]any, error) {
	return deployTemplates().list(ctx, c, "deploy_templates", q, limit, "")
}

// GetDeployTemplate reads a deploy template by UUID or name.
func GetDeployTemplate(ctx context.Context, c *gophercloud.ServiceClient, ref string) (map[string]any, error) {
	return deployTemplates().get(ctx, c, ref, "")
}

// CreateDeployTemplate creates a deploy template.
func CreateDeployTemplate(ctx context.Context, c *gophercloud.ServiceClient, body map[string]any) (map[string]any, error) {
	return deployTemplates().create(ctx, c, body, "")
}

// UpdateDeployTemplate applies a JSON patch to a deploy template.
func UpdateDeployTemplate(ctx context.Context, c *gophercloud.ServiceClient, ref string, ops Patch) (map[string]any, error) {
	return deployTemplates().update(ctx, c, ref, ops, "")
}

// DeleteDeployTemplate deletes a deploy template.
func DeleteDeployTemplate(ctx context.Context, c *gophercloud.ServiceClient, ref string) error {
	return deployTemplates().delete(ctx, c, ref, "")
}

// --- runbooks ----------------------------------------------------------------

func runbooks() resource { return resource{"runbooks", "runbooks", "baremetal runbook"} }

// runbookFeature is FeatureRunbookDescription for a write naming the
// description, the higher requirement, and FeatureRunbooks otherwise.
func runbookFeature(description bool) Feature {
	if description {
		return FeatureRunbookDescription
	}
	return FeatureRunbooks
}

// ListRunbooks lists runbooks matching q (marker, sort_key, sort_dir,
// detail=True), up to limit (0: all). Runbooks are FeatureRunbooks.
func ListRunbooks(ctx context.Context, c *gophercloud.ServiceClient, q url.Values, limit int) ([]map[string]any, error) {
	return runbooks().list(ctx, c, "runbooks", q, limit, FeatureRunbooks)
}

// GetRunbook reads a runbook by UUID or name.
func GetRunbook(ctx context.Context, c *gophercloud.ServiceClient, ref string) (map[string]any, error) {
	return runbooks().get(ctx, c, ref, FeatureRunbooks)
}

// CreateRunbook creates a runbook; a description is FeatureRunbookDescription.
func CreateRunbook(ctx context.Context, c *gophercloud.ServiceClient, body map[string]any) (map[string]any, error) {
	return runbooks().create(ctx, c, body, runbookFeature(body["description"] != nil))
}

// UpdateRunbook applies a JSON patch to a runbook; one naming /description is
// FeatureRunbookDescription.
func UpdateRunbook(ctx context.Context, c *gophercloud.ServiceClient, ref string, ops Patch) (map[string]any, error) {
	return runbooks().update(ctx, c, ref, ops, runbookFeature(ops.touches("/description")))
}

// DeleteRunbook deletes a runbook.
func DeleteRunbook(ctx context.Context, c *gophercloud.ServiceClient, ref string) error {
	return runbooks().delete(ctx, c, ref, FeatureRunbooks)
}

// ListRunbookTraits lists a runbook's traits (FeatureRunbookTraits).
func ListRunbookTraits(ctx context.Context, c *gophercloud.ServiceClient, ref string) ([]string, error) {
	var body struct {
		Traits []string `json:"traits"`
	}
	err := ironicGet(ctx, c, joinPath("runbooks", ref, "traits"), &body)
	return body.Traits, wrap(ExplainMicroversion(ctx, c, FeatureRunbookTraits, err), "listing traits of baremetal runbook %s", ref)
}

// AddRunbookTrait adds one trait to a runbook.
func AddRunbookTrait(ctx context.Context, c *gophercloud.ServiceClient, ref, trait string) error {
	err := ironicPut(ctx, c, joinPath("runbooks", ref, "traits", trait), nil)
	return wrap(ExplainMicroversion(ctx, c, FeatureRunbookTraits, err), "adding trait %s to baremetal runbook %s", trait, ref)
}

// RemoveRunbookTrait removes one trait from a runbook.
func RemoveRunbookTrait(ctx context.Context, c *gophercloud.ServiceClient, ref, trait string) error {
	err := ironicDelete(ctx, c, joinPath("runbooks", ref, "traits", trait))
	return wrap(ExplainMicroversion(ctx, c, FeatureRunbookTraits, err), "removing trait %s from baremetal runbook %s", trait, ref)
}

// RemoveRunbookTraits removes every trait from a runbook.
func RemoveRunbookTraits(ctx context.Context, c *gophercloud.ServiceClient, ref string) error {
	err := ironicDelete(ctx, c, joinPath("runbooks", ref, "traits"))
	return wrap(ExplainMicroversion(ctx, c, FeatureRunbookTraits, err), "removing traits from baremetal runbook %s", ref)
}

// --- inspection rules --------------------------------------------------------

func inspectionRules() resource {
	return resource{"inspection_rules", "inspection_rules", "inspection rule"}
}

// ListInspectionRules lists ironic's inspection rules (FeatureInspectionRules)
// matching q (marker, sort_key, sort_dir, detail=true), up to limit (0: all).
func ListInspectionRules(ctx context.Context, c *gophercloud.ServiceClient, q url.Values, limit int) ([]map[string]any, error) {
	return inspectionRules().list(ctx, c, "inspection_rules", q, limit, FeatureInspectionRules)
}

// GetInspectionRule reads an inspection rule by UUID.
func GetInspectionRule(ctx context.Context, c *gophercloud.ServiceClient, id string) (map[string]any, error) {
	return inspectionRules().get(ctx, c, id, FeatureInspectionRules)
}

// CreateInspectionRule creates an inspection rule.
func CreateInspectionRule(ctx context.Context, c *gophercloud.ServiceClient, body map[string]any) (map[string]any, error) {
	return inspectionRules().create(ctx, c, body, FeatureInspectionRules)
}

// UpdateInspectionRule applies a JSON patch to an inspection rule.
func UpdateInspectionRule(ctx context.Context, c *gophercloud.ServiceClient, id string, ops Patch) (map[string]any, error) {
	return inspectionRules().update(ctx, c, id, ops, FeatureInspectionRules)
}

// DeleteInspectionRule deletes an inspection rule.
func DeleteInspectionRule(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return inspectionRules().delete(ctx, c, id, FeatureInspectionRules)
}

// --- volume connectors and targets -------------------------------------------

func volumeConnectors() resource {
	return resource{"volume/connectors", "connectors", "baremetal volume connector"}
}

func volumeTargets() resource {
	return resource{"volume/targets", "targets", "baremetal volume target"}
}

// ListVolumeConnectors lists volume connectors matching q (node, marker,
// sort_key, sort_dir, detail=true), up to limit (0: all).
func ListVolumeConnectors(ctx context.Context, c *gophercloud.ServiceClient, q url.Values, limit int) ([]map[string]any, error) {
	return volumeConnectors().list(ctx, c, "volume/connectors", q, limit, "")
}

// GetVolumeConnector reads a volume connector by UUID.
func GetVolumeConnector(ctx context.Context, c *gophercloud.ServiceClient, id string) (map[string]any, error) {
	return volumeConnectors().get(ctx, c, id, "")
}

// CreateVolumeConnector creates a volume connector.
func CreateVolumeConnector(ctx context.Context, c *gophercloud.ServiceClient, body map[string]any) (map[string]any, error) {
	return volumeConnectors().create(ctx, c, body, "")
}

// UpdateVolumeConnector applies a JSON patch to a volume connector.
func UpdateVolumeConnector(ctx context.Context, c *gophercloud.ServiceClient, id string, ops Patch) (map[string]any, error) {
	return volumeConnectors().update(ctx, c, id, ops, "")
}

// DeleteVolumeConnector deletes a volume connector.
func DeleteVolumeConnector(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return volumeConnectors().delete(ctx, c, id, "")
}

// ListVolumeTargets lists volume targets matching q (node, marker, sort_key,
// sort_dir, detail=true), up to limit (0: all).
func ListVolumeTargets(ctx context.Context, c *gophercloud.ServiceClient, q url.Values, limit int) ([]map[string]any, error) {
	return volumeTargets().list(ctx, c, "volume/targets", q, limit, "")
}

// GetVolumeTarget reads a volume target by UUID.
func GetVolumeTarget(ctx context.Context, c *gophercloud.ServiceClient, id string) (map[string]any, error) {
	return volumeTargets().get(ctx, c, id, "")
}

// CreateVolumeTarget creates a volume target.
func CreateVolumeTarget(ctx context.Context, c *gophercloud.ServiceClient, body map[string]any) (map[string]any, error) {
	return volumeTargets().create(ctx, c, body, "")
}

// UpdateVolumeTarget applies a JSON patch to a volume target.
func UpdateVolumeTarget(ctx context.Context, c *gophercloud.ServiceClient, id string, ops Patch) (map[string]any, error) {
	return volumeTargets().update(ctx, c, id, ops, "")
}

// DeleteVolumeTarget deletes a volume target.
func DeleteVolumeTarget(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return volumeTargets().delete(ctx, c, id, "")
}

// --- shards ------------------------------------------------------------------

// ListShards lists the node shards and their node counts (FeatureShards).
func ListShards(ctx context.Context, c *gophercloud.ServiceClient) ([]map[string]any, error) {
	var body struct {
		Shards []map[string]any `json:"shards"`
	}
	err := ironicGet(ctx, c, "shards", &body)
	return body.Shards, wrap(ExplainMicroversion(ctx, c, FeatureShards, err), "listing baremetal shards")
}
