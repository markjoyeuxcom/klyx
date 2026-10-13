package ai

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// KeychainService is the OS credential-store service Klyx's AI keys live
// under; each provider is one account ("ai/<provider>").
const KeychainService = "io.moomora.klyx"

// ErrNoKey: the credential store holds no key for the provider.
var ErrNoKey = errors.New("no API key stored")

// KeychainError: the credential store could not be read or written (locked,
// unavailable, access denied). It is never "no key": a provider whose key
// cannot be read fails closed with this reason (spec §2.1).
type KeychainError struct{ Err error }

func (e *KeychainError) Error() string { return "keychain unavailable: " + e.Err.Error() }
func (e *KeychainError) Unwrap() error { return e.Err }

// KeyStore holds API keys out of fleet.yaml. Tests fake it.
type KeyStore interface {
	Get(p ProviderID) (string, error)
	Set(p ProviderID, key string) error
	Delete(p ProviderID) error
}

// OSKeyStore is the OS credential store: the macOS Keychain, the Windows
// Credential Manager, or the Linux Secret Service. On macOS the write goes to
// /usr/bin/security over stdin, never argv.
type OSKeyStore struct{}

func keychainAccount(p ProviderID) string { return "ai/" + string(p) }

func (OSKeyStore) Get(p ProviderID) (string, error) {
	key, err := keyring.Get(KeychainService, keychainAccount(p))
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		return "", ErrNoKey
	case err != nil:
		return "", &KeychainError{Err: err}
	case key == "":
		return "", ErrNoKey
	}
	return key, nil
}

func (OSKeyStore) Set(p ProviderID, key string) error {
	if key == "" {
		return fmt.Errorf("refusing to store an empty key")
	}
	if err := keyring.Set(KeychainService, keychainAccount(p), key); err != nil {
		return &KeychainError{Err: err}
	}
	return nil
}

func (OSKeyStore) Delete(p ProviderID) error {
	err := keyring.Delete(KeychainService, keychainAccount(p))
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return &KeychainError{Err: err}
	}
	return nil
}
