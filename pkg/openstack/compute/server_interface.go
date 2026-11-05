package compute

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/attachinterfaces"
)

// InterfaceTagMicroversion is the microversion that added tag to an
// interface attachment. Nova's schema forbids unknown fields, so a tag sent
// below it is a 400.
const InterfaceTagMicroversion = "2.49"

// InterfaceAttachOpts adds tag to an interface attachment. gophercloud's
// attachinterfaces.CreateOpts has no field for it.
type InterfaceAttachOpts struct {
	attachinterfaces.CreateOpts
	// Tag needs [InterfaceTagMicroversion].
	Tag string
}

// ToAttachInterfacesCreateMap implements attachinterfaces.CreateOptsBuilder.
func (o InterfaceAttachOpts) ToAttachInterfacesCreateMap() (map[string]any, error) {
	body, err := o.CreateOpts.ToAttachInterfacesCreateMap()
	if err != nil {
		return nil, err
	}
	if o.Tag == "" {
		return body, nil
	}
	inner, ok := body["interfaceAttachment"].(map[string]any)
	if !ok {
		inner = map[string]any{}
	}
	inner["tag"] = o.Tag
	body["interfaceAttachment"] = inner
	return body, nil
}

// ListInterfaces lists a server's attached interfaces.
func ListInterfaces(ctx context.Context, c *gophercloud.ServiceClient, serverID string) ([]attachinterfaces.Interface, error) {
	pages, err := attachinterfaces.List(c, serverID).AllPages(ctx)
	if err == nil {
		var all []attachinterfaces.Interface
		if all, err = attachinterfaces.ExtractInterfaces(pages); err == nil {
			return all, nil
		}
	}
	return nil, fmt.Errorf("listing interfaces of server %s: %w", serverID, err)
}

// AttachInterface attaches a port, or a new port on a network, to a server
// (attachinterfaces.CreateOpts or [InterfaceAttachOpts]).
func AttachInterface(ctx context.Context, c *gophercloud.ServiceClient, serverID string, opts attachinterfaces.CreateOptsBuilder) (*attachinterfaces.Interface, error) {
	iface, err := attachinterfaces.Create(ctx, c, serverID, opts).Extract()
	if err != nil {
		return nil, fmt.Errorf("attaching an interface to server %s: %w", serverID, err)
	}
	return iface, nil
}

// DetachInterface detaches the port with ID portID from a server.
func DetachInterface(ctx context.Context, c *gophercloud.ServiceClient, serverID, portID string) error {
	return wrap(attachinterfaces.Delete(ctx, c, serverID, portID).ExtractErr(),
		"detaching port %s from server %s", portID, serverID)
}
