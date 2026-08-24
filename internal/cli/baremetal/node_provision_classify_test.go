package baremetal

import (
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"
)

// classifyProvisionState is the decision waitForProvisionState makes on every
// poll. Reaching each terminal outcome through the live loop needs a real async
// ironic transition; here every one of them is a table row.
func TestClassifyProvisionState(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"

	tests := []struct {
		name      string
		provision string
		target    string
		lastError string
		want      nodes.ProvisionState
		wantDone  bool
		wantErr   string
	}{
		{
			name:      "settled in the wanted state is done",
			provision: "active",
			want:      nodes.Active,
			wantDone:  true,
		},
		{
			name:      "transition still in flight keeps polling",
			provision: "deploying",
			target:    "active",
			want:      nodes.Active,
		},
		{
			// The node already sits in `want` when the verb starts (rebuild of an
			// active node): target_provision_state is still set, so the wait must
			// not report success before ironic has begun the transition.
			name:      "already in the wanted state but not settled keeps polling",
			provision: "active",
			target:    "active",
			want:      nodes.Active,
		},
		{
			name:      "failure state is terminal",
			provision: "deploy failed",
			target:    "active",
			lastError: "no valid host",
			want:      nodes.Active,
			wantErr:   `node ` + id + ` entered failure state "deploy failed": no valid host`,
		},
		{
			name:      "error state is terminal",
			provision: string(nodes.Error),
			want:      nodes.Active,
			wantErr:   `node ` + id + ` entered failure state "error": `,
		},
		{
			// manage verify failure lands the node back on "enroll" with the target
			// cleared: terminal, and waiting the full timeout would tell nobody why.
			name:      "settled somewhere unexpected is terminal",
			provision: "enroll",
			want:      nodes.Manageable,
			wantErr:   `node ` + id + ` settled in unexpected state "enroll" instead of "manageable"`,
		},
		{
			name:      "settled somewhere unexpected reports last_error when present",
			provision: "enroll",
			lastError: "credentials rejected",
			want:      nodes.Manageable,
			wantErr:   `node ` + id + ` settled in unexpected state "enroll" instead of "manageable": credentials rejected`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := &nodes.Node{
				ProvisionState:       tt.provision,
				TargetProvisionState: tt.target,
				LastError:            tt.lastError,
			}
			done, err := classifyProvisionState(n, id, tt.want)
			switch {
			case tt.wantErr != "":
				if err == nil {
					t.Fatalf("classifyProvisionState() error = nil, want %q", tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("classifyProvisionState() error = %q, want %q", err.Error(), tt.wantErr)
				}
				if done {
					t.Fatalf("classifyProvisionState() done = true on a terminal error")
				}
			case err != nil:
				t.Fatalf("classifyProvisionState() unexpected error: %v", err)
			case done != tt.wantDone:
				t.Fatalf("classifyProvisionState() done = %v, want %v", done, tt.wantDone)
			}
		})
	}
}
