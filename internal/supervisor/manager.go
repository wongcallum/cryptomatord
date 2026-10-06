package supervisor

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/callum/cryptomatord/internal/config"
	"github.com/callum/cryptomatord/internal/state"
)

// ErrVaultNotFound is returned for an unknown vault name.
var ErrVaultNotFound = errors.New("vault not found")

// Manager owns the set of supervised vaults.
type Manager struct {
	vaults map[string]*Vault
	names  []string // sorted, for stable iteration
	logger *slog.Logger

	subMu sync.Mutex
	subs  map[chan struct{}]struct{}
}

// NewManager builds a manager from config. Call Start to launch supervision.
func NewManager(cfg *config.Config, logger *slog.Logger) *Manager {
	m := &Manager{
		vaults: make(map[string]*Vault),
		logger: logger,
		subs:   make(map[chan struct{}]struct{}),
	}
	for _, name := range cfg.VaultNames() {
		v := newVault(name, cfg.Vaults[name], cfg.CLIPath, cfg.DefaultMounter, logger.With("vault", name))
		v.onChange = m.notify
		m.vaults[name] = v
		m.names = append(m.names, name)
	}
	return m
}

// Subscribe returns a channel that receives a value whenever any vault's
// status changes, and a func to unsubscribe. The channel has a one-slot buffer
// and sends never block, so a burst of changes coalesces into one wakeup; the
// subscriber should re-read List() after each receive.
func (m *Manager) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	m.subMu.Lock()
	m.subs[ch] = struct{}{}
	m.subMu.Unlock()
	return ch, func() {
		m.subMu.Lock()
		delete(m.subs, ch)
		m.subMu.Unlock()
	}
}

func (m *Manager) notify() {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for ch := range m.subs {
		select {
		case ch <- struct{}{}:
		default: // a wakeup is already pending
		}
	}
}

// Start launches every vault's supervisor loop, then auto-mounts flagged vaults
// concurrently.
func (m *Manager) Start() {
	for _, name := range m.names {
		m.vaults[name].start()
	}
	for _, name := range m.names {
		v := m.vaults[name]
		if !v.cfg.ShouldAutoMount() {
			continue
		}
		go func(v *Vault) {
			if err := v.Mount(); err != nil {
				m.logger.Warn("auto-mount failed", "vault", v.name, "error", err)
			}
		}(v)
	}
}

// List returns a snapshot of every vault in sorted order.
func (m *Manager) List() []state.Status {
	out := make([]state.Status, 0, len(m.names))
	for _, name := range m.names {
		out = append(out, m.vaults[name].Status())
	}
	return out
}

// Status returns one vault's snapshot.
func (m *Manager) Status(name string) (state.Status, error) {
	v, ok := m.vaults[name]
	if !ok {
		return state.Status{}, ErrVaultNotFound
	}
	return v.Status(), nil
}

// Mount mounts a vault and returns its resulting status. A mount failure is
// reflected in the returned status (State=failed, Error set), not the error;
// the error is non-nil only for lookup failures.
func (m *Manager) Mount(name string) (state.Status, error) {
	v, ok := m.vaults[name]
	if !ok {
		return state.Status{}, ErrVaultNotFound
	}
	_ = v.Mount()
	return v.Status(), nil
}

// Unmount unmounts a vault and returns its resulting status.
func (m *Manager) Unmount(name string) (state.Status, error) {
	v, ok := m.vaults[name]
	if !ok {
		return state.Status{}, ErrVaultNotFound
	}
	_ = v.Unmount()
	return v.Status(), nil
}

// Shutdown unmounts all vaults, bounded by ctx.
func (m *Manager) Shutdown(ctx context.Context) {
	var wg sync.WaitGroup
	for _, name := range m.names {
		wg.Add(1)
		go func(v *Vault) {
			defer wg.Done()
			v.Shutdown()
		}(m.vaults[name])
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		m.logger.Warn("shutdown timed out; some mounts may remain")
	}
}
