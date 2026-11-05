package volume

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/services"
)

// Microversions that add fields to a service listing.
const (
	// ServiceClusterMicroversion adds the cluster name.
	ServiceClusterMicroversion = "3.7"
	// ServiceBackendStateMicroversion adds BackendState.
	ServiceBackendStateMicroversion = "3.49"
)

// Service is a cinder service with the field gophercloud's type drops.
type Service struct {
	services.Service
	// BackendState is the driver's own up/down view of the storage behind
	// a cinder-volume service (ServiceBackendStateMicroversion).
	BackendState string
}

// ListServices lists the cinder services opts selects.
func ListServices(ctx context.Context, c *gophercloud.ServiceClient, opts services.ListOptsBuilder) ([]Service, error) {
	pages, err := services.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing volume services")
	}
	all, err := services.ExtractServices(pages)
	if err != nil {
		return nil, wrap(err, "listing volume services")
	}
	// The same "services" array, read again for backend_state.
	var ext struct {
		Services []struct {
			BackendState string `json:"backend_state"`
		} `json:"services"`
	}
	sp, ok := pages.(services.ServicePage)
	if !ok {
		return nil, fmt.Errorf("listing volume services: unexpected page type %T", pages)
	}
	if err := sp.ExtractInto(&ext); err != nil {
		return nil, wrap(err, "listing volume services")
	}
	out := make([]Service, len(all))
	for i, s := range all {
		out[i].Service = s
		if i < len(ext.Services) {
			out[i].BackendState = ext.Services[i].BackendState
		}
	}
	return out, nil
}

// ServiceUpdateOpts enables or disables the service binary runs on host.
// A DisabledReason implies Disabled.
type ServiceUpdateOpts struct {
	Host           string
	Binary         string
	Disabled       bool
	DisabledReason string
}

// UpdateService enables or disables a cinder service (admin). gophercloud's
// services package has no update call, so this PUTs to os-services.
func UpdateService(ctx context.Context, c *gophercloud.ServiceClient, opts ServiceUpdateOpts) error {
	body := map[string]any{"host": opts.Host, "binary": opts.Binary}
	action := "enable"
	if opts.Disabled || opts.DisabledReason != "" {
		action = "disable"
		if opts.DisabledReason != "" {
			action = "disable-log-reason"
			body["disabled_reason"] = opts.DisabledReason
		}
	}
	return wrap(putJSON(ctx, c, c.ServiceURL("os-services", action), body, nil),
		"setting block storage service %s/%s", opts.Host, opts.Binary)
}
