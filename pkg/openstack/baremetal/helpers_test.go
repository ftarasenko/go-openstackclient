package baremetal_test

import (
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/baremetal"
)

func TestCompareMicroversions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.82", "1.86", -1},
		{"1.86", "1.86", 0},
		{"1.90", "1.86", 1},
		{"1.9", "1.86", -1}, // 9 < 86: minor is numeric, not lexicographic
		{"2.1", "1.99", 1},
		{"garbage", "1.86", -1}, // unparseable must never suppress the explanation
	}
	for _, c := range cases {
		if got := baremetal.CompareMicroversions(c.a, c.b); got != c.want {
			t.Errorf("baremetal.CompareMicroversions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestExtractBIOSSetting_AcceptsBothKeyings(t *testing.T) {
	// Ironic keys the object by the setting name; gophercloud's own fixture uses
	// the literal "Setting". Both have to decode, or `bios setting show` returns
	// a blank row against one of them.
	for name, body := range map[string]any{
		"ironic":      map[string]any{"BootMode": map[string]any{"name": "BootMode", "value": "Uefi"}},
		"gophercloud": map[string]any{"Setting": map[string]any{"name": "BootMode", "value": "Uefi"}},
	} {
		var res nodes.GetBIOSSettingResult
		res.Body = body
		s, err := baremetal.ExtractBIOSSetting(res, "BootMode")
		if err != nil {
			t.Fatalf("%s keying: %v", name, err)
		}
		th.AssertEquals(t, "Uefi", s.Value)
	}
}

// fakePage is a minimal pagination.Page that is deliberately not an
// allocations.AllocationPage, exercising extractAllocations' guard against a
// page type it wasn't built to decode.
type fakePage struct{}

func (fakePage) NextPageURL() (string, error) { return "", nil }
func (fakePage) IsEmpty() (bool, error)       { return true, nil }
func (fakePage) GetBody() any                 { return nil }

func TestExtractAllocations_WrongPageTypeErrors(t *testing.T) {
	_, err := baremetal.ExtractAllocations(fakePage{})
	if err == nil {
		t.Fatal("expected an error for an unexpected page type, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected page type") {
		t.Errorf("error = %q, want it to mention the unexpected page type", err.Error())
	}
}
