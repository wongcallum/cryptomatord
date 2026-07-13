package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaults(t *testing.T) {
	p := writeTemp(t, `{
		"vaults": {
			"work": {
				"path": "/data/work",
				"mountPoint": "/mnt/work",
				"passwordCommand": "echo hunter2"
			}
		}
	}`)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.CLIPath != DefaultCLI {
		t.Errorf("CLIPath = %q, want default %q", c.CLIPath, DefaultCLI)
	}
	if c.DefaultMounter != DefaultMounter {
		t.Errorf("DefaultMounter = %q, want default", c.DefaultMounter)
	}
	if c.Socket == "" {
		t.Error("Socket should default to a non-empty path")
	}
	v := c.Vaults["work"]
	if !v.ShouldAutoMount() {
		t.Error("autoMount should default to true when omitted")
	}
	if got := v.EffectiveMounter(c.DefaultMounter); got != DefaultMounter {
		t.Errorf("EffectiveMounter = %q, want default", got)
	}
}

func TestAutoMountExplicitFalse(t *testing.T) {
	p := writeTemp(t, `{
		"vaults": {
			"work": {"path": "/data/work", "mountPoint": "/mnt/work", "passwordCommand": "x", "autoMount": false}
		}
	}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Vaults["work"].ShouldAutoMount() {
		t.Error("autoMount:false should disable auto-mount")
	}
}

func TestPerVaultMounterOverride(t *testing.T) {
	p := writeTemp(t, `{
		"defaultMounter": "com.example.Default",
		"vaults": {"v": {"path": "/a", "mountPoint": "/b", "passwordCommand": "x", "mounter": "com.example.Custom"}}
	}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Vaults["v"].EffectiveMounter(c.DefaultMounter); got != "com.example.Custom" {
		t.Errorf("EffectiveMounter = %q, want custom", got)
	}
}

func TestDefaultConfigPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/conf")
	if got, want := DefaultConfigPath(), "/xdg/conf/cryptomatord/config.json"; got != want {
		t.Errorf("DefaultConfigPath = %q, want %q", got, want)
	}

	// With XDG_CONFIG_HOME unset it derives from HOME/.config.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/alice")
	if got, want := DefaultConfigPath(), "/home/alice/.config/cryptomatord/config.json"; got != want {
		t.Errorf("DefaultConfigPath = %q, want %q", got, want)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]string{
		"missing path":       `{"vaults":{"v":{"mountPoint":"/b","passwordCommand":"x"}}}`,
		"relative path":      `{"vaults":{"v":{"path":"rel","mountPoint":"/b","passwordCommand":"x"}}}`,
		"missing mountPoint": `{"vaults":{"v":{"path":"/a","passwordCommand":"x"}}}`,
		"relative mount":     `{"vaults":{"v":{"path":"/a","mountPoint":"rel","passwordCommand":"x"}}}`,
		"missing password":   `{"vaults":{"v":{"path":"/a","mountPoint":"/b"}}}`,
		"unknown field":      `{"nope": true, "vaults":{}}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeTemp(t, content)); err == nil {
				t.Errorf("expected error for %s", name)
			}
		})
	}
}
