package network

import "testing"

// badNetworkBuilder returns a "network" body whose value is not a
// map[string]any, exercising providerCreateOpts.ToNetworkCreateMap's guard
// against an unexpected body shape from its embedded CreateOptsBuilder.
type badNetworkBuilder struct{}

func (badNetworkBuilder) ToNetworkCreateMap() (map[string]any, error) {
	return map[string]any{"network": "not-a-map"}, nil
}

// okNetworkBuilder returns the normal {"network": {...}} shape every real
// networks.CreateOptsBuilder produces.
type okNetworkBuilder struct{}

func (okNetworkBuilder) ToNetworkCreateMap() (map[string]any, error) {
	return map[string]any{"network": map[string]any{"name": "netA"}}, nil
}

func TestProviderCreateOpts_UnexpectedBodyShapeErrors(t *testing.T) {
	opts := providerCreateOpts{
		CreateOptsBuilder: badNetworkBuilder{},
		NetworkType:       "vlan",
	}
	_, err := opts.ToNetworkCreateMap()
	if err == nil {
		t.Fatal("expected an error for an unexpected \"network\" body shape, got nil")
	}
}

func TestProviderCreateOpts_SetsProviderFields(t *testing.T) {
	opts := providerCreateOpts{
		CreateOptsBuilder: okNetworkBuilder{},
		NetworkType:       "vlan",
		PhysicalNetwork:   "physnet1",
		SegmentationID:    "42",
	}
	m, err := opts.ToNetworkCreateMap()
	if err != nil {
		t.Fatalf("ToNetworkCreateMap returned error: %v", err)
	}
	net, ok := m["network"].(map[string]any)
	if !ok {
		t.Fatalf("network body is %T, want map[string]any", m["network"])
	}
	if net["provider:network_type"] != "vlan" {
		t.Errorf("provider:network_type = %v, want vlan", net["provider:network_type"])
	}
	if net["provider:physical_network"] != "physnet1" {
		t.Errorf("provider:physical_network = %v, want physnet1", net["provider:physical_network"])
	}
	if net["provider:segmentation_id"] != "42" {
		t.Errorf("provider:segmentation_id = %v, want 42", net["provider:segmentation_id"])
	}
}
