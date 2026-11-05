package compute

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/services"
	"github.com/gophercloud/gophercloud/v2/pagination"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

// Compute services (nova-compute, nova-conductor, …) are keyed by ID — a UUID
// from microversion 2.53 — and found by host and binary.

// ServiceAdminState holds the fields of KeyStack's os-services extension,
// which gophercloud's services.Service does not model. Vanilla nova does not
// send them, so both stay empty there.
type ServiceAdminState struct {
	AdminState   string `json:"admin_state"`
	ErrorDetails string `json:"error_details"`
}

// ListServices lists the compute services opts selects. The second slice
// carries each service's KeyStack extension fields and is aligned with the
// first by index.
func ListServices(ctx context.Context, c *gophercloud.ServiceClient, opts services.ListOptsBuilder) ([]services.Service, []ServiceAdminState, error) {
	pages, err := services.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("listing compute services: %w", err)
	}
	all, err := services.ExtractServices(pages)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing compute service list: %w", err)
	}
	ext, err := ExtractServiceAdminState(pages)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing compute service list: %w", err)
	}
	return all, ext, nil
}

// ExtractServiceAdminState is services.ExtractServices for the KeyStack
// extension fields of a page of services.List, in the same order.
func ExtractServiceAdminState(page pagination.Page) ([]ServiceAdminState, error) {
	var s struct {
		Services []ServiceAdminState `json:"services"`
	}
	sp, ok := page.(services.ServicePage)
	if !ok {
		return nil, fmt.Errorf("extracting service admin state: unexpected page type %T", page)
	}
	err := sp.ExtractInto(&s)
	return s.Services, err
}

// FindServiceID returns the ID of the compute service binary runs on host.
// No such service is a *resolve.NotFoundError whose Ref is "host/binary".
func FindServiceID(ctx context.Context, c *gophercloud.ServiceClient, host, binary string) (string, error) {
	pages, err := services.List(c, services.ListOpts{Host: host, Binary: binary}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving compute service %s/%s: %w", host, binary, err)
	}
	all, err := services.ExtractServices(pages)
	if err != nil {
		return "", fmt.Errorf("resolving compute service %s/%s: %w", host, binary, err)
	}
	for _, s := range all {
		if s.Host == host && s.Binary == binary {
			return s.ID, nil
		}
	}
	return "", &resolve.NotFoundError{Kind: "compute service", Ref: host + "/" + binary}
}

// ServiceUpdateOpts is a compute service update. It replaces
// services.UpdateOpts, which tags ForcedDown omitempty and so cannot send
// forced_down=false (bringing a forced-down service back up), and has no
// field for KeyStack's admin state. A zero field is left out of the request.
type ServiceUpdateOpts struct {
	// Status is services.ServiceEnabled or services.ServiceDisabled.
	Status services.ServiceStatus
	// DisabledReason is recorded with a disable.
	DisabledReason string
	// ForcedDown, when set, marks the service down (true) or lifts the mark
	// (false); nova evacuates only from a host whose service is down.
	ForcedDown *bool
	// AdminState and ErrorDetails are KeyStack's os-services extension;
	// vanilla nova rejects a body carrying them with 400.
	AdminState   string
	ErrorDetails string
}

// ToServiceUpdateMap implements services.UpdateOptsBuilder.
func (o ServiceUpdateOpts) ToServiceUpdateMap() (map[string]any, error) {
	body := map[string]any{}
	if o.AdminState != "" {
		body["admin_state"] = o.AdminState
	}
	if o.ErrorDetails != "" {
		body["error_details"] = o.ErrorDetails
	}
	if o.Status != "" {
		body["status"] = string(o.Status)
	}
	if o.DisabledReason != "" {
		body["disabled_reason"] = o.DisabledReason
	}
	if o.ForcedDown != nil {
		body["forced_down"] = *o.ForcedDown
	}
	return body, nil
}

// UpdateService enables, disables, forces down or lifts a forced-down mark
// from a compute service, or sets its KeyStack admin state.
func UpdateService(ctx context.Context, c *gophercloud.ServiceClient, id string, opts services.UpdateOptsBuilder) (*services.Service, error) {
	s, err := services.Update(ctx, c, id, opts).Extract()
	if err != nil {
		return nil, fmt.Errorf("updating compute service %s: %w", id, err)
	}
	return s, nil
}

// DeleteService deletes a compute service record.
func DeleteService(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	if err := services.Delete(ctx, c, id).ExtractErr(); err != nil {
		return fmt.Errorf("deleting compute service %s: %w", id, err)
	}
	return nil
}
