package placement_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ftarasenko/koc/pkg/openstack/placement"
)

// Below 1.38 each value is a class's usage.
func TestUsageByClass_BothShapes(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want map[string]int
	}{
		"pre-1.38": {`{"VCPU": 3, "MEMORY_MB": 512}`, map[string]int{"VCPU": 3, "MEMORY_MB": 512}},
		"1.38 grouped": {`{"INSTANCE": {"VCPU": 3, "consumer_count": 2}, "unknown": {"VCPU": 1, "DISK_GB": 4, "consumer_count": 1}}`,
			map[string]int{"VCPU": 4, "DISK_GB": 4}},
	} {
		t.Run(name, func(t *testing.T) {
			var in map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.body), &in); err != nil {
				t.Fatal(err)
			}
			got, err := placement.UsageByClass(in)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("usageByClass = %v, want %v", got, tc.want)
			}
		})
	}
}
