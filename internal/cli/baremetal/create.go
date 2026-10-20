package baremetal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
)

// "baremetal create" enrolls whole inventories from files, as
// python-ironicclient's v1/create_resources.py does: each file is a JSON or
// YAML object with "chassis" and/or "nodes" arrays; a chassis may nest
// "nodes", a node "ports" and "portgroups", a port group "ports". A nested
// resource is created after its parent, with the parent's UUID filled in. Every
// file is validated before anything is created; creation then carries on past
// a failed resource and reports every error at the end, as upstream does.
//
// UNVERIFIED against KeyStack (docs.keystack.ru unreachable, HTTP 403).

func newCreateCommand(a *auth.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "create <file> [<file> ...]",
		Short: "Create resources (chassis, nodes, port groups, ports) from .json or .yaml files",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			docs, err := loadResourceFiles(args)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runCreateResources(ctx, client, docs, cmd.OutOrStdout())
		},
	}
}

// resourceDoc is one validated file: its chassis and nodes.
type resourceDoc struct {
	chassis, nodes []map[string]any
}

// loadResourceFiles reads and validates every file, collecting every problem
// before returning, so one run reports them all.
func loadResourceFiles(paths []string) ([]resourceDoc, error) {
	var docs []resourceDoc
	var errs []string
	for _, p := range paths {
		doc, err := loadResourceFile(p)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		docs = append(docs, doc)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("while validating the resources file(s), the following error(s) were encountered:\n%s",
			strings.Join(errs, "\n"))
	}
	return docs, nil
}

func loadResourceFile(path string) (resourceDoc, error) {
	var top map[string]any
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the operator names the resource files
	if err != nil {
		return resourceDoc{}, fmt.Errorf("cannot read file %q: %w", path, err)
	}
	switch {
	case strings.HasSuffix(path, ".json"):
		err = json.Unmarshal(raw, &top)
	case strings.HasSuffix(path, ".yaml"):
		err = yaml.Unmarshal(raw, &top)
	default:
		return resourceDoc{}, fmt.Errorf("cannot process file %q: it must have a .json or .yaml extension", path)
	}
	if err != nil {
		return resourceDoc{}, fmt.Errorf("file %q is invalid: %w", path, err)
	}
	var doc resourceDoc
	for key, v := range top {
		var list *[]map[string]any
		switch key {
		case "chassis":
			list = &doc.chassis
		case "nodes":
			list = &doc.nodes
		default:
			return resourceDoc{}, fmt.Errorf("file %q: unexpected key %q (only chassis and nodes are allowed)", path, key)
		}
		if *list, err = objectList(v); err != nil {
			return resourceDoc{}, fmt.Errorf("file %q: %s: %w", path, key, err)
		}
	}
	return doc, nil
}

// objectList checks that v is an array of objects, the only shape the schema
// upstream validates against allows for each key.
func objectList(v any) ([]map[string]any, error) {
	items, ok := v.([]any)
	if !ok {
		return nil, errors.New("must be an array")
	}
	out := make([]map[string]any, 0, len(items))
	for i, it := range items {
		obj, ok := it.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("item %d must be an object", i+1)
		}
		out = append(out, obj)
	}
	return out, nil
}

// resourceCreator walks the documents, creating each resource and collecting
// the errors.
type resourceCreator struct {
	ctx    context.Context
	client *gophercloud.ServiceClient
	w      io.Writer
	errs   []string
}

func runCreateResources(ctx context.Context, client *gophercloud.ServiceClient, docs []resourceDoc, w io.Writer) error {
	c := &resourceCreator{ctx: ctx, client: client, w: w}
	for _, d := range docs {
		for _, ch := range d.chassis {
			c.chassis(ch)
		}
		for _, n := range d.nodes {
			c.node(n, "")
		}
	}
	if len(c.errs) > 0 {
		return fmt.Errorf("during resources creation, the following error(s) were encountered:\n%s",
			strings.Join(c.errs, "\n"))
	}
	return nil
}

// create POSTs body minus the nested keys and returns the new UUID, or ""
// after recording the error.
func (c *resourceCreator) create(kind, path string, body map[string]any, nested ...string) string {
	params := maps.Clone(body)
	for _, k := range nested {
		delete(params, k)
	}
	var out struct {
		UUID string `json:"uuid"`
	}
	if err := ironicPost(c.ctx, c.client, path, params, &out); err != nil {
		c.errs = append(c.errs, fmt.Sprintf("unable to create the %s with the specified attributes: %v. The error is: %v",
			kind, compactJSON(params), err))
		return ""
	}
	if _, err := fmt.Fprintf(c.w, "Created %s %s\n", kind, out.UUID); err != nil {
		c.errs = append(c.errs, err.Error())
	}
	return out.UUID
}

// claim sets body[key] to parent, refusing a resource that already names a
// different parent.
func (c *resourceCreator) claim(kind string, body map[string]any, key, parentKind, parent string) bool {
	if have, ok := body[key].(string); ok && have != "" && have != parent {
		c.errs = append(c.errs, fmt.Sprintf("cannot create a %s as part of %s %s because the %s %v has a different %s specified",
			kind, parentKind, parent, kind, compactJSON(body), key))
		return false
	}
	body[key] = parent
	return true
}

func (c *resourceCreator) chassis(body map[string]any) {
	uuid := c.create("chassis", "chassis", body, "nodes")
	if uuid == "" {
		return
	}
	for _, n := range c.nested(body, "nodes", "chassis") {
		c.node(n, uuid)
	}
}

func (c *resourceCreator) node(body map[string]any, chassisUUID string) {
	if chassisUUID != "" && !c.claim("node", body, "chassis_uuid", "chassis", chassisUUID) {
		return
	}
	uuid := c.create("node", "nodes", body, "ports", "portgroups", "traits")
	if uuid == "" {
		return
	}
	if traits, ok := body["traits"]; ok && traits != nil {
		if err := ironicPut(c.ctx, c.client, joinPath("nodes", uuid, "traits"), map[string]any{"traits": traits}); err != nil {
			c.errs = append(c.errs, fmt.Sprintf("unable to set the traits of node %s: %v", uuid, err))
		}
	}
	for _, pg := range c.nested(body, "portgroups", "node") {
		c.portGroup(pg, uuid)
	}
	for _, p := range c.nested(body, "ports", "node") {
		c.port(p, uuid, "")
	}
}

func (c *resourceCreator) portGroup(body map[string]any, nodeUUID string) {
	if !c.claim("port group", body, "node_uuid", "node", nodeUUID) {
		return
	}
	uuid := c.create("port group", "portgroups", body, "ports")
	if uuid == "" {
		return
	}
	for _, p := range c.nested(body, "ports", "port group") {
		c.port(p, nodeUUID, uuid)
	}
}

func (c *resourceCreator) port(body map[string]any, nodeUUID, portGroupUUID string) {
	if !c.claim("port", body, "node_uuid", "node", nodeUUID) {
		return
	}
	if portGroupUUID != "" && !c.claim("port", body, "portgroup_uuid", "port group", portGroupUUID) {
		return
	}
	c.create("port", "ports", body)
}

// nested reads a parent's child list, recording a malformed one as an error.
func (c *resourceCreator) nested(body map[string]any, key, parentKind string) []map[string]any {
	v, ok := body[key]
	if !ok || v == nil {
		return nil
	}
	list, err := objectList(v)
	if err != nil {
		c.errs = append(c.errs, fmt.Sprintf("%s %v: %s: %v", parentKind, compactJSON(body), key, err))
		return nil
	}
	return list
}

func compactJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
