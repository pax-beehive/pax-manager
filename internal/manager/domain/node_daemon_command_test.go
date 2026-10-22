package domain

import "testing"

func TestNodeDaemonCommandState(t *testing.T) {
	confirmation := func(status string) *NodeDaemonMaintenanceConfirmation {
		return &NodeDaemonMaintenanceConfirmation{Status: status}
	}
	tests := []struct {
		name    string
		command NodeDaemonCommand
		want    string
	}{
		{
			name:    "dispatching",
			command: NodeDaemonCommand{Type: NodeDaemonCommandPaxlUpgrade, Status: "dispatching"},
			want:    NodeDaemonCommandStateInProgress,
		},
		{
			name:    "paxl received",
			command: NodeDaemonCommand{Type: NodeDaemonCommandPaxlUpgrade, Status: "received"},
			want:    NodeDaemonCommandStateInProgress,
		},
		{
			name:    "paxl applied",
			command: NodeDaemonCommand{Type: NodeDaemonCommandPaxlUpgrade, Status: "applied"},
			want:    NodeDaemonCommandStateSucceeded,
		},
		{
			name:    "harness failed",
			command: NodeDaemonCommand{Type: NodeDaemonCommandHarnessUpgrade, Status: "failed"},
			want:    NodeDaemonCommandStateFailed,
		},
		{
			name:    "rejected",
			command: NodeDaemonCommand{Type: NodeDaemonCommandPaxdUpgrade, Status: "rejected"},
			want:    NodeDaemonCommandStateFailed,
		},
		{
			name: "dispatch failed",
			command: NodeDaemonCommand{
				Type:   NodeDaemonCommandPaxdRestart,
				Status: "dispatch_failed",
			},
			want: NodeDaemonCommandStateFailed,
		},
		{
			name:    "lost",
			command: NodeDaemonCommand{Type: NodeDaemonCommandPaxlUpgrade, Status: "lost"},
			want:    NodeDaemonCommandStateFailed,
		},
		{
			name: "upgrade awaiting new boot",
			command: NodeDaemonCommand{
				Type: NodeDaemonCommandPaxdUpgrade, Status: "received",
				MaintenanceConfirmation: confirmation("awaiting_new_boot"),
			},
			want: NodeDaemonCommandStateInProgress,
		},
		{
			name: "upgrade applied but not yet confirmed",
			command: NodeDaemonCommand{
				Type: NodeDaemonCommandPaxdUpgrade, Status: "applied",
				MaintenanceConfirmation: confirmation("awaiting_running"),
			},
			want: NodeDaemonCommandStateInProgress,
		},
		{
			name: "upgrade confirmed while paxd still says received",
			command: NodeDaemonCommand{
				Type: NodeDaemonCommandPaxdUpgrade, Status: "received",
				MaintenanceConfirmation: confirmation("confirmed"),
			},
			want: NodeDaemonCommandStateSucceeded,
		},
		{
			name: "upgrade version mismatch",
			command: NodeDaemonCommand{
				Type: NodeDaemonCommandPaxdUpgrade, Status: "received",
				MaintenanceConfirmation: confirmation("version_mismatch"),
			},
			want: NodeDaemonCommandStateFailed,
		},
		{
			name: "restart failed despite confirmation",
			command: NodeDaemonCommand{
				Type: NodeDaemonCommandPaxdRestart, Status: "failed",
				MaintenanceConfirmation: confirmation("confirmed"),
			},
			want: NodeDaemonCommandStateFailed,
		},
		{
			name:    "restart applied without tracking",
			command: NodeDaemonCommand{Type: NodeDaemonCommandPaxdRestart, Status: "applied"},
			want:    NodeDaemonCommandStateSucceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NodeDaemonCommandState(tt.command); got != tt.want {
				t.Fatalf("NodeDaemonCommandState() = %q, want %q", got, tt.want)
			}
		})
	}
}
