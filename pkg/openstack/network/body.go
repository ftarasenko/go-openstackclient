package network

import (
	"fmt"
	"reflect"

	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/addressscopes"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/floatingips"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/portforwarding"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/routers"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/qos/policies"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/rbacpolicies"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/addressgroups"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/groups"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/rules"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/segments"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/subnetpools"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/trunks"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/subnets"
)

// The With…Attrs functions splice extension attributes into a request body.
// gophercloud's typed opts model only part of what neutron accepts
// (qos_policy_id, dns_domain, port_security_enabled, …), so each wraps a
// gophercloud builder and adds attrs to the top level of its body, where
// they win over the typed fields. A nil or empty attrs leaves the body as
// opts builds it. The update wrappers keep opts' RevisionNumber, which
// gophercloud sends as If-Match.

type networkCreateExt struct{ bodyExt }

func (e networkCreateExt) ToNetworkCreateMap() (map[string]any, error) { return e.body() }

// WithNetworkCreateAttrs adds attrs to the top level of opts' "network" body.
func WithNetworkCreateAttrs(opts networks.CreateOptsBuilder, attrs map[string]any) networks.CreateOptsBuilder {
	return networkCreateExt{bodyExt{build: opts.ToNetworkCreateMap, key: "network", extra: attrs}}
}

type networkUpdateExt struct {
	bodyExt
	RevisionNumber *int `json:"-" h:"If-Match"`
}

func (e networkUpdateExt) ToNetworkUpdateMap() (map[string]any, error) { return e.body() }

// WithNetworkUpdateAttrs adds attrs to the top level of opts' "network" body,
// keeping its If-Match revision guard.
func WithNetworkUpdateAttrs(opts networks.UpdateOptsBuilder, attrs map[string]any) networks.UpdateOptsBuilder {
	return networkUpdateExt{bodyExt: bodyExt{build: opts.ToNetworkUpdateMap, key: "network", extra: attrs}, RevisionNumber: revisionOf(opts)}
}

type subnetCreateExt struct{ bodyExt }

func (e subnetCreateExt) ToSubnetCreateMap() (map[string]any, error) { return e.body() }

// WithSubnetCreateAttrs adds attrs to the top level of opts' "subnet" body.
func WithSubnetCreateAttrs(opts subnets.CreateOptsBuilder, attrs map[string]any) subnets.CreateOptsBuilder {
	return subnetCreateExt{bodyExt{build: opts.ToSubnetCreateMap, key: "subnet", extra: attrs}}
}

type subnetUpdateExt struct {
	bodyExt
	RevisionNumber *int `json:"-" h:"If-Match"`
}

func (e subnetUpdateExt) ToSubnetUpdateMap() (map[string]any, error) { return e.body() }

// WithSubnetUpdateAttrs adds attrs to the top level of opts' "subnet" body,
// keeping its If-Match revision guard.
func WithSubnetUpdateAttrs(opts subnets.UpdateOptsBuilder, attrs map[string]any) subnets.UpdateOptsBuilder {
	return subnetUpdateExt{bodyExt: bodyExt{build: opts.ToSubnetUpdateMap, key: "subnet", extra: attrs}, RevisionNumber: revisionOf(opts)}
}

type portCreateExt struct{ bodyExt }

func (e portCreateExt) ToPortCreateMap() (map[string]any, error) { return e.body() }

// WithPortCreateAttrs adds attrs to the top level of opts' "port" body.
func WithPortCreateAttrs(opts ports.CreateOptsBuilder, attrs map[string]any) ports.CreateOptsBuilder {
	return portCreateExt{bodyExt{build: opts.ToPortCreateMap, key: "port", extra: attrs}}
}

type portUpdateExt struct {
	bodyExt
	RevisionNumber *int `json:"-" h:"If-Match"`
}

func (e portUpdateExt) ToPortUpdateMap() (map[string]any, error) { return e.body() }

