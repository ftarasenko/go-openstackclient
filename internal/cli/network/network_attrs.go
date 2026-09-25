package network

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag and attribute names used only by the network noun's verbs. They are
// kept here rather than in flagnames.go so parallel parity batches do not
// collide; the "net" prefix marks them as this noun's.
const (
	netFlagExternal          = "external"
	netFlagInternal          = "internal"
	netFlagTransparentVLAN   = "transparent-vlan"
	netFlagNoTransparentVLAN = "no-transparent-vlan"
	netFlagProviderType      = "provider-network-type"
	netFlagProviderPhysNet   = "provider-physical-network"
	netFlagProviderSegment   = "provider-segment"

	netAttrRouterExternal  = "router:external"
	netAttrSegmentationID  = "provider:segmentation_id"
	netAttrPortSecurity    = "port_security_enabled"
	netAttrIsDefault       = "is_default"
	netAttrQoSPolicyID     = "qos_policy_id"
	netAttrDNSDomain       = "dns_domain"
	netAttrVLANTransparent = "vlan_transparent"
)

// NetExtAttrs carries the network extension attributes gophercloud's
// networks.Network does not model: qos_policy_id (qos), dns_domain
// (dns-integration), is_default (auto-allocated-topology),
// port_security_enabled (port-security), vlan_transparent,
// availability_zones and the address-scope pair. The booleans are pointers so
// a cloud without the extension renders an empty cell rather than "false".
// Exported and flat for the same reason as MTUExt.
type NetExtAttrs struct {
	QoSPolicyID         string   `json:"qos_policy_id"`
	DNSDomain           string   `json:"dns_domain"`
	IsDefault           *bool    `json:"is_default"`
	PortSecurityEnabled *bool    `json:"port_security_enabled"`
	VLANTransparent     *bool    `json:"vlan_transparent"`
	AvailabilityZones   []string `json:"availability_zones"`
	IPv4AddressScope    string   `json:"ipv4_address_scope"`
	IPv6AddressScope    string   `json:"ipv6_address_scope"`
}

// pairBool turns an upstream store_true pair (cobra keeps the two exclusive)
// into an optional value: true for on, false for off, nil when neither was given.
func pairBool(on, off bool) *bool {
	switch {
	case on:
		return boolPtr(true)
	case off:
		return boolPtr(false)
	default:
		return nil
	}
}

// setOptional stores *v under key when v is non-nil, allocating attrs as needed.
func setOptional(attrs map[string]any, key string, v *bool) map[string]any {
	if v == nil {
		return attrs
	}
	return mergeAttrs(attrs, map[string]any{key: *v})
}

// providerAttrs is the provider:* triple set sends as top-level attributes,
// matching upstream _get_attrs (each only when non-empty).
func providerAttrs(networkType, physNet, segment string) map[string]any {
	var attrs map[string]any
	for key, value := range map[string]string{
		fieldProviderNetworkType:     networkType,
		fieldProviderPhysicalNetwork: physNet,
		netAttrSegmentationID:        segment,
	} {
		if value != "" {
			attrs = mergeAttrs(attrs, map[string]any{key: value})
		}
	}
	return attrs
}

// networkCreateAttrs collects the create-time attributes networks.CreateOpts
// cannot carry, with --extra-property merged last so it wins (upstream's
// attrs.update(extra_properties)).
func networkCreateAttrs(ctx context.Context, client *gophercloud.ServiceClient, f *networkCreateFlags) (map[string]any, error) {
	var attrs map[string]any
	attrs = setOptional(attrs, netAttrPortSecurity, pairBool(f.enablePortSecurity, f.disablePortSecurity))
	attrs = setOptional(attrs, netAttrIsDefault, pairBool(f.defaultNet, f.noDefault))
	attrs = setOptional(attrs, netAttrVLANTransparent, pairBool(f.transparentVLAN, f.noTransparentVLAN))
	if f.qosPolicy != "" {
		qosID, err := resolveQoSPolicyID(ctx, client, f.qosPolicy)
		if err != nil {
			return nil, err
		}
		attrs = mergeAttrs(attrs, map[string]any{netAttrQoSPolicyID: qosID})
	}
	if f.dnsDomain != "" {
		attrs = mergeAttrs(attrs, map[string]any{netAttrDNSDomain: f.dnsDomain})
	}
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return nil, err
	}
	return mergeAttrs(attrs, extra), nil
}

