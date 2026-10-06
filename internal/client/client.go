// Package client is the ctl-side HTTP client that dials the daemon's unix
// socket.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/callum/cryptomatord/internal/state"
)

// Client talks to the control API over a unix socket.
type Client struct {
	http   *http.Client
	stream *http.Client // no overall timeout, for long-lived /events streams
	base   string
}

// New returns a client dialing the given unix socket path.
func New(socket string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}
	return &Client{
		http:   &http.Client{Transport: tr, Timeout: 90 * time.Second},
		stream: &http.Client{Transport: tr},
		// Host is ignored for unix transports but required to form a valid URL.
		base: "http://unix",
	}
}

// Health pings the daemon.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/healthz", nil)
}

// List returns all vault statuses.
func (c *Client) List(ctx context.Context) ([]state.Status, error) {
	var out []state.Status
	return out, c.do(ctx, http.MethodGet, "/vaults", &out)
}

// Status returns one vault's status.
func (c *Client) Status(ctx context.Context, name string) (state.Status, error) {
	var out state.Status
	return out, c.do(ctx, http.MethodGet, "/vaults/"+url.PathEscape(name), &out)
}

// Mount mounts a vault and returns the resulting status.
func (c *Client) Mount(ctx context.Context, name string) (state.Status, error) {
	var out state.Status
	return out, c.do(ctx, http.MethodPost, "/vaults/"+url.PathEscape(name)+"/mount", &out)
}

// Unmount unmounts a vault and returns the resulting status.
func (c *Client) Unmount(ctx context.Context, name string) (state.Status, error) {
	var out state.Status
	return out, c.do(ctx, http.MethodPost, "/vaults/"+url.PathEscape(name)+"/unmount", &out)
}

// Watch calls fn with the full vault list on connect and after every change,
// until ctx is cancelled (returns ctx.Err()) or the daemon closes the stream.
func (c *Client) Watch(ctx context.Context, fn func([]state.Status)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/events", nil)
	if err != nil {
		return err
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to daemon: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("daemon returned %s", resp.Status)
	}

	dec := json.NewDecoder(resp.Body)
	for {
		var list []state.Status
		if err := dec.Decode(&list); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("daemon closed the event stream")
			}
			return fmt.Errorf("reading event stream: %w", err)
		}
		fn(list)
	}
}

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to daemon: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("daemon returned %s", resp.Status)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}
	return nil
}
