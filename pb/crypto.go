package pb

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"os"
)

// tokenEncryptionEnv is the env var holding the passphrase used to encrypt
// per-user OAuth tokens at rest. Any string works; it is hashed to a 32-byte key.
const tokenEncryptionEnv = "TOKEN_ENCRYPTION_KEY"

// errNoEncryptionKey is returned when OAuth token storage is attempted without a
// configured key. OAuth tokens are sensitive, so we refuse to store them in the
// clear.
var errNoEncryptionKey = errors.New(tokenEncryptionEnv + " is not set; it is required to store OAuth tokens")

func tokenCipher() (cipher.AEAD, error) {
	secret := os.Getenv(tokenEncryptionEnv)
	if secret == "" {
		return nil, errNoEncryptionKey
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// encryptSecret returns base64(nonce||ciphertext) for the given plaintext. An
// empty string encrypts to an empty string.
func encryptSecret(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	aead, err := tokenCipher()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// decryptSecret reverses encryptSecret. An empty string decrypts to an empty string.
func decryptSecret(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	aead, err := tokenCipher()
	if err != nil {
		return "", err
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	if len(data) < aead.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := data[:aead.NonceSize()], data[aead.NonceSize():]
	pt, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
