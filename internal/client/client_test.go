package client_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/callum/cryptomatord/internal/api"
	"github.com/callum/cryptomatord/internal/client"
	"github.com/callum/cryptomatord/internal/state"
	"github.com/callum/cryptomatord/internal/supervisor"
)

// fakeManager implements api.Manager without spawning processes.
type fakeManager struct {
	mu       sync.Mutex
	statuses map[string]state.Status
	subs     []chan struct{}
}

func (f *fakeManager) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	f.mu.Lock()
	f.subs = append(f.subs, ch)
	f.mu.Unlock()
	return ch, func() {}
}

func (f *fakeManager) notify() {
	for _, ch := range f.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (f *fakeManager) List() []state.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.statuses))
	for n := range f.statuses {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]state.Status, 0, len(names))
	for _, n := range names {
		out = append(out, f.statuses[n])
	}
	return out
}

func (f *fakeManager) Status(name string) (state.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.statuses[name]
	if !ok {
		return state.Status{}, supervisor.ErrVaultNotFound
	}
	return s, nil
}

func (f *fakeManager) Mount(name string) (state.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.statuses[name]
	if !ok {
		return state.Status{}, supervisor.ErrVaultNotFound
	}
	s.State = state.Mounted
	f.statuses[name] = s
	f.notify()
	return s, nil
}

func (f *fakeManager) Unmount(name string) (state.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.statuses[name]
	if !ok {
		return state.Status{}, supervisor.ErrVaultNotFound
	}
	s.State = state.Unmounted
	f.statuses[name] = s
	f.notify()
	return s, nil
}

func startServer(t *testing.T) *client.Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "cmd") // short path: unix sockets cap at ~108 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")

	fm := &fakeManager{statuses: map[string]state.Status{
		"work": {Name: "work", State: state.Unmounted, Path: "/data/work", MountPoint: "/mnt/work"},
		"docs": {Name: "docs", State: state.Unmounted, Path: "/data/docs", MountPoint: "/mnt/docs"},
	}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := api.NewServer(fm, sock, logger)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	})
	return client.New(sock)
}

func TestClientHealthAndList(t *testing.T) {
	cl := startServer(t)
	ctx := context.Background()

	if err := cl.Health(ctx); err != nil {
		t.Fatalf("Health: %v", err)
	}
	list, err := cl.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List returned %d vaults, want 2", len(list))
	}
	if list[0].Name != "docs" || list[1].Name != "work" {
		t.Errorf("List not sorted: %v", []string{list[0].Name, list[1].Name})
	}
}

func TestClientMountUnmount(t *testing.T) {
	cl := startServer(t)
	ctx := context.Background()

	st, err := cl.Mount(ctx, "work")
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if st.State != state.Mounted {
		t.Errorf("after Mount state = %s, want mounted", st.State)
	}

	st, err = cl.Unmount(ctx, "work")
	if err != nil {
		t.Fatalf("Unmount: %v", err)
	}
	if st.State != state.Unmounted {
		t.Errorf("after Unmount state = %s, want unmounted", st.State)
	}
}

func TestClientNotFound(t *testing.T) {
	cl := startServer(t)
	ctx := context.Background()

	if _, err := cl.Status(ctx, "nope"); err == nil {
		t.Fatal("expected error for unknown vault")
	}
	if _, err := cl.Mount(ctx, "nope"); err == nil {
		t.Fatal("expected error mounting unknown vault")
	}
}

func TestClientWatch(t *testing.T) {
	cl := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	snaps := make(chan []state.Status, 8)
	errC := make(chan error, 1)
	go func() { errC <- cl.Watch(ctx, func(l []state.Status) { snaps <- l }) }()

	// The initial snapshot arrives on connect.
	first := <-snaps
	if len(first) != 2 || first[1].State != state.Unmounted {
		t.Fatalf("initial snapshot = %+v", first)
	}

	if _, err := cl.Mount(ctx, "work"); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	select {
	case l := <-snaps:
		if l[1].Name != "work" || l[1].State != state.Mounted {
			t.Errorf("after Mount snapshot = %+v", l)
		}
	case <-ctx.Done():
		t.Fatal("no snapshot pushed after Mount")
	}

	cancel()
	if err := <-errC; err != context.Canceled {
		t.Errorf("Watch returned %v, want context.Canceled", err)
	}
}
