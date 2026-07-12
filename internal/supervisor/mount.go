package supervisor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// isMountpoint reports whether path is a mount point, by comparing its device
// id against its parent's. This is how a FUSE mount becomes observable.
func isMountpoint(path string) bool {
	var st, pst syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return false
	}
	if err := syscall.Stat(filepath.Dir(path), &pst); err != nil {
		return false
	}
	return st.Dev != pst.Dev
}

// runPasswordCommand runs the shell command and returns its stdout with a
// trailing newline stripped (most secret stores append one). On failure the
// command's stderr is folded into the error.
func runPasswordCommand(ctx context.Context, command string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("password command failed: %w: %s", err, msg)
		}
		return nil, fmt.Errorf("password command failed: %w", err)
	}
	return bytes.TrimRight(stdout.Bytes(), "\r\n"), nil
}

// tryUnmount best-effort unmounts a FUSE mount point via fusermount3. Used both
// to clear a stale mount left by a crashed child and as a fallback when SIGINT
// doesn't unmount cleanly.
func tryUnmount(ctx context.Context, mountPoint string) error {
	out, err := exec.CommandContext(ctx, "fusermount3", "-u", mountPoint).CombinedOutput()
	if err != nil {
		return fmt.Errorf("fusermount3 -u %s: %w: %s", mountPoint, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ensureMountPoint creates the mount point directory and verifies it is empty,
// as required by libfuse.
func ensureMountPoint(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create mount point: %w", err)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("read mount point: %w", err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("mount point %s is not empty", path)
	}
	return nil
}

// nextBackoff doubles cur up to maxBackoff, starting at 1s.
func nextBackoff(cur time.Duration) time.Duration {
	if cur <= 0 {
		return time.Second
	}
	if n := cur * 2; n < maxBackoff {
		return n
	}
	return maxBackoff
}

// zero overwrites a secret byte slice.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// boundedBuffer retains only the last max bytes written. It captures the tail
// of a child's output for inclusion in error messages.
type boundedBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newBoundedBuffer(max int) *boundedBuffer { return &boundedBuffer{max: max} }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}

func (b *boundedBuffer) tail() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.buf))
}

// prefixWriter prefixes each complete line written to w. It is only ever
// written to from one goroutine (os/exec serialises writes when Stdout==Stderr).
type prefixWriter struct {
	w      io.Writer
	prefix string
	buf    []byte
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		i := bytes.IndexByte(p.buf, '\n')
		if i < 0 {
			break
		}
		if _, err := io.WriteString(p.w, p.prefix); err != nil {
			return 0, err
		}
		if _, err := p.w.Write(p.buf[:i+1]); err != nil {
			return 0, err
		}
		p.buf = p.buf[i+1:]
	}
	return len(b), nil
}
