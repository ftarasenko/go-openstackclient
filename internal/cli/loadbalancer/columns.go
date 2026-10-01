package loadbalancer

import (
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/flavorprofiles"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/l7policies"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/listeners"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/loadbalancers"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/monitors"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/pools"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/quotas"
)

// The columns each write verb renders, taken from its field builder so -c is
// checked before the write against exactly what the result will carry.
var (
	lbColumns            = fieldNames(lbFields(&loadbalancers.LoadBalancer{}))
	listenerColumns      = fieldNames(listenerFields(&listeners.Listener{}))
	poolColumns          = fieldNames(poolFields(&pools.Pool{}))
	memberColumns        = fieldNames(memberFields(&pools.Member{}))
	healthMonitorColumns = fieldNames(healthMonitorFields(&monitors.Monitor{}))
	l7PolicyColumns      = fieldNames(l7PolicyFields(&l7policies.L7Policy{}))
	l7RuleColumns        = fieldNames(l7RuleFields(&l7policies.Rule{}))
	lbQuotaColumns       = fieldNames(lbQuotaFields(&quotas.Quota{}))
	flavorColumns        = fieldNames(flavorFields(&flavors.Flavor{}))
	flavorProfileColumns = fieldNames(flavorProfileFields(&flavorprofiles.FlavorProfile{}))
)

// fieldNames returns the headers of a field builder's result.
func fieldNames(fields []string, _ []any) []string { return fields }

func flavorFields(fl *flavors.Flavor) ([]string, []any) {
	return []string{"id", "name", "description", "enabled", "flavor_profile_id"},
		[]any{fl.ID, fl.Name, fl.Description, fl.Enabled, fl.FlavorProfileId}
}

func flavorProfileFields(p *flavorprofiles.FlavorProfile) ([]string, []any) {
	return []string{"id", "name", "provider_name", "flavor_data"},
		[]any{p.ID, p.Name, p.ProviderName, p.FlavorData}
}
