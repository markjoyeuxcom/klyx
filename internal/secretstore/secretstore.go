// Package secretstore keeps Klyx's secrets out of fleet.yaml, in the OS
// credential store: the macOS Keychain, the Windows Credential Manager, or
// the Linux Secret Service (coverage C5 §3). Every secret is one account
// under one service; AI keys are "ai/<provider>", endpoint credentials
// "endpoint/<cluster>/<kind>".
package secretstore

import (
	"errors"
	"fmt"
	"sync"

	"github.com/zalando/go-keyring"
)

// Service is the OS credential-store service every Klyx secret lives under.
const Service = "io.moomora.klyx"

// ErrNoKey: the store holds nothing for the account.
var ErrNoKey = errors.New("no credential stored")

// KeychainError: the store could not be read or written (locked,
// unavailable, access denied). It is never "nothing stored": a reader whose
// secret cannot be read fails closed with this reason.
type KeychainError struct{ Err error }

func (e *KeychainError) Error() string { return "keychain unavailable: " + e.Err.Error() }
func (e *KeychainError) Unwrap() error { return e.Err }

// Store holds secrets by account. Tests use Memory.
type Store interface {
	Get(account string) (string, error)
	Set(account, value string) error
	Delete(account string) error
}

// OS is the OS credential store. On macOS the write goes to
// /usr/bin/security over stdin, never argv.
type OS struct{}

func (OS) Get(account string) (string, error) {
	v, err := keyring.Get(Service, account)
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		return "", ErrNoKey
	case err != nil:
		return "", &KeychainError{Err: err}
	case v == "":
		return "", ErrNoKey
	}
	return v, nil
}

func (OS) Set(account, value string) error {
	if value == "" {
		return fmt.Errorf("refusing to store an empty secret")
	}
	if err := keyring.Set(Service, account, value); err != nil {
		return &KeychainError{Err: err}
	}
	return nil
}

func (OS) Delete(account string) error {
	err := keyring.Delete(Service, account)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return &KeychainError{Err: err}
	}
	return nil
}

// Memory is an in-memory Store for tests, with injectable failures.
type Memory struct {
	mu                           sync.Mutex
	m                            map[string]string
	FailGet, FailSet, FailDelete error
}

func (s *Memory) Get(account string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FailGet != nil {
		return "", s.FailGet
	}
	v, ok := s.m[account]
	if !ok || v == "" {
		return "", ErrNoKey
	}
	return v, nil
}

func (s *Memory) Set(account, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FailSet != nil {
		return s.FailSet
	}
	if value == "" {
		return fmt.Errorf("refusing to store an empty secret")
	}
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[account] = value
	return nil
}

func (s *Memory) Delete(account string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FailDelete != nil {
		return s.FailDelete
	}
	delete(s.m, account)
	return nil
}