// networkSetOpts builds the typed half of a network set: the fields
// networks.UpdateOpts models. changed reports whether any was given.
func networkSetOpts(f *networkSetFlags, flags flagSet) (opts networks.UpdateOpts, changed bool) {
	if flags.Changed("name") {
		opts.Name = &f.name
	}
	if flags.Changed(flagDescription) {
		opts.Description = &f.description
	}
	opts.AdminStateUp = enableDisable(flags, f.enable, f.disable)
	switch {
	case flags.Changed(flagShare):
		opts.Shared = boolPtr(f.share)
	case flags.Changed(flagNoShare) && f.noShare:
		opts.Shared = boolPtr(false)
	}
	changed = opts.Name != nil || opts.Description != nil || opts.AdminStateUp != nil || opts.Shared != nil
	return opts, changed
}

// networkSetAttrs builds the untyped half of a network set: every attribute
// networks.UpdateOpts lacks, mirroring upstream _get_attrs, then
// --extra-property last.
func networkSetAttrs(ctx context.Context, client *gophercloud.ServiceClient, f *networkSetFlags, flags flagSet) (map[string]any, error) {
	var attrs map[string]any
	if f.mtu > 0 {
		attrs = mergeAttrs(attrs, map[string]any{"mtu": f.mtu})
	}
	attrs = setOptional(attrs, netAttrPortSecurity,
		enableDisable(flags, f.enablePortSecurity, f.disablePortSecurity, flagEnablePortSecurity, flagDisablePortSecurity))
	attrs = setOptional(attrs, netAttrRouterExternal,
		enableDisable(flags, f.external, f.internal, netFlagExternal, netFlagInternal))
	attrs = setOptional(attrs, netAttrIsDefault,
		enableDisable(flags, f.defaultNet, f.noDefault, flagDefault, flagNoDefault))
	switch {
	case f.noQoSPolicy:
		attrs = mergeAttrs(attrs, map[string]any{netAttrQoSPolicyID: nil})
	case f.qosPolicy != "":
		qosID, err := resolveQoSPolicyID(ctx, client, f.qosPolicy)
		if err != nil {
			return nil, err
		}
		attrs = mergeAttrs(attrs, map[string]any{netAttrQoSPolicyID: qosID})
	}
	if flags.Changed(flagDNSDomain) {
		attrs = mergeAttrs(attrs, map[string]any{netAttrDNSDomain: f.dnsDomain})
	}
	attrs = mergeAttrs(attrs, providerAttrs(f.providerType, f.providerPhysNet, f.providerSegment))
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return nil, err
	}
	return mergeAttrs(attrs, extra), nil
}

// getNetwork fetches one network with every extension attribute koc renders.
func getNetwork(ctx context.Context, client *gophercloud.ServiceClient, id string) (*networkExt, error) {
	var n networkExt
	if err := networks.Get(ctx, client, id).ExtractInto(&n); err != nil {
		return nil, err
	}
	return &n, nil
}

// updateNetwork is the shared tail of set and unset: PUT the attributes when
// any were given (upstream skips the update when only tags change, and so
// does this), then apply the tag change, then render the network.
func updateNetwork(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref, id string,
	req networkUpdate, tags *tagWriteFlags, w io.Writer,
) error {
	var (
		n   *networkExt
		err error
	)
	if req.changed {
		n = &networkExt{}
		if err := networks.Update(ctx, client, id, withNetworkUpdateAttrs(req.opts, req.attrs)).ExtractInto(n); err != nil {
			return fmt.Errorf("updating network %s: %w", ref, err)
		}
	} else if n, err = getNetwork(ctx, client, id); err != nil {
		return fmt.Errorf("getting network %s: %w", ref, err)
	}
	if n.Tags, err = req.applyTags(ctx, client, tagResourceNetworks, id, n.Tags, tags); err != nil {
		return err
	}
	fields, values := networkShowFields(n)
	return o.WriteSingle(w, fields, values)
}

// networkUpdate is one set/unset request: the typed opts, the extra body
// attributes, whether a PUT is needed at all, and which tag helper to apply.
type networkUpdate struct {
	opts      networks.UpdateOpts
	attrs     map[string]any
	changed   bool
	applyTags func(context.Context, *gophercloud.ServiceClient, string, string, []string, *tagWriteFlags) ([]string, error)
}
