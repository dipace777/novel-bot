package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func validName(name string) bool {
	return utf8.ValidString(name) && utf8.RuneCountInString(name) >= 1 && utf8.RuneCountInString(name) <= 100
}

func validID(id string) bool {
	if len(id) != 32 || id != strings.ToLower(id) {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func parseTokenWithPrefix(token, prefix string) (string, bool) {
	if len(token) != len(prefix)+32+1+43 || !strings.HasPrefix(token, prefix) {
		return "", false
	}
	id, secret, ok := strings.Cut(strings.TrimPrefix(token, prefix), ".")
	if !ok || !validID(id) {
		return "", false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(secret)
	return id, err == nil && len(b) == 32
}
