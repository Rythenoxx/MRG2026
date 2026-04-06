package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256" // Added for hashing
	"encoding/base64"
	"fmt"
	"io"
)

// EncryptPayload encrypts text using AES-GCM
func EncryptPayload(plainText string, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plainText), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// DecryptPayload decrypts text using AES-GCM
func DecryptPayload(cryptoText string, key []byte) (string, error) {
	data, err := base64.StdEncoding.DecodeString(cryptoText)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	out, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// GenerateSessionKey creates a random 32-byte key
func GenerateSessionKey() []byte {
	key := make([]byte, 32)
	rand.Read(key)
	return key
}

// GetSmartID generates an 8-char anonymous hash of the hostname
func GetSmartID(hostname string) string {
	hash := sha256.Sum256([]byte(hostname + "MARENGO_SALT_2026"))
	return fmt.Sprintf("%x", hash)[:8]
}
