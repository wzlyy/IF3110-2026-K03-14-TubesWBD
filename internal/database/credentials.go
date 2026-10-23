package database

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
)

type credentialCipher struct{ aead cipher.AEAD }

func newCredentialCipher(key string) (*credentialCipher, error) {
	if len(key) != 32 {
		return nil, errors.New("ENCRYPTION_KEY must be exactly 32 bytes")
	}
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return nil, errors.New("cannot initialize credential encryption")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("cannot initialize credential encryption")
	}
	return &credentialCipher{aead: aead}, nil
}

func (c *credentialCipher) encrypt(value string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", errors.New("cannot encrypt credentials")
	}
	return base64.StdEncoding.EncodeToString(c.aead.Seal(nonce, nonce, []byte(value), nil)), nil
}

func (c *credentialCipher) decrypt(value string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(data) < c.aead.NonceSize()+c.aead.Overhead() {
		return "", errors.New("cannot decrypt saved credentials; save credentials again")
	}
	n := c.aead.NonceSize()
	plain, err := c.aead.Open(nil, data[:n], data[n:], nil)
	if err != nil {
		return "", errors.New("cannot decrypt saved credentials; save credentials again")
	}
	return string(plain), nil
}
