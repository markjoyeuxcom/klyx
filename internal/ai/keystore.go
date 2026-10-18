package ai

import (
	"errors"

	"github.com/moomora/klyx/internal/secretstore"
)

// KeychainService is the OS credential-store service Klyx's AI keys live
// under; each provider is one account ("ai/<provider>").
const KeychainService = secretstore.Service

// ErrNoKey: the credential store holds no key for the provider.
var ErrNoKey = errors.New("no API key stored")

// KeychainError: the credential store could not be read or written (locked,
// unavailable, access denied). It is never "no key": a provider whose key
// cannot be read fails closed with this reason (spec §2.1). It is the shared
// store's error (coverage C5 §3).
type KeychainError = secretstore.KeychainError

// KeyStore holds API keys out of fleet.yaml. Tests fake it.
type KeyStore interface {
	Get(p ProviderID) (string, error)
	Set(p ProviderID, key string) error
	Delete(p ProviderID) error
}

// OSKeyStore is the OS credential store, through the shared secretstore.
type OSKeyStore struct{}

func keychainAccount(p ProviderID) string { return "ai/" + string(p) }

func (OSKeyStore) Get(p ProviderID) (string, error) {
	key, err := secretstore.OS{}.Get(keychainAccount(p))
	if errors.Is(err, secretstore.ErrNoKey) {
		return "", ErrNoKey
	}
	return key, err
}

func (OSKeyStore) Set(p ProviderID, key string) error {
	if key == "" {
		return errors.New("refusing to store an empty key")
	}
	return secretstore.OS{}.Set(keychainAccount(p), key)
}

func (OSKeyStore) Delete(p ProviderID) error { return secretstore.OS{}.Delete(keychainAccount(p)) }
