// Package state defines the wire types shared between the supervisor, the HTTP
// API, and the ctl client. It is a leaf package (no other internal imports) so
// every layer can depend on it without creating import cycles.
package state

import "time"

// State is the lifecycle stage of a single vault.
type State string

const (
	// Unmounted: no child process, nothing mounted.
	Unmounted State = "unmounted"
	// Unlocking: a cryptomator-cli child has been spawned and we are waiting
	// for the mount to become observable.
	Unlocking State = "unlocking"
	// Mounted: the child is running and the mount point is live.
	Mounted State = "mounted"
	// Failed: the last mount attempt failed; see Status.Error.
	Failed State = "failed"
	// Restarting: the child exited unexpectedly and a restart is scheduled.
	Restarting State = "restarting"
)

// Status is a point-in-time snapshot of a vault, serialised over the control
// socket.
type Status struct {
	Name       string    `json:"name"`
	State      State     `json:"state"`
	Path       string    `json:"path"`
	MountPoint string    `json:"mountPoint"`
	AutoMount  bool      `json:"autoMount"`
	Since      time.Time `json:"since"`
	Error      string    `json:"error,omitempty"`
}
