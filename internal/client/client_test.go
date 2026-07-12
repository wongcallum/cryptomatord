package client_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/callum/cryptomatord/internal/api"
	"github.com/callum/cryptomatord/internal/client"
	"github.com/callum/cryptomatord/internal/state"
	"github.com/callum/cryptomatord/internal/supervisor"
)

// fakeManager implements api.Manager without spawning processes.
type fakeManager struct {
	statuses map[string]state.Status
}

func (f *fakeManager) List() []state.Status {
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
	s, ok := f.statuses[name]
	if !ok {
		return state.Status{}, supervisor.ErrVaultNotFound
	}
	return s, nil
}

func (f *fakeManager) Mount(name string) (state.Status, error) {
	s, ok := f.statuses[name]
	if !ok {
		return state.Status{}, supervisor.ErrVaultNotFound
	}
	s.State = state.Mounted
	f.statuses[name] = s
	return s, nil
}

func (f *fakeManager) Unmount(name string) (state.Status, error) {
	s, ok := f.statuses[name]
	if !ok {
		return state.Status{}, supervisor.ErrVaultNotFound
	}
	s.State = state.Unmounted
	f.statuses[name] = s
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
