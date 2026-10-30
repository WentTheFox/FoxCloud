package secret

import (
	"context"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/zalando/go-keyring"
)

// Service is the keyring service name all FoxCloud secrets are filed under.
const Service = "foxcloud"

// CryptKey is the single master secret the crypt password and salt are
// derived from, so there is only one thing to back up.
const CryptKey = "crypt-key"

// Names lists every secret FoxCloud manages.
var Names = []string{CryptKey}

const keyringPrefix = "keyring:"

// ErrNotFound means the named secret is not in the keyring.
var ErrNotFound = keyring.ErrNotFound

// Get reads a secret from the OS keyring (Windows Credential Manager,
// macOS Keychain, or the Secret Service on Linux).
func Get(name string) (string, error) { return keyring.Get(Service, name) }

// Set stores a secret in the OS keyring.
func Set(name, value string) error { return keyring.Set(Service, name, value) }

// Generate returns a new random secret with 256 bits of entropy.
func Generate() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// GenerateAll creates every managed secret in the keyring. It refuses to run
// if any already exists: overwriting the crypt key makes existing data
// unreadable.
func GenerateAll() error {
	for _, n := range Names {
		if _, err := Get(n); err == nil {
			return fmt.Errorf("%s already exists; refusing to overwrite it", n)
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	for _, n := range Names {
		v, err := Generate()
		if err != nil {
			return err
		}
		if err := Set(n, v); err != nil {
			return err
		}
	}
	return nil
}

// Derive turns the master key into the crypt password and salt. The labels
// make the two outputs independent, and the derivation is fixed: changing it
// would make existing data unreadable.
func Derive(master string) (password, salt string, err error) {
	if master == "" {
		return "", "", errors.New("empty crypt key")
	}
	derive := func(info string) (string, error) {
		b, err := hkdf.Key(sha256.New, []byte(master), nil, info, 32)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(b), nil
	}
	if password, err = derive("foxcloud crypt password v1"); err != nil {
		return "", "", err
	}
	if salt, err = derive("foxcloud crypt salt v1"); err != nil {
		return "", "", err
	}
	return password, salt, nil
}

// Resolve returns a secret from spec, which is either "keyring:<name>" or a
// shell command printing the secret. An empty spec yields an empty secret.
func Resolve(ctx context.Context, spec string) (string, error) {
	if name, ok := strings.CutPrefix(strings.TrimSpace(spec), keyringPrefix); ok {
		v, err := Get(name)
		if errors.Is(err, ErrNotFound) {
			return "", fmt.Errorf("%s is not in the keyring; run `foxcloud secret generate`", name)
		}
		return v, err
	}
	return FromCommand(ctx, spec)
}
