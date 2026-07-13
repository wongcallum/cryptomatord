// Package config loads and validates the daemon's JSON configuration. The
// NixOS module generates this file with pkgs.formats.json, but it is also
// hand-writable.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// DefaultMounter is used when neither a per-vault mounter nor a top-level
// defaultMounter is set. It is the native FUSE mounter on Linux.
const DefaultMounter = "org.cryptomator.frontend.fuse.mount.LinuxFuseMountProvider"

// DefaultCLI is the cryptomator-cli binary looked up on PATH when cliPath is
// unset.
const DefaultCLI = "cryptomator-cli"

// Config is the whole daemon configuration.
type Config struct {
	// Socket is the control unix socket path. Defaults to
	// $XDG_RUNTIME_DIR/cryptomatord/control.sock.
	Socket string `json:"socket"`
	// CLIPath is the cryptomator-cli executable. Defaults to "cryptomator-cli"
	// resolved on PATH.
	CLIPath string `json:"cliPath"`
	// DefaultMounter is the fallback mounter class for vaults that don't set one.
	DefaultMounter string `json:"defaultMounter"`
	// Vaults is keyed by vault name.
	Vaults map[string]Vault `json:"vaults"`
}

// Vault is a single managed vault.
type Vault struct {
	// Path is the vault directory (containing vault.cryptomator).
	Path string `json:"path"`
	// MountPoint is an (ideally empty) directory to mount the cleartext at.
	MountPoint string `json:"mountPoint"`
	// PasswordCommand is a shell command whose stdout is the passphrase.
	PasswordCommand string `json:"passwordCommand"`
	// AutoMount controls whether the vault is mounted at daemon startup.
	// Defaults to true when omitted.
	AutoMount *bool `json:"autoMount"`
	// Mounter overrides DefaultMounter for this vault. Empty => DefaultMounter.
	Mounter string `json:"mounter"`
	// MountOptions are passed through as repeated --mountOption flags.
	MountOptions []string `json:"mountOptions"`
}

// ShouldAutoMount reports the effective auto-mount setting (default true).
func (v Vault) ShouldAutoMount() bool {
	return v.AutoMount == nil || *v.AutoMount
}

// EffectiveMounter returns the mounter class to use, falling back to def.
func (v Vault) EffectiveMounter(def string) string {
	if v.Mounter != "" {
		return v.Mounter
	}
	return def
}

// Load reads, parses, defaults and validates the config at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Socket == "" {
		c.Socket = DefaultSocketPath()
	}
	if c.CLIPath == "" {
		c.CLIPath = DefaultCLI
	}
	if c.DefaultMounter == "" {
		c.DefaultMounter = DefaultMounter
	}
}

func (c *Config) validate() error {
	for _, name := range c.VaultNames() {
		v := c.Vaults[name]
		if name == "" {
			return fmt.Errorf("vault has empty name")
		}
		if v.Path == "" {
			return fmt.Errorf("vault %q: path is required", name)
		}
		if !filepath.IsAbs(v.Path) {
			return fmt.Errorf("vault %q: path must be absolute, got %q", name, v.Path)
		}
		if v.MountPoint == "" {
			return fmt.Errorf("vault %q: mountPoint is required", name)
		}
		if !filepath.IsAbs(v.MountPoint) {
			return fmt.Errorf("vault %q: mountPoint must be absolute, got %q", name, v.MountPoint)
		}
		if v.PasswordCommand == "" {
			return fmt.Errorf("vault %q: passwordCommand is required", name)
		}
	}
	return nil
}

// VaultNames returns the configured vault names in sorted order for stable
// iteration.
func (c *Config) VaultNames() []string {
	names := make([]string, 0, len(c.Vaults))
	for name := range c.Vaults {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DefaultSocketPath is $XDG_RUNTIME_DIR/cryptomatord/control.sock, or a
// tmp-based fallback when XDG_RUNTIME_DIR is unset.
func DefaultSocketPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("cryptomatord-%d", os.Getuid()))
	}
	return filepath.Join(dir, "cryptomatord", "control.sock")
}

// DefaultConfigPath is $XDG_CONFIG_HOME/cryptomatord/config.json (or
// $HOME/.config/cryptomatord/config.json when XDG_CONFIG_HOME is unset). It is
// what `serve` loads when no --config is given, so the home-manager module can
// drop the config there and the NixOS module needs no store-path indirection.
func DefaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		// Neither XDG_CONFIG_HOME nor HOME set; fall back to a tmp path so the
		// error surfaces as "file not found" rather than a relative path.
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("cryptomatord-%d", os.Getuid()))
	}
	return filepath.Join(dir, "cryptomatord", "config.json")
}
