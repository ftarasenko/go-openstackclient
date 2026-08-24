package server

import "testing"

// classifyMigrationState is the decision waitForMigration makes on every poll.
// Driving each outcome through the loop needs a live nova migration; here they
// are table rows.
func TestClassifyMigrationState(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		taskState string
		wantDone  bool
		wantErr   string
	}{
		{
			name:     "active with no task in flight is done",
			status:   "ACTIVE",
			wantDone: true,
		},
		{
			// Cold migration parks the server awaiting `server migrate confirm`.
			name:     "verify_resize with no task in flight is done",
			status:   "VERIFY_RESIZE",
			wantDone: true,
		},
		{
			// Nova leaves status ACTIVE while it sets up a live migration, so
			// task_state is what keeps the wait from returning immediately.
			name:      "active while still migrating keeps polling",
			status:    "ACTIVE",
			taskState: "migrating",
		},
		{
			name:      "resize state keeps polling",
			status:    "RESIZE",
			taskState: "resize_migrating",
		},
		{
			name:    "error is terminal",
			status:  "ERROR",
			wantErr: `server "web-1" entered ERROR status during migration`,
		},
		{
			// Nova's status casing has varied across releases, so the comparisons
			// are case-insensitive and must stay that way.
			name:     "status matching is case-insensitive",
			status:   "active",
			wantDone: true,
		},
		{
			name:    "lower-case error is still terminal",
			status:  "error",
			wantErr: `server "web-1" entered ERROR status during migration`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			done, err := classifyMigrationState("web-1", tt.status, tt.taskState)
			switch {
			case tt.wantErr != "":
				if err == nil {
					t.Fatalf("classifyMigrationState() error = nil, want %q", tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("classifyMigrationState() error = %q, want %q", err.Error(), tt.wantErr)
				}
				if done {
					t.Fatalf("classifyMigrationState() done = true on a terminal error")
				}
			case err != nil:
				t.Fatalf("classifyMigrationState() unexpected error: %v", err)
			case done != tt.wantDone:
				t.Fatalf("classifyMigrationState() done = %v, want %v", done, tt.wantDone)
			}
		})
	}
}