// WithPortUpdateAttrs adds attrs to the top level of opts' "port" body,
// keeping its If-Match revision guard.
func WithPortUpdateAttrs(opts ports.UpdateOptsBuilder, attrs map[string]any) ports.UpdateOptsBuilder {
	return portUpdateExt{bodyExt: bodyExt{build: opts.ToPortUpdateMap, key: "port", extra: attrs}, RevisionNumber: revisionOf(opts)}
}

type routerCreateExt struct{ bodyExt }

func (e routerCreateExt) ToRouterCreateMap() (map[string]any, error) { return e.body() }

// WithRouterCreateAttrs adds attrs to the top level of opts' "router" body.
func WithRouterCreateAttrs(opts routers.CreateOptsBuilder, attrs map[string]any) routers.CreateOptsBuilder {
	return routerCreateExt{bodyExt{build: opts.ToRouterCreateMap, key: "router", extra: attrs}}
}

type routerUpdateExt struct {
	bodyExt
	RevisionNumber *int `json:"-" h:"If-Match"`
}

func (e routerUpdateExt) ToRouterUpdateMap() (map[string]any, error) { return e.body() }

// WithRouterUpdateAttrs adds attrs to the top level of opts' "router" body,
// keeping its If-Match revision guard.
func WithRouterUpdateAttrs(opts routers.UpdateOptsBuilder, attrs map[string]any) routers.UpdateOptsBuilder {
	return routerUpdateExt{bodyExt: bodyExt{build: opts.ToRouterUpdateMap, key: "router", extra: attrs}, RevisionNumber: revisionOf(opts)}
}

type floatingIPCreateExt struct{ bodyExt }

func (e floatingIPCreateExt) ToFloatingIPCreateMap() (map[string]any, error) { return e.body() }

// WithFloatingIPCreateAttrs adds attrs to the top level of opts' "floatingip" body.
func WithFloatingIPCreateAttrs(opts floatingips.CreateOptsBuilder, attrs map[string]any) floatingips.CreateOptsBuilder {
	return floatingIPCreateExt{bodyExt{build: opts.ToFloatingIPCreateMap, key: "floatingip", extra: attrs}}
}

type floatingIPUpdateExt struct {
	bodyExt
	RevisionNumber *int `json:"-" h:"If-Match"`
}

func (e floatingIPUpdateExt) ToFloatingIPUpdateMap() (map[string]any, error) { return e.body() }

// WithFloatingIPUpdateAttrs adds attrs to the top level of opts' "floatingip" body,
// keeping its If-Match revision guard.
func WithFloatingIPUpdateAttrs(opts floatingips.UpdateOptsBuilder, attrs map[string]any) floatingips.UpdateOptsBuilder {
	return floatingIPUpdateExt{bodyExt: bodyExt{build: opts.ToFloatingIPUpdateMap, key: "floatingip", extra: attrs}, RevisionNumber: revisionOf(opts)}
}

type secGroupCreateExt struct{ bodyExt }

func (e secGroupCreateExt) ToSecGroupCreateMap() (map[string]any, error) { return e.body() }

// WithSecurityGroupCreateAttrs adds attrs to the top level of opts' "security_group" body.
func WithSecurityGroupCreateAttrs(opts groups.CreateOptsBuilder, attrs map[string]any) groups.CreateOptsBuilder {
	return secGroupCreateExt{bodyExt{build: opts.ToSecGroupCreateMap, key: "security_group", extra: attrs}}
}

type secGroupUpdateExt struct {
	bodyExt
	RevisionNumber *int `json:"-" h:"If-Match"`
}

func (e secGroupUpdateExt) ToSecGroupUpdateMap() (map[string]any, error) { return e.body() }

// WithSecurityGroupUpdateAttrs adds attrs to the top level of opts' "security_group" body,
// keeping its If-Match revision guard.
func WithSecurityGroupUpdateAttrs(opts groups.UpdateOptsBuilder, attrs map[string]any) groups.UpdateOptsBuilder {
	return secGroupUpdateExt{bodyExt: bodyExt{build: opts.ToSecGroupUpdateMap, key: "security_group", extra: attrs}, RevisionNumber: revisionOf(opts)}
}

