package main

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// ─── App JWT ─────────────────────────────────────────────────────────────────

// parseRSAPrivateKey accepts the PEM GitHub hands out (PKCS#1, "RSA PRIVATE
// KEY") as well as PKCS#8, which keys re-exported by other tools often use.
func parseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("github app: private key is not valid PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("github app: private key is neither PKCS#1 nor PKCS#8")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("github app: private key is not an RSA key")
	}
	return key, nil
}

// signAppJWT builds the short-lived RS256 JWT GitHub requires to authenticate
// as the App itself. iat is backdated a minute to absorb clock drift; GitHub
// caps exp at 10 minutes.
func signAppJWT(githubAppID int64, pemStr string, now time.Time) (string, error) {
	key, err := parseRSAPrivateKey(pemStr)
	if err != nil {
		return "", err
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": strconv.FormatInt(githubAppID, 10),
	})
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(nil, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("github app: sign jwt: %w", err)
	}
	return signingInput + "." + enc.EncodeToString(sig), nil
}
