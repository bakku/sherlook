package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type memKeyring struct {
	secrets map[string]string
	broken  bool
}

func newMemKeyring() *memKeyring { return &memKeyring{secrets: map[string]string{}} }

func (k *memKeyring) Get(account string) (string, error) {
	if k.broken {
		return "", errors.New("no secret service")
	}
	s, ok := k.secrets[account]
	if !ok {
		return "", ErrSecretNotFound
	}
	return s, nil
}

func (k *memKeyring) Set(account, secret string) error {
	if k.broken {
		return errors.New("no secret service")
	}
	k.secrets[account] = secret
	return nil
}

func (k *memKeyring) Delete(account string) error {
	if k.broken {
		return errors.New("no secret service")
	}
	if _, ok := k.secrets[account]; !ok {
		return ErrSecretNotFound
	}
	delete(k.secrets, account)
	return nil
}

func newStore(t *testing.T, kr Keyring) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sherlook", "config.toml")
	s, warnings, err := Open(path, kr)
	if err != nil || len(warnings) > 0 {
		t.Fatalf("open: %v %v", err, warnings)
	}
	return s
}

func mailbox(addr string) Mailbox {
	return Mailbox{Address: addr, Label: "Support", TenantID: "tenant", ClientID: "client"}
}

func TestLoadMissingFile(t *testing.T) {
	cfg, warnings, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil || len(warnings) != 0 || len(cfg.Mailboxes) != 0 || cfg.Version != CurrentVersion {
		t.Fatalf("cfg=%+v warnings=%v err=%v", cfg, warnings, err)
	}
}

func TestUpsertStoresSecretInKeyring(t *testing.T) {
	kr := newMemKeyring()
	s := newStore(t, kr)

	warning, err := s.Upsert("", mailbox("Support@Contoso.com"), "s3cret")
	if err != nil || warning != "" {
		t.Fatalf("upsert: %v %q", err, warning)
	}
	if kr.secrets["support@contoso.com"] != "s3cret" {
		t.Errorf("keyring = %v", kr.secrets)
	}

	raw, _ := os.ReadFile(s.Path)
	if strings.Contains(string(raw), "s3cret") {
		t.Errorf("secret leaked into config file:\n%s", raw)
	}
	info, _ := os.Stat(s.Path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o", info.Mode().Perm())
	}

	// Round trip.
	reloaded, warnings, err := Open(s.Path, kr)
	if err != nil || len(warnings) != 0 {
		t.Fatal(err, warnings)
	}
	if got := reloaded.Config.Mailboxes; len(got) != 1 || got[0].Address != "Support@Contoso.com" || got[0].Label != "Support" {
		t.Errorf("reloaded = %+v", got)
	}
	if secret, err := reloaded.Secret(got0(reloaded)); err != nil || secret != "s3cret" {
		t.Errorf("secret = %q, %v", secret, err)
	}
}

func got0(s *Store) Mailbox { return s.Config.Mailboxes[0] }

func TestUpsertFallsBackToFile(t *testing.T) {
	kr := newMemKeyring()
	kr.broken = true
	s := newStore(t, kr)

	warning, err := s.Upsert("", mailbox("a@b.c"), "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warning, "no OS keyring") {
		t.Errorf("warning = %q", warning)
	}
	raw, _ := os.ReadFile(s.Path)
	if !strings.Contains(string(raw), `client_secret = "s3cret"`) {
		t.Errorf("config:\n%s", raw)
	}
	if !s.SecretInFile(got0(s)) {
		t.Error("expected SecretInFile")
	}
	if secret, err := s.Secret(got0(s)); err != nil || secret != "s3cret" {
		t.Errorf("secret = %q, %v", secret, err)
	}
}

func TestUpsertValidation(t *testing.T) {
	s := newStore(t, newMemKeyring())
	if _, err := s.Upsert("", mailbox("a@b.c"), ""); err == nil {
		t.Error("expected secret to be required on add")
	}
	if _, err := s.Upsert("", mailbox("not-an-address"), "x"); err == nil {
		t.Error("expected invalid address error")
	}
	if _, err := s.Upsert("", Mailbox{Address: "a@b.c", ClientID: "c"}, "x"); err == nil {
		t.Error("expected tenant ID to be required")
	}
	if _, err := s.Upsert("", mailbox("a@b.c"), "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upsert("", mailbox("A@B.C"), "y"); err == nil {
		t.Error("expected duplicate address (case-insensitive) to be rejected")
	}
}

func TestUpsertEditKeepsSecret(t *testing.T) {
	kr := newMemKeyring()
	s := newStore(t, kr)
	if _, err := s.Upsert("", mailbox("a@b.c"), "s3cret"); err != nil {
		t.Fatal(err)
	}

	edited := mailbox("a@b.c")
	edited.Label = "Renamed"
	if _, err := s.Upsert("a@b.c", edited, ""); err != nil {
		t.Fatal(err)
	}
	if got0(s).Label != "Renamed" || kr.secrets["a@b.c"] != "s3cret" {
		t.Errorf("mailbox=%+v keyring=%v", got0(s), kr.secrets)
	}

	// Changing the address moves the keyring entry.
	if _, err := s.Upsert("a@b.c", mailbox("new@b.c"), ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := kr.secrets["a@b.c"]; ok || kr.secrets["new@b.c"] != "s3cret" {
		t.Errorf("keyring = %v", kr.secrets)
	}

	// Setting a new secret replaces it.
	if _, err := s.Upsert("new@b.c", mailbox("new@b.c"), "other"); err != nil {
		t.Fatal(err)
	}
	if kr.secrets["new@b.c"] != "other" {
		t.Errorf("keyring = %v", kr.secrets)
	}
}

func TestRemoveDeletesSecret(t *testing.T) {
	kr := newMemKeyring()
	s := newStore(t, kr)
	for _, addr := range []string{"a@b.c", "d@e.f"} {
		if _, err := s.Upsert("", mailbox(addr), "secret-"+addr); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Remove("A@B.C"); err != nil {
		t.Fatal(err)
	}
	if len(s.Config.Mailboxes) != 1 || got0(s).Address != "d@e.f" {
		t.Errorf("mailboxes = %+v", s.Config.Mailboxes)
	}
	if _, ok := kr.secrets["a@b.c"]; ok {
		t.Errorf("keyring still has removed secret: %v", kr.secrets)
	}
}

func TestLoadWarnsOnBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("version = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(path, 0o644)
	_, warnings, err := Load(path)
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "chmod 600") {
		t.Errorf("warnings = %v, err = %v", warnings, err)
	}
}

func TestLoadRejectsNewerVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	_ = os.WriteFile(path, []byte("version = 2\n"), 0o600)
	if _, _, err := Load(path); err == nil || !strings.Contains(err.Error(), "upgrade") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadRejectsDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `version = 1
[[mailbox]]
address = "a@b.c"
tenant_id = "t"
client_id = "c"
[[mailbox]]
address = "A@b.c"
tenant_id = "t"
client_id = "c"
`
	_ = os.WriteFile(path, []byte(content), 0o600)
	if _, _, err := Load(path); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Errorf("err = %v", err)
	}
}
