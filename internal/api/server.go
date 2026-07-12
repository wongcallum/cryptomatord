// Package api serves the control API over a unix domain socket.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/callum/cryptomatord/internal/state"
	"github.com/callum/cryptomatord/internal/supervisor"
)

// Manager is the subset of the supervisor the API needs.
type Manager interface {
	List() []state.Status
	Status(name string) (state.Status, error)
	Mount(name string) (state.Status, error)
	Unmount(name string) (state.Status, error)
}

// ErrorResponse is the body returned for error status codes.
type ErrorResponse struct {
	Error string `json:"error"`
}

// Server exposes a Manager over HTTP on a unix socket.
type Server struct {
	mgr    Manager
	socket string
	logger *slog.Logger
	http   *http.Server
	ln     net.Listener
}

// NewServer wires the routes. Call Listen then Serve.
func NewServer(mgr Manager, socket string, logger *slog.Logger) *Server {
	s := &Server{mgr: mgr, socket: socket, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /vaults", s.handleList)
	mux.HandleFunc("GET /vaults/{name}", s.handleGet)
	mux.HandleFunc("POST /vaults/{name}/mount", s.handleMount)
	mux.HandleFunc("POST /vaults/{name}/unmount", s.handleUnmount)
	s.http = &http.Server{Handler: mux}
	return s
}

// Listen creates the socket directory and unix listener with 0600 perms.
func (s *Server) Listen() error {
	if err := os.MkdirAll(filepath.Dir(s.socket), 0o700); err != nil {
		return err
	}
	if err := os.Remove(s.socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ln, err := net.Listen("unix", s.socket)
	if err != nil {
		return err
	}
	if err := os.Chmod(s.socket, 0o600); err != nil {
		_ = ln.Close()
		return err
	}
	s.ln = ln
	return nil
}

// Serve blocks serving requests until Shutdown is called.
func (s *Server) Serve() error {
	if err := s.http.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown gracefully stops the server and removes the socket.
func (s *Server) Shutdown(ctx context.Context) {
	_ = s.http.Shutdown(ctx)
	_ = os.Remove(s.socket)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleList(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.mgr.List())
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	st, err := s.mgr.Status(r.PathValue("name"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleMount(w http.ResponseWriter, r *http.Request) {
	st, err := s.mgr.Mount(r.PathValue("name"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleUnmount(w http.ResponseWriter, r *http.Request) {
	st, err := s.mgr.Unmount(r.PathValue("name"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, st)
}

func (s *Server) writeError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	if errors.Is(err, supervisor.ErrVaultNotFound) {
		code = http.StatusNotFound
	}
	s.writeJSON(w, code, ErrorResponse{Error: err.Error()})
}

func (s *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.logger.Debug("encode response", "error", err)
	}
}
