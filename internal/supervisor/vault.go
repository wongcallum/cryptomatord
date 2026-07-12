package supervisor

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/callum/cryptomatord/internal/config"
	"github.com/callum/cryptomatord/internal/state"
)

// Timeouts and tuning for the supervisor loop.
const (
	passwordTimeout = 30 * time.Second
	mountTimeout    = 30 * time.Second
	unmountTimeout  = 15 * time.Second
	pollInterval    = 100 * time.Millisecond
	maxBackoff      = time.Minute
	stderrCapture   = 8 << 10 // keep the last 8 KiB of child output for errors
)

type cmdKind int

const (
	cmdMount cmdKind = iota
	cmdUnmount
	cmdShutdown
)

type command struct {
	kind  cmdKind
	reply chan error
}

// child is a running cryptomator-cli process.
type child struct {
	cmd    *exec.Cmd
	done   chan error // receives cmd.Wait()'s result exactly once
	stderr *boundedBuffer
}

// Vault supervises a single vault. All state transitions happen inside run(),
// a single goroutine, so the process lifecycle needs no locking; only the
// published status snapshot is mutex-guarded for concurrent readers.
type Vault struct {
	name       string
	cfg        config.Vault
	cliPath    string
	effMounter string
	logger     *slog.Logger

	// mountCheck reports whether the mount is live. Defaults to isMountpoint;
	// overridden in tests where a real FUSE mount isn't available.
	mountCheck func(string) bool

	cmds chan command

	mu     sync.Mutex
	status state.Status
}

func newVault(name string, cfg config.Vault, cliPath, defaultMounter string, logger *slog.Logger) *Vault {
	return &Vault{
		name:       name,
		cfg:        cfg,
		cliPath:    cliPath,
		effMounter: cfg.EffectiveMounter(defaultMounter),
		logger:     logger,
		mountCheck: isMountpoint,
		cmds:       make(chan command),
		status: state.Status{
			Name:       name,
			State:      state.Unmounted,
			Path:       cfg.Path,
			MountPoint: cfg.MountPoint,
			AutoMount:  cfg.ShouldAutoMount(),
			Since:      time.Now(),
		},
	}
}

func (v *Vault) start()         { go v.run() }
func (v *Vault) Mount() error   { return v.send(cmdMount) }
func (v *Vault) Unmount() error { return v.send(cmdUnmount) }
func (v *Vault) Shutdown()      { _ = v.send(cmdShutdown) }

func (v *Vault) send(kind cmdKind) error {
	reply := make(chan error, 1)
	v.cmds <- command{kind: kind, reply: reply}
	return <-reply
}

// Status returns a snapshot safe to read from any goroutine.
func (v *Vault) Status() state.Status {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.status
}

func (v *Vault) setState(s state.State, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.status.State = s
	v.status.Since = time.Now()
	if err != nil {
		v.status.Error = err.Error()
	} else {
		v.status.Error = ""
	}
}

// transition changes state while preserving the existing Error (used for
// Restarting, which keeps the crash cause visible).
func (v *Vault) transition(s state.State) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.status.State = s
	v.status.Since = time.Now()
}

// run is the vault's single-threaded supervisor loop.
func (v *Vault) run() {
	var (
		cur          *child           // current process, nil when stopped
		wantMounted  bool             // desired state
		backoff      time.Duration    // current restart backoff
		restartTimer *time.Timer      // pending restart, if any
		restartC     <-chan time.Time // restartTimer.C, or nil
	)

	stopRestart := func() {
		if restartTimer != nil {
			restartTimer.Stop()
			restartTimer = nil
		}
		restartC = nil
	}
	scheduleRestart := func() {
		backoff = nextBackoff(backoff)
		restartTimer = time.NewTimer(backoff)
		restartC = restartTimer.C
		v.transition(state.Restarting)
		v.logger.Warn("scheduling restart", "backoff", backoff.String())
	}

	for {
		// A nil channel blocks forever, disabling that select arm.
		var doneC chan error
		if cur != nil {
			doneC = cur.done
		}

		select {
		case cmd := <-v.cmds:
			switch cmd.kind {
			case cmdMount:
				wantMounted = true
				stopRestart()
				backoff = 0
				if cur != nil {
					cmd.reply <- nil
					break
				}
				c, err := v.doStart()
				if err == nil {
					cur = c
				}
				cmd.reply <- err

			case cmdUnmount:
				wantMounted = false
				stopRestart()
				backoff = 0
				v.doStop(cur)
				cur = nil
				cmd.reply <- nil

			case cmdShutdown:
				stopRestart()
				v.doStop(cur)
				cur = nil
				cmd.reply <- nil
				return
			}

		case werr := <-doneC:
			// The child exited on its own.
			tail := cur.stderr.tail()
			cur = nil
			if wantMounted {
				v.setState(state.Restarting, fmt.Errorf("cryptomator-cli exited: %v: %s", werr, tail))
				scheduleRestart()
			} else {
				v.setState(state.Unmounted, nil)
			}

		case <-restartC:
			stopRestart()
			if !wantMounted {
				break
			}
			c, err := v.doStart()
			if err != nil {
				scheduleRestart() // keep retrying with growing backoff
			} else {
				cur = c
				backoff = 0
			}
		}
	}
}

