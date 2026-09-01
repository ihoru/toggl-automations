package credentials

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeKeyring struct {
	secret    string
	getErr    error
	setErr    error
	deleteErr error
}

func (keyring *fakeKeyring) Get(_, _ string) (string, error) {
	if keyring.getErr != nil {
		return "", keyring.getErr
	}
	if keyring.secret == "" {
		return "", ErrNotFound
	}
	return keyring.secret, nil
}

func (keyring *fakeKeyring) Set(_, _, secret string) error {
	if keyring.setErr != nil {
		return keyring.setErr
	}
	keyring.secret = secret
	return nil
}

func (keyring *fakeKeyring) Delete(_, _ string) error {
	if keyring.deleteErr != nil {
		return keyring.deleteErr
	}
	if keyring.secret == "" {
		return ErrNotFound
	}
	keyring.secret = ""
	return nil
}

func TestStorePrefersExistingFallbackFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := New(&fakeKeyring{secret: "keyring-token"}, path)

	secret, source, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if secret != "file-token" || source != SourceFile {
		t.Fatalf("secret=%q source=%q", secret, source)
	}
}

func TestStoreUsesSystemKeyringWithoutFallbackFile(t *testing.T) {
	t.Parallel()

	store := New(&fakeKeyring{secret: "keyring-token"}, filepath.Join(t.TempDir(), "token"))

	secret, source, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if secret != "keyring-token" || source != SourceKeyring {
		t.Fatalf("secret=%q source=%q", secret, source)
	}
}

func TestStoreFallsBackToSecureFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config", "token")
	store := New(&fakeKeyring{getErr: errors.New("keyring unavailable"), setErr: errors.New("keyring unavailable")}, path)

	source, err := store.Save("file-token")
	if err != nil {
		t.Fatal(err)
	}
	if source != SourceFile {
		t.Fatalf("source=%q", source)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("permissions=%04o", permissions)
	}

	secret, source, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if secret != "file-token" || source != SourceFile {
		t.Fatalf("secret=%q source=%q", secret, source)
	}
}

func TestFallbackFileOverridesStaleKeyringTokenAfterSetFailure(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config", "token")
	keyring := &fakeKeyring{secret: "old-token", setErr: errors.New("keyring locked")}
	store := New(keyring, path)

	if source, err := store.Save("new-token"); err != nil || source != SourceFile {
		t.Fatalf("source=%q err=%v", source, err)
	}
	secret, source, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if secret != "new-token" || source != SourceFile {
		t.Fatalf("secret=%q source=%q", secret, source)
	}
}

func TestStoreRejectsInsecureFallbackPermissions(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("file-token\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := New(&fakeKeyring{}, path)

	_, _, err := store.Load()
	if err == nil || !contains(err.Error(), "expected 0600") {
		t.Fatalf("err=%v", err)
	}
}

func TestStoreDeletesKeyringAndFallback(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyring := &fakeKeyring{secret: "keyring-token"}
	store := New(keyring, path)

	removed, err := store.Delete()
	if err != nil {
		t.Fatal(err)
	}
	if !removed || keyring.secret != "" {
		t.Fatalf("removed=%v keyring secret=%q", removed, keyring.secret)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fallback file still exists: %v", err)
	}
}

func contains(value, substring string) bool {
	for index := 0; index+len(substring) <= len(value); index++ {
		if value[index:index+len(substring)] == substring {
			return true
		}
	}
	return false
}