type secGroupRuleCreateExt struct{ bodyExt }

func (e secGroupRuleCreateExt) ToSecGroupRuleCreateMap() (map[string]any, error) { return e.body() }

// WithSecurityGroupRuleCreateAttrs adds attrs to the top level of opts' "security_group_rule" body.
func WithSecurityGroupRuleCreateAttrs(opts rules.CreateOptsBuilder, attrs map[string]any) rules.CreateOptsBuilder {
	return secGroupRuleCreateExt{bodyExt{build: opts.ToSecGroupRuleCreateMap, key: "security_group_rule", extra: attrs}}
}

type subnetPoolCreateExt struct{ bodyExt }

func (e subnetPoolCreateExt) ToSubnetPoolCreateMap() (map[string]any, error) { return e.body() }

// WithSubnetPoolCreateAttrs adds attrs to the top level of opts' "subnetpool" body.
func WithSubnetPoolCreateAttrs(opts subnetpools.CreateOptsBuilder, attrs map[string]any) subnetpools.CreateOptsBuilder {
	return subnetPoolCreateExt{bodyExt{build: opts.ToSubnetPoolCreateMap, key: "subnetpool", extra: attrs}}
}

type subnetPoolUpdateExt struct {
	bodyExt
	RevisionNumber *int `json:"-" h:"If-Match"`
}

func (e subnetPoolUpdateExt) ToSubnetPoolUpdateMap() (map[string]any, error) { return e.body() }

// WithSubnetPoolUpdateAttrs adds attrs to the top level of opts' "subnetpool" body,
// keeping its If-Match revision guard.
func WithSubnetPoolUpdateAttrs(opts subnetpools.UpdateOptsBuilder, attrs map[string]any) subnetpools.UpdateOptsBuilder {
	return subnetPoolUpdateExt{bodyExt: bodyExt{build: opts.ToSubnetPoolUpdateMap, key: "subnetpool", extra: attrs}, RevisionNumber: revisionOf(opts)}
}

type addressScopeCreateExt struct{ bodyExt }

func (e addressScopeCreateExt) ToAddressScopeCreateMap() (map[string]any, error) { return e.body() }

// WithAddressScopeCreateAttrs adds attrs to the top level of opts' "address_scope" body.
func WithAddressScopeCreateAttrs(opts addressscopes.CreateOptsBuilder, attrs map[string]any) addressscopes.CreateOptsBuilder {
	return addressScopeCreateExt{bodyExt{build: opts.ToAddressScopeCreateMap, key: "address_scope", extra: attrs}}
}

type addressScopeUpdateExt struct {
	bodyExt
	RevisionNumber *int `json:"-" h:"If-Match"`
}

func (e addressScopeUpdateExt) ToAddressScopeUpdateMap() (map[string]any, error) { return e.body() }

// WithAddressScopeUpdateAttrs adds attrs to the top level of opts' "address_scope" body,
// keeping its If-Match revision guard.
func WithAddressScopeUpdateAttrs(opts addressscopes.UpdateOptsBuilder, attrs map[string]any) addressscopes.UpdateOptsBuilder {
	return addressScopeUpdateExt{bodyExt: bodyExt{build: opts.ToAddressScopeUpdateMap, key: "address_scope", extra: attrs}, RevisionNumber: revisionOf(opts)}
}

type addressGroupCreateExt struct{ bodyExt }

func (e addressGroupCreateExt) ToAddressGroupCreateMap() (map[string]any, error) { return e.body() }

// WithAddressGroupCreateAttrs adds attrs to the top level of opts' "address_group" body.
func WithAddressGroupCreateAttrs(opts addressgroups.CreateOptsBuilder, attrs map[string]any) addressgroups.CreateOptsBuilder {
	return addressGroupCreateExt{bodyExt{build: opts.ToAddressGroupCreateMap, key: "address_group", extra: attrs}}
}

