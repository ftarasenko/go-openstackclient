package placement

import (
	"reflect"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/allocations"
)

// keepAllocations is the survivor computation behind "allocation unset".
// Placement replaces a consumer's whole allocation set on update, so getting
// this wrong silently hands back resources the operator did not name.
func TestKeepAllocations(t *testing.T) {
	const (
		p1 = "11111111-1111-1111-1111-111111111111"
		p2 = "22222222-2222-2222-2222-222222222222"
	)
	current := map[string]allocations.ProviderAllocations{
		p1: {Resources: map[string]int{"VCPU": 2, "MEMORY_MB": 1024}},
		p2: {Resources: map[string]int{"DISK_GB": 20}},
	}

	tests := []struct {
		name      string
		providers []string
		classes   []string
		want      map[string]allocations.ProviderAllocationsOpts
	}{
		{
			name: "naming neither keeps everything",
			want: map[string]allocations.ProviderAllocationsOpts{
				p1: {Resources: map[string]int{"VCPU": 2, "MEMORY_MB": 1024}},
				p2: {Resources: map[string]int{"DISK_GB": 20}},
			},
		},
		{
			name:      "a named provider is dropped whole",
			providers: []string{p1},
			want: map[string]allocations.ProviderAllocationsOpts{
				p2: {Resources: map[string]int{"DISK_GB": 20}},
			},
		},
		{
			name:    "a named class is dropped from every provider that has it",
			classes: []string{"VCPU"},
			want: map[string]allocations.ProviderAllocationsOpts{
				p1: {Resources: map[string]int{"MEMORY_MB": 1024}},
				p2: {Resources: map[string]int{"DISK_GB": 20}},
			},
		},
		{
			// Placement rejects a provider with an empty resources object, so a
			// provider whose last class was named must disappear, not survive
			// empty. The caller reads len(kept)==0 as "this is a DELETE".
			name:    "a provider left with no classes is dropped entirely",
			classes: []string{"DISK_GB"},
			want: map[string]allocations.ProviderAllocationsOpts{
				p1: {Resources: map[string]int{"VCPU": 2, "MEMORY_MB": 1024}},
			},
		},
		{
			name:      "the two filters combine",
			providers: []string{p2},
			classes:   []string{"MEMORY_MB"},
			want: map[string]allocations.ProviderAllocationsOpts{
				p1: {Resources: map[string]int{"VCPU": 2}},
			},
		},
		{
			name:      "naming everything leaves nothing",
			providers: []string{p2},
			classes:   []string{"VCPU", "MEMORY_MB"},
			want:      map[string]allocations.ProviderAllocationsOpts{},
		},
		{
			name:      "an unknown provider or class changes nothing",
			providers: []string{"33333333-3333-3333-3333-333333333333"},
			classes:   []string{"PCI_DEVICE"},
			want: map[string]allocations.ProviderAllocationsOpts{
				p1: {Resources: map[string]int{"VCPU": 2, "MEMORY_MB": 1024}},
				p2: {Resources: map[string]int{"DISK_GB": 20}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := keepAllocations(current, tt.providers, tt.classes)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("keepAllocations() = %+v, want %+v", got, tt.want)
			}
		})
	}

	// The read is the caller's, so the survivors must be a fresh map: mutating
	// them must not corrupt what was read back from placement.
	if got := keepAllocations(current, nil, []string{"VCPU"}); len(current[p1].Resources) != 2 {
		t.Errorf("keepAllocations() mutated its input: %+v (result %+v)", current, got)
	}
}

// An empty allocation set is the "already cleared" case and must not panic.
func TestKeepAllocations_Empty(t *testing.T) {
	if got := keepAllocations(nil, []string{"x"}, []string{"VCPU"}); len(got) != 0 {
		t.Errorf("keepAllocations(nil) = %+v, want empty", got)
	}
}
