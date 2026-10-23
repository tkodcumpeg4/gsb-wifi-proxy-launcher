package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"sync"
)

// On Android the 32-byte data key comes from the app in GSB_SECRET_KEY (hex).
// The app keeps it wrapped by an Android Keystore key (KeyVault.kt), so it is
// never stored in the clear; config.json holds only AES-GCM ciphertext.

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
	key, err := hex.DecodeString(os.Getenv("GSB_SECRET_KEY"))
	if err != nil || len(key) != 32 {
		return nil, errors.New("GSB_SECRET_KEY eksik veya geçersiz")
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