type addressGroupUpdateExt struct {
	bodyExt
	RevisionNumber *int `json:"-" h:"If-Match"`
}

func (e addressGroupUpdateExt) ToAddressGroupUpdateMap() (map[string]any, error) { return e.body() }

// WithAddressGroupUpdateAttrs adds attrs to the top level of opts' "address_group" body,
// keeping its If-Match revision guard.
func WithAddressGroupUpdateAttrs(opts addressgroups.UpdateOptsBuilder, attrs map[string]any) addressgroups.UpdateOptsBuilder {
	return addressGroupUpdateExt{bodyExt: bodyExt{build: opts.ToAddressGroupUpdateMap, key: "address_group", extra: attrs}, RevisionNumber: revisionOf(opts)}
}

type qosPolicyCreateExt struct{ bodyExt }

func (e qosPolicyCreateExt) ToPolicyCreateMap() (map[string]any, error) { return e.body() }

// WithQoSPolicyCreateAttrs adds attrs to the top level of opts' "policy" body.
func WithQoSPolicyCreateAttrs(opts policies.CreateOptsBuilder, attrs map[string]any) policies.CreateOptsBuilder {
	return qosPolicyCreateExt{bodyExt{build: opts.ToPolicyCreateMap, key: "policy", extra: attrs}}
}

type qosPolicyUpdateExt struct {
	bodyExt
	RevisionNumber *int `json:"-" h:"If-Match"`
}

func (e qosPolicyUpdateExt) ToPolicyUpdateMap() (map[string]any, error) { return e.body() }

// WithQoSPolicyUpdateAttrs adds attrs to the top level of opts' "policy" body,
// keeping its If-Match revision guard.
func WithQoSPolicyUpdateAttrs(opts policies.UpdateOptsBuilder, attrs map[string]any) policies.UpdateOptsBuilder {
	return qosPolicyUpdateExt{bodyExt: bodyExt{build: opts.ToPolicyUpdateMap, key: "policy", extra: attrs}, RevisionNumber: revisionOf(opts)}
}

type rbacCreateExt struct{ bodyExt }

func (e rbacCreateExt) ToRBACPolicyCreateMap() (map[string]any, error) { return e.body() }

// WithRBACPolicyCreateAttrs adds attrs to the top level of opts' "rbac_policy" body.
func WithRBACPolicyCreateAttrs(opts rbacpolicies.CreateOptsBuilder, attrs map[string]any) rbacpolicies.CreateOptsBuilder {
	return rbacCreateExt{bodyExt{build: opts.ToRBACPolicyCreateMap, key: "rbac_policy", extra: attrs}}
}

type rbacUpdateExt struct {
	bodyExt
	RevisionNumber *int `json:"-" h:"If-Match"`
}

func (e rbacUpdateExt) ToRBACPolicyUpdateMap() (map[string]any, error) { return e.body() }

// WithRBACPolicyUpdateAttrs adds attrs to the top level of opts' "rbac_policy" body,
// keeping its If-Match revision guard.
func WithRBACPolicyUpdateAttrs(opts rbacpolicies.UpdateOptsBuilder, attrs map[string]any) rbacpolicies.UpdateOptsBuilder {
	return rbacUpdateExt{bodyExt: bodyExt{build: opts.ToRBACPolicyUpdateMap, key: "rbac_policy", extra: attrs}, RevisionNumber: revisionOf(opts)}
}

type segmentCreateExt struct{ bodyExt }

func (e segmentCreateExt) ToSegmentCreateMap() (map[string]any, error) { return e.body() }

// WithSegmentCreateAttrs adds attrs to the top level of opts' "segment" body.
func WithSegmentCreateAttrs(opts segments.CreateOptsBuilder, attrs map[string]any) segments.CreateOptsBuilder {
	return segmentCreateExt{bodyExt{build: opts.ToSegmentCreateMap, key: "segment", extra: attrs}}
}

type segmentUpdateExt struct {
	bodyExt
	RevisionNumber *int `json:"-" h:"If-Match"`
}

