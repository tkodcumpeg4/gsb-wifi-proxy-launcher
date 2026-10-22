package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"sync"
)

// Linux has no DPAPI. Secrets are sealed with AES-256-GCM under a random key
// kept in <data>/secret.key (0600), so config.json alone — e.g. copied to
// another machine or pasted somewhere — does not reveal the passwords. Anyone
// who can read the user's files can still read the key; this is weaker than
// DPAPI, which ties the key to the Windows login.

var secretKey struct {
	sync.Mutex
	key []byte
}

func loadSecretKey() ([]byte, error) {
	secretKey.Lock()
	defer secretKey.Unlock()
	if secretKey.key != nil {
		return secretKey.key, nil
	}
	path := dataPath("secret.key")
	key, err := os.ReadFile(path)
	if err != nil || len(key) != 32 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, err
		}
	}
	secretKey.key = key
	return key, nil
}

func secretAEAD() (cipher.AEAD, error) {
	key, err := loadSecretKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func encryptSecret(plain string) (string, error) {
	aead, err := secretAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func decryptSecret(enc string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	aead, err := secretAEAD()
	if err != nil {
		return "", err
	}
	if len(raw) < aead.NonceSize() {
		return "", errors.New("şifreli değer kısa")
	}
	out, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
	return string(out), err
}
