package store

import (
	"errors"
	"fmt"
	"sync"

	"github.com/zalando/go-keyring"
)

// KeyringService is the service name of every CPA-Bar entry in the system
// credential store; the user is the instance ID.
const KeyringService = "com.github.licoy.cpabar"

// ErrSecretNotFound means no key is stored for the instance.
var ErrSecretNotFound = errors.New("no management key stored for this instance")

// SecretStore keeps management keys. Implementations never fall back to
// plain text storage.
type SecretStore interface {
	Get(instanceID string) (string, error)
	Set(instanceID, key string) error
	Delete(instanceID string) error
}

// Keyring stores keys in the macOS Keychain, the Windows Credential Manager
// or the Secret Service on Linux.
type Keyring struct{}

func (Keyring) Get(id string) (string, error) {
	v, err := keyring.Get(KeyringService, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrSecretNotFound
	}
	if err != nil {
		return "", fmt.Errorf("system credential store unavailable: %w", err)
	}
	return v, nil
}

func (Keyring) Set(id, key string) error {
	if err := keyring.Set(KeyringService, id, key); err != nil {
		return fmt.Errorf("system credential store unavailable: %w", err)
	}
	return nil
}

func (Keyring) Delete(id string) error {
	err := keyring.Delete(KeyringService, id)
	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return fmt.Errorf("system credential store unavailable: %w", err)
}

// MemorySecrets is an in-memory SecretStore, for tests.
type MemorySecrets struct {
	mu sync.Mutex
	m  map[string]string
	// Err, when set, is returned by every call, as an unavailable store.
	Err error
}

func (s *MemorySecrets) Get(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return "", s.Err
	}
	v, ok := s.m[id]
	if !ok {
		return "", ErrSecretNotFound
	}
	return v, nil
}

func (s *MemorySecrets) Set(id, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[id] = key
	return nil
}

func (s *MemorySecrets) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	delete(s.m, id)
	return nil
}