// doStart clears any stale mount, runs the password command, spawns
// cryptomator-cli, and blocks until the mount is observable, the child exits,
// or the timeout elapses. On success it returns the running child; on failure
// it sets Failed and returns the error.
func (v *Vault) doStart() (*child, error) {
	v.setState(state.Unlocking, nil)

	uctx, ucancel := context.WithTimeout(context.Background(), unmountTimeout)
	_ = tryUnmount(uctx, v.cfg.MountPoint) // best effort: clear a crashed mount
	ucancel()

	if err := ensureMountPoint(v.cfg.MountPoint); err != nil {
		v.setState(state.Failed, err)
		return nil, err
	}

	pctx, pcancel := context.WithTimeout(context.Background(), passwordTimeout)
	pass, err := runPasswordCommand(pctx, v.cfg.PasswordCommand)
	pcancel()
	if err != nil {
		v.setState(state.Failed, err)
		return nil, err
	}

	c, err := v.spawn(pass)
	zero(pass)
	if err != nil {
		v.setState(state.Failed, err)
		return nil, err
	}

	deadline := time.NewTimer(mountTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case werr := <-c.done:
			err := fmt.Errorf("cryptomator-cli exited during unlock: %v: %s", werr, c.stderr.tail())
			v.setState(state.Failed, err)
			return nil, err
		case <-deadline.C:
			v.killChild(c)
			err := fmt.Errorf("timed out waiting for mount at %s", v.cfg.MountPoint)
			v.setState(state.Failed, err)
			return nil, err
		case <-ticker.C:
			if v.mountCheck(v.cfg.MountPoint) {
				v.setState(state.Mounted, nil)
				v.logger.Info("mounted", "mountPoint", v.cfg.MountPoint)
				return c, nil
			}
		}
	}
}

// doStop stops the child (if any) and marks the vault unmounted.
func (v *Vault) doStop(c *child) {
	if c != nil {
		v.killChild(c)
	}
	v.setState(state.Unmounted, nil)
}

// spawn launches cryptomator-cli, feeds it the passphrase on stdin, and starts
// reaping it in the background.
func (v *Vault) spawn(pass []byte) (*child, error) {
	args := []string{
		"unlock",
		"--password:stdin",
		"--mounter=" + v.effMounter,
		"--mountPoint=" + v.cfg.MountPoint,
	}
	for _, mo := range v.cfg.MountOptions {
		args = append(args, "--mountOption="+mo)
	}
	args = append(args, v.cfg.Path)

	cmd := exec.Command(v.cliPath, args...)
	// Own process group so a terminal SIGINT during manual testing doesn't race
	// us to the child; we always signal it explicitly.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stderr := newBoundedBuffer(stderrCapture)
	// Same writer value for both so os/exec serialises the two copiers.
	sink := io.MultiWriter(&prefixWriter{w: os.Stderr, prefix: "[" + v.name + "] "}, stderr)
	cmd.Stdout = sink
	cmd.Stderr = sink

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start cryptomator-cli: %w", err)
	}
	if _, err := stdin.Write(pass); err != nil {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("write passphrase: %w", err)
	}
	_ = stdin.Close()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return &child{cmd: cmd, done: done, stderr: stderr}, nil
}

// killChild SIGINTs the child for a clean unmount, falling back to
// fusermount3 + SIGKILL, and reaps it. It must be called from run().
func (v *Vault) killChild(c *child) {
	_ = c.cmd.Process.Signal(syscall.SIGINT)
	select {
	case <-c.done:
	case <-time.After(unmountTimeout):
		v.logger.Warn("SIGINT did not unmount in time; forcing", "mountPoint", v.cfg.MountPoint)
		fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := tryUnmount(fctx, v.cfg.MountPoint); err != nil {
			v.logger.Warn("forced unmount failed", "error", err)
		}
		cancel()
		_ = c.cmd.Process.Kill()
		<-c.done // reap
	}
}