func (e segmentUpdateExt) ToSegmentUpdateMap() (map[string]any, error) { return e.body() }

// WithSegmentUpdateAttrs adds attrs to the top level of opts' "segment" body,
// keeping its If-Match revision guard.
func WithSegmentUpdateAttrs(opts segments.UpdateOptsBuilder, attrs map[string]any) segments.UpdateOptsBuilder {
	return segmentUpdateExt{bodyExt: bodyExt{build: opts.ToSegmentUpdateMap, key: "segment", extra: attrs}, RevisionNumber: revisionOf(opts)}
}

type trunkCreateExt struct{ bodyExt }

func (e trunkCreateExt) ToTrunkCreateMap() (map[string]any, error) { return e.body() }

// WithTrunkCreateAttrs adds attrs to the top level of opts' "trunk" body.
func WithTrunkCreateAttrs(opts trunks.CreateOptsBuilder, attrs map[string]any) trunks.CreateOptsBuilder {
	return trunkCreateExt{bodyExt{build: opts.ToTrunkCreateMap, key: "trunk", extra: attrs}}
}

type portForwardingCreateExt struct{ bodyExt }

func (e portForwardingCreateExt) ToPortForwardingCreateMap() (map[string]any, error) { return e.body() }

// WithPortForwardingCreateAttrs adds attrs to the top level of opts' "port_forwarding" body.
func WithPortForwardingCreateAttrs(opts portforwarding.CreateOptsBuilder, attrs map[string]any) portforwarding.CreateOptsBuilder {
	return portForwardingCreateExt{bodyExt{build: opts.ToPortForwardingCreateMap, key: "port_forwarding", extra: attrs}}
}

type portForwardingUpdateExt struct {
	bodyExt
	RevisionNumber *int `json:"-" h:"If-Match"`
}

func (e portForwardingUpdateExt) ToPortForwardingUpdateMap() (map[string]any, error) { return e.body() }

// WithPortForwardingUpdateAttrs adds attrs to the top level of opts' "port_forwarding" body,
// keeping its If-Match revision guard.
func WithPortForwardingUpdateAttrs(opts portforwarding.UpdateOptsBuilder, attrs map[string]any) portforwarding.UpdateOptsBuilder {
	return portForwardingUpdateExt{bodyExt: bodyExt{build: opts.ToPortForwardingUpdateMap, key: "port_forwarding", extra: attrs}, RevisionNumber: revisionOf(opts)}
}

// bodyExt layers extra top-level attributes onto a gophercloud request body.
// gophercloud's typed opts model only part of what neutron accepts (extension
// attributes such as qos_policy_id, dns_domain or port_security_enabled), so
// the With…Attrs functions compose a builder with this rather than growing one
// hand-written wrapper per missing key. build is the wrapped builder's own
// To<Resource><Verb>Map method and key the body's envelope ("network",
// "port", …). Extra attributes win over the typed ones, as upstream's
// attrs.update(extra_properties) does.
type bodyExt struct {
	build func() (map[string]any, error)
	key   string
	extra map[string]any
}

func (b bodyExt) body() (map[string]any, error) {
	base, err := b.build()
	if err != nil || len(b.extra) == 0 {
		return base, err
	}
	inner, ok := base[b.key].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected %s request body shape: %T", b.key, base[b.key])
	}
	for k, v := range b.extra {
		inner[k] = v
	}
	return base, nil
}

// revisionOf returns the RevisionNumber field of a gophercloud opts struct, or
// nil when it has none. Every update adapter re-declares that field with the
// same `h:"If-Match"` tag, because gophercloud.BuildHeaders reads headers off
// the top-level fields of the builder handed to Update — the adapter, not the
// opts inside it — so without this a wrapped update would silently drop
// neutron's revision guard.
func revisionOf(b any) *int {
	v := reflect.ValueOf(b)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	f := v.FieldByName("RevisionNumber")
	if !f.IsValid() {
		return nil
	}
	if r, ok := f.Interface().(*int); ok {
		return r
	}
	return nil
}
