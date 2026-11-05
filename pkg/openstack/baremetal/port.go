package baremetal

import (
	"context"
	"net/url"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/ports"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/paging"
)

// PortListOpts is gophercloud's ports.ListOpts plus ironic's portgroup
// filter, which it does not model.
type PortListOpts struct {
	ports.ListOpts
	PortGroup string
}

// ToPortListQuery implements ports.ListOptsBuilder.
func (o PortListOpts) ToPortListQuery() (string, error) {
	s, err := o.ListOpts.ToPortListQuery()
	return o.withPortGroup(s, err)
}

// ToPortListDetailQuery implements ports.ListOptsBuilder for /ports/detail.
func (o PortListOpts) ToPortListDetailQuery() (string, error) {
	s, err := o.ListOpts.ToPortListDetailQuery()
	return o.withPortGroup(s, err)
}

func (o PortListOpts) withPortGroup(s string, err error) (string, error) {
	if err != nil || o.PortGroup == "" {
		return s, err
	}
	sep := "?"
	if strings.Contains(s, "?") {
		sep = "&"
	}
	return s + sep + "portgroup=" + url.QueryEscape(o.PortGroup), nil
}

// ListPorts lists ports, up to limit (0: all); detail reads /ports/detail.
func ListPorts(ctx context.Context, c *gophercloud.ServiceClient, opts ports.ListOptsBuilder,
	detail bool, limit int,
) ([]map[string]any, error) {
	list := ports.List
	if detail {
		list = ports.ListDetail
	}
	all, err := paging.Collect(ctx, list(c, opts), limit, pageMaps("ports"))
	return all, wrap(err, "listing baremetal ports")
}

// GetPort reads a port by UUID or name.
func GetPort(ctx context.Context, c *gophercloud.ServiceClient, id string) (map[string]any, error) {
	p, err := resultMap(ports.Get(ctx, c, id).Result)
	return p, wrap(err, "getting baremetal port %s", id)
}

// PortCreateOpts is gophercloud's ports.CreateOpts plus the port name
// (FeaturePortName), which it does not model.
type PortCreateOpts struct {
	ports.CreateOpts
	Name string
}

// ToPortCreateMap implements ports.CreateOptsBuilder.
func (o PortCreateOpts) ToPortCreateMap() (map[string]any, error) {
	b, err := o.CreateOpts.ToPortCreateMap()
	if err != nil {
		return nil, err
	}
	if o.Name != "" {
		b["name"] = o.Name
	}
	return b, nil
}

// CreatePort creates a port. A body with a name is gated by FeaturePortName.
func CreatePort(ctx context.Context, c *gophercloud.ServiceClient, opts ports.CreateOptsBuilder) (map[string]any, error) {
	p, err := resultMap(ports.Create(ctx, c, opts).Result)
	if err != nil {
		if b, berr := opts.ToPortCreateMap(); berr == nil && b["name"] != nil {
			err = ExplainMicroversion(ctx, c, FeaturePortName, err)
		}
	}
	return p, wrap(err, "creating baremetal port")
}

// UpdatePort applies a JSON patch to a port. Setting /name is gated by
// FeaturePortName.
func UpdatePort(ctx context.Context, c *gophercloud.ServiceClient, id string, ops ports.UpdateOpts) (map[string]any, error) {
	p, err := resultMap(ports.Update(ctx, c, id, ops).Result)
	if err != nil {
		for _, op := range ops {
			if u, ok := op.(ports.UpdateOperation); ok && u.Path == "/name" && u.Op != ports.RemoveOp {
				err = ExplainMicroversion(ctx, c, FeaturePortName, err)
				break
			}
		}
	}
	return p, wrap(err, "updating baremetal port %s", id)
}

// DeletePort deletes a port.
func DeletePort(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(ports.Delete(ctx, c, id).ExtractErr(), "deleting baremetal port %s", id)
}
