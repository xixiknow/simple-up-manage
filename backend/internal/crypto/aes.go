package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

type AESGCM struct {
	gcm cipher.AEAD
}

func New(keySpec string) (*AESGCM, error) {
	key, err := parseKey(keySpec)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("aes gcm: %w", err)
	}
	return &AESGCM{gcm: gcm}, nil
}

func parseKey(spec string) ([]byte, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("encrypt key is empty")
	}
	if len(spec) == 64 && isHex(spec) {
		b, err := hex.DecodeString(spec)
		if err != nil {
			return nil, fmt.Errorf("decode hex encrypt key: %w", err)
		}
		return b, nil
	}
	if len(spec) == 32 {
		return []byte(spec), nil
	}
	if decoded, err := hex.DecodeString(spec); err == nil && (len(decoded) == 16 || len(decoded) == 24 || len(decoded) == 32) {
		return decoded, nil
	}
	return nil, fmt.Errorf("APP_ENCRYPT_KEY must be 32 raw bytes or 64 hex chars")
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func (a *AESGCM) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", fmt.Errorf("plaintext is empty")
	}
	nonce := make([]byte, a.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := a.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func (a *AESGCM) Decrypt(ciphertext string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}
	ns := a.gcm.NonceSize()
	if len(raw) < ns {
		return "", fmt.Errorf("ciphertext too short")
	}
	plain, err := a.gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plain), nil
}

func KeyPreview(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if len(raw) <= 8 {
		return raw[:1] + "..."
	}
	prefix := raw
	if len(prefix) > 6 {
		prefix = raw[:6]
	}
	return prefix + "..." + raw[len(raw)-4:]
}
