package credentials

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/zalando/go-keyring"
)

const (
	keyringService = "toggl-automations"
	keyringUser    = "api-token"
)

var ErrNotFound = errors.New("credential not found")

type Source string

const (
	SourceEnvironment Source = "environment"
	SourceKeyring     Source = "system keyring"
	SourceFile        Source = "secure config file"
)

type Keyring interface {
	Get(service, user string) (string, error)
	Set(service, user, secret string) error
	Delete(service, user string) error
}

type Store struct {
	keyring Keyring
	file    fileStore
}

func NewDefault() (*Store, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("locate user config directory: %w", err)
	}
	return New(systemKeyring{}, filepath.Join(configDir, "toggl-automations", "token")), nil
}

func New(keyring Keyring, fallbackPath string) *Store {
	return &Store{keyring: keyring, file: fileStore{path: fallbackPath}}
}

func (store *Store) Load() (string, Source, error) {
	secret, fileErr := store.file.load()
	if fileErr == nil {
		return secret, SourceFile, nil
	}
	if !errors.Is(fileErr, ErrNotFound) {
		return "", "", fileErr
	}

	secret, keyringErr := store.keyring.Get(keyringService, keyringUser)
	if keyringErr == nil {
		if err := validate(secret); err != nil {
			return "", "", fmt.Errorf("invalid token in system keyring: %w", err)
		}
		return secret, SourceKeyring, nil
	}
	if errors.Is(keyringErr, ErrNotFound) {
		return "", "", ErrNotFound
	}
	return "", "", fmt.Errorf("access system keyring: %w", keyringErr)
}

func (store *Store) Save(secret string) (Source, error) {
	secret = strings.TrimSpace(secret)
	if err := validate(secret); err != nil {
		return "", err
	}

	if err := store.keyring.Set(keyringService, keyringUser, secret); err == nil {
		if err := store.file.delete(); err != nil && !errors.Is(err, ErrNotFound) {
			return SourceKeyring, fmt.Errorf("remove obsolete fallback token: %w", err)
		}
		return SourceKeyring, nil
	}

	if err := store.file.save(secret); err != nil {
		return "", fmt.Errorf("save fallback token: %w", err)
	}
	return SourceFile, nil
}

func (store *Store) Delete() (bool, error) {
	removed := false
	var failures []error

	if err := store.keyring.Delete(keyringService, keyringUser); err == nil {
		removed = true
	} else if !errors.Is(err, ErrNotFound) {
		failures = append(failures, fmt.Errorf("delete token from system keyring: %w", err))
	}

	if err := store.file.delete(); err == nil {
		removed = true
	} else if !errors.Is(err, ErrNotFound) {
		failures = append(failures, err)
	}

	return removed, errors.Join(failures...)
}

func validate(secret string) error {
	if secret == "" {
		return errors.New("token must not be empty")
	}
	if strings.IndexFunc(secret, unicode.IsSpace) >= 0 {
		return errors.New("token must not contain whitespace")
	}
	return nil
}

type systemKeyring struct{}

func (systemKeyring) Get(service, user string) (string, error) {
	secret, err := keyring.Get(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return secret, err
}

func (systemKeyring) Set(service, user, secret string) error {
	return keyring.Set(service, user, secret)
}

func (systemKeyring) Delete(service, user string) error {
	err := keyring.Delete(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

type fileStore struct {
	path string
}

func (store fileStore) load() (string, error) {
	info, err := os.Lstat(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("inspect fallback token: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("fallback token must be a regular file, not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("fallback token permissions are %04o; expected 0600", info.Mode().Perm())
	}

	data, err := os.ReadFile(store.path)
	if err != nil {
		return "", fmt.Errorf("read fallback token: %w", err)
	}
	secret := strings.TrimSpace(string(data))
	if err := validate(secret); err != nil {
		return "", fmt.Errorf("invalid fallback token: %w", err)
	}
	return secret, nil
}

func (store fileStore) save(secret string) error {
	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect config directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("config directory must be a directory, not a symlink")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("secure config directory permissions: %w", err)
	}

	file, err := os.CreateTemp(directory, ".token-*")
	if err != nil {
		return fmt.Errorf("create temporary fallback token: %w", err)
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)

	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure fallback token permissions: %w", err)
	}
	if _, err := fmt.Fprintln(file, secret); err != nil {
		_ = file.Close()
		return fmt.Errorf("write fallback token: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync fallback token: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close fallback token: %w", err)
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return fmt.Errorf("replace fallback token: %w", err)
	}
	return nil
}

func (store fileStore) delete() error {
	err := os.Remove(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("delete fallback token: %w", err)
	}
	return nil
}
