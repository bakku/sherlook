package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

const keyringService = "sherlook"

// ErrSecretNotFound means no secret is stored for a mailbox.
var ErrSecretNotFound = errors.New("secret not found")

// Keyring stores secrets keyed by mailbox address.
type Keyring interface {
	Get(account string) (string, error)
	Set(account, secret string) error
	Delete(account string) error
}

// OSKeyring uses the platform keyring (Secret Service, macOS Keychain, Windows
// Credential Manager). Calls time out so a missing or hanging Secret Service
// daemon on a headless box doesn't freeze the app.
type OSKeyring struct {
	Timeout time.Duration
}

func (k OSKeyring) Get(account string) (string, error) {
	var secret string
	err := k.run(func() error {
		var err error
		secret, err = keyring.Get(keyringService, account)
		return err
	})
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrSecretNotFound
	}
	return secret, err
}

func (k OSKeyring) Set(account, secret string) error {
	return k.run(func() error { return keyring.Set(keyringService, account, secret) })
}

func (k OSKeyring) Delete(account string) error {
	err := k.run(func() error { return keyring.Delete(keyringService, account) })
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrSecretNotFound
	}
	return err
}

func (k OSKeyring) run(fn func() error) error {
	timeout := k.Timeout
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return errors.New("keyring did not respond")
	}
}

// Store couples the config file with secret storage.
type Store struct {
	Path    string
	Keyring Keyring
	Config  *Config
}

// Open loads the config at path.
func Open(path string, kr Keyring) (*Store, []string, error) {
	cfg, warnings, err := Load(path)
	if err != nil {
		return nil, nil, err
	}
	return &Store{Path: path, Keyring: kr, Config: cfg}, warnings, nil
}

// Secret resolves the client secret of a mailbox: config file first (only
// set when the keyring was unavailable), then the keyring.
func (s *Store) Secret(mb Mailbox) (string, error) {
	if mb.ClientSecret != "" {
		return mb.ClientSecret, nil
	}
	secret, err := s.Keyring.Get(normalize(mb.Address))
	if errors.Is(err, ErrSecretNotFound) {
		return "", fmt.Errorf("no client secret stored for %s; edit the mailbox to set one", mb.Address)
	}
	if err != nil {
		return "", fmt.Errorf("reading client secret for %s from keyring: %w", mb.Address, err)
	}
	return secret, nil
}

// SecretInFile reports whether the mailbox's secret lives in the config file.
func (s *Store) SecretInFile(mb Mailbox) bool {
	return mb.ClientSecret != ""
}

// Upsert adds a mailbox (oldAddress == "") or replaces the one currently at
// oldAddress. An empty newSecret keeps the existing secret when editing. The
// returned warning is non-empty if the secret had to be written to the config
// file because the keyring is unavailable.
func (s *Store) Upsert(oldAddress string, mb Mailbox, newSecret string) (warning string, err error) {
	mb.Address = strings.TrimSpace(mb.Address)
	mb.Label = strings.TrimSpace(mb.Label)
	mb.TenantID = strings.TrimSpace(mb.TenantID)
	mb.ClientID = strings.TrimSpace(mb.ClientID)
	mb.ClientSecret = ""
	if err := mb.validate(); err != nil {
		return "", err
	}

	oldIdx := -1
	if oldAddress != "" {
		oldIdx = s.Config.Index(oldAddress)
		if oldIdx < 0 {
			return "", fmt.Errorf("mailbox %s no longer exists", oldAddress)
		}
	}
	if i := s.Config.Index(mb.Address); i >= 0 && i != oldIdx {
		return "", fmt.Errorf("mailbox %s is already configured", mb.Address)
	}

	var old Mailbox
	if oldIdx >= 0 {
		old = s.Config.Mailboxes[oldIdx]
	}
	addressChanged := oldIdx >= 0 && normalize(old.Address) != normalize(mb.Address)

	secret := newSecret
	switch {
	case secret == "" && oldIdx < 0:
		return "", errors.New("client secret is required")
	case secret == "" && !addressChanged:
		// Keep the secret where it is.
		mb.ClientSecret = old.ClientSecret
	case secret == "":
		// The keyring entry is keyed by address, so move it.
		if secret, err = s.Secret(old); err != nil {
			return "", err
		}
	}

	if secret != "" {
		if err := s.Keyring.Set(normalize(mb.Address), secret); err != nil {
			mb.ClientSecret = secret
			warning = fmt.Sprintf("no OS keyring available (%v); client secret stored in %s (mode 0600)", err, s.Path)
		}
	}

	next := *s.Config
	next.Mailboxes = append([]Mailbox(nil), s.Config.Mailboxes...)
	if oldIdx >= 0 {
		next.Mailboxes[oldIdx] = mb
	} else {
		next.Mailboxes = append(next.Mailboxes, mb)
	}
	if err := Save(s.Path, &next); err != nil {
		return "", err
	}
	s.Config = &next

	if addressChanged {
		_ = s.Keyring.Delete(normalize(old.Address))
	}
	return warning, nil
}

// Remove deletes a mailbox from the config and its secret from the keyring.
func (s *Store) Remove(address string) error {
	idx := s.Config.Index(address)
	if idx < 0 {
		return fmt.Errorf("mailbox %s is not configured", address)
	}
	next := *s.Config
	next.Mailboxes = append(append([]Mailbox(nil), s.Config.Mailboxes[:idx]...), s.Config.Mailboxes[idx+1:]...)
	if err := Save(s.Path, &next); err != nil {
		return err
	}
	s.Config = &next
	_ = s.Keyring.Delete(normalize(address))
	return nil
}
