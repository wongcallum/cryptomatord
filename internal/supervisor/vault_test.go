package supervisor

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/callum/cryptomatord/internal/config"
	"github.com/callum/cryptomatord/internal/state"
)

func stubCLI(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../test/stub-cli.sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("stub-cli.sh not found: %v", err)
	}
	return p
}

// markerCheck mirrors the stub: a mount is "live" when the sibling marker exists.
func markerCheck(mountPoint string) bool {
	_, err := os.Stat(mountPoint + ".mounted")
	return err == nil
}

func newTestVault(t *testing.T, mountOptions ...string) *Vault {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Vault{
		Path:            filepath.Join(dir, "vault"),
		MountPoint:      filepath.Join(dir, "mnt"),
		PasswordCommand: "printf secret",
		MountOptions:    mountOptions,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	v := newVault("test", cfg, stubCLI(t), config.DefaultMounter, logger)
	v.mountCheck = markerCheck
	v.start()
	t.Cleanup(v.Shutdown)
	return v
}

func waitState(t *testing.T, v *Vault, want state.State, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if v.Status().State == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := v.Status()
	t.Fatalf("state = %s, want %s (error=%q)", got.State, want, got.Error)
}

func TestMountThenUnmount(t *testing.T) {
	v := newTestVault(t)

	if err := v.Mount(); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	waitState(t, v, state.Mounted, 5*time.Second)
	if !markerCheck(v.cfg.MountPoint) {
		t.Fatal("mount marker missing after mount")
	}

	if err := v.Unmount(); err != nil {
		t.Fatalf("Unmount: %v", err)
	}
	waitState(t, v, state.Unmounted, 5*time.Second)
	if markerCheck(v.cfg.MountPoint) {
		t.Fatal("mount marker should be gone after unmount")
	}
}

func TestBadPasswordFails(t *testing.T) {
	v := newTestVault(t, "stub-fail")

	err := v.Mount()
	if err == nil {
		t.Fatal("expected Mount to fail when unlock fails")
	}
	if st := v.Status(); st.State != state.Failed {
		t.Fatalf("state = %s, want failed", st.State)
	}
	if st := v.Status(); st.Error == "" {
		t.Error("expected a non-empty error on failure")
	}
}

func TestCrashTriggersRestart(t *testing.T) {
	// stub mounts, then "crashes" 1s later; the supervisor should restart it.
	v := newTestVault(t, "stub-crash=1")

	if err := v.Mount(); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	waitState(t, v, state.Mounted, 5*time.Second)
	// The crash should move it out of Mounted...
	waitState(t, v, state.Restarting, 4*time.Second)
	// ...and the backoff restart should bring it back.
	waitState(t, v, state.Mounted, 6*time.Second)
}

func TestMountIsIdempotent(t *testing.T) {
	v := newTestVault(t)

	if err := v.Mount(); err != nil {
		t.Fatalf("first Mount: %v", err)
	}
	waitState(t, v, state.Mounted, 5*time.Second)
	// A second Mount on an already-mounted vault is a no-op success.
	if err := v.Mount(); err != nil {
		t.Fatalf("second Mount: %v", err)
	}
	if st := v.Status(); st.State != state.Mounted {
		t.Fatalf("state = %s, want mounted", st.State)
	}
}

func TestUnmountWhenIdleIsNoop(t *testing.T) {
	v := newTestVault(t)
	if err := v.Unmount(); err != nil {
		t.Fatalf("Unmount on idle vault: %v", err)
	}
	if st := v.Status(); st.State != state.Unmounted {
		t.Fatalf("state = %s, want unmounted", st.State)
	}
}
