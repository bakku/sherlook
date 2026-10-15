// Package config loads and saves sherlook's TOML configuration and manages
// mailbox client secrets (OS keyring, falling back to the config file).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// CurrentVersion is the config schema version written by this build.
const CurrentVersion = 1

// Mailbox is a shared mailbox together with the app registration used to access it.
type Mailbox struct {
	Address  string `toml:"address"`
	Label    string `toml:"label,omitempty"`
	TenantID string `toml:"tenant_id"`
	ClientID string `toml:"client_id"`
	// ClientSecret is only persisted here when no OS keyring is available.
	ClientSecret string `toml:"client_secret,omitempty"`
}

// DisplayName is the label if set, otherwise the address.
func (m Mailbox) DisplayName() string {
	if m.Label != "" {
		return m.Label
	}
	return m.Address
}

// Config is the on-disk configuration.
type Config struct {
	Version   int       `toml:"version"`
	ExportDir string    `toml:"export_dir,omitempty"`
	Mailboxes []Mailbox `toml:"mailbox"`
}

// DefaultPath returns <user config dir>/sherlook/config.toml.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sherlook", "config.toml"), nil
}

// Load reads the config at path. A missing file yields an empty config.
// Warnings describe non-fatal problems such as overly broad file permissions.
func Load(path string) (*Config, []string, error) {
	cfg := &Config{Version: CurrentVersion}
	var warnings []string

	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		warnings = append(warnings, fmt.Sprintf("%s is accessible by other users (mode %04o); run: chmod 600 %s", path, info.Mode().Perm(), path))
	}

	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if cfg.Version == 0 {
		cfg.Version = CurrentVersion
	}
	if cfg.Version > CurrentVersion {
		return nil, nil, fmt.Errorf("%s has config version %d, but this sherlook only supports version %d; please upgrade", path, cfg.Version, CurrentVersion)
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, warnings, nil
}

// Save writes the config atomically with mode 0600.
func Save(path string, cfg *Config) error {
	cfg.Version = CurrentVersion
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := toml.NewEncoder(tmp).Encode(cfg); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Validate checks required fields and address uniqueness.
func (c *Config) Validate() error {
	seen := map[string]bool{}
	for i, mb := range c.Mailboxes {
		if err := mb.validate(); err != nil {
			return fmt.Errorf("mailbox #%d: %w", i+1, err)
		}
		key := normalize(mb.Address)
		if seen[key] {
			return fmt.Errorf("mailbox %s is configured more than once", mb.Address)
		}
		seen[key] = true
	}
	return nil
}

// Index returns the position of the mailbox with the given address
// (case-insensitive), or -1.
func (c *Config) Index(address string) int {
	key := normalize(address)
	for i, mb := range c.Mailboxes {
		if normalize(mb.Address) == key {
			return i
		}
	}
	return -1
}

func (m Mailbox) validate() error {
	switch {
	case strings.TrimSpace(m.Address) == "":
		return errors.New("address is required")
	case !strings.Contains(m.Address, "@"):
		return fmt.Errorf("address %q is not an email address", m.Address)
	case strings.TrimSpace(m.TenantID) == "":
		return errors.New("tenant ID is required")
	case strings.TrimSpace(m.ClientID) == "":
		return errors.New("client ID is required")
	}
	return nil
}

func normalize(address string) string {
	return strings.ToLower(strings.TrimSpace(address))
}
