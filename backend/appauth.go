package main

import (
	"context"
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

// ─── Installation tokens ─────────────────────────────────────────────────────

const (
	authTypePAT = "pat"
	authTypeApp = "app"

	// installationTokenSkew is how long before GitHub's expiry a cached
	// installation token stops being served, so a token handed to a caller
	// (or to a git clone) never dies mid-use.
	installationTokenSkew = 5 * time.Minute
)

func installationTokenCacheKey(installationRowID string) string {
	return "installation-token:" + installationRowID
}

type cachedInstallationToken struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

// installationToken returns a valid installation access token for the
// installation row, minting one with the App's private key on a cache miss.
// The cache is only an optimisation: entries are encrypted at rest and a miss
// (or an unavailable cache backend) just mints again.
func (p *githubPlugin) installationToken(installationRowID string) (string, time.Time, error) {
	cacheKey := installationTokenCacheKey(installationRowID)
	if p.cache != nil {
		if enc, hit := p.cache.Get(cacheKey); hit && enc != "" {
			if raw, err := p.decrypt(enc); err == nil {
				var c cachedInstallationToken
				if json.Unmarshal([]byte(raw), &c) == nil && c.Token != "" {
					if exp := time.Unix(c.ExpiresAt, 0); time.Until(exp) > installationTokenSkew {
						return c.Token, exp, nil
					}
				}
			}
		}
	}

	inst, err := p.db.Query(
		`SELECT app_id, installation_id, suspended_at FROM github_app_installations WHERE id = $1`,
		installationRowID,
	)
	if err != nil {
		return "", time.Time{}, err
	}
	if len(inst.Rows) == 0 {
		return "", time.Time{}, &appError{code: "GITHUB_INSTALLATION_NOT_FOUND", status: 404, msg: "GitHub App installation not found"}
	}
	isc := newRowScanner(inst.Columns, inst.Rows[0])
	if isc.strPtr("suspended_at") != nil {
		return "", time.Time{}, &appError{code: "GITHUB_INSTALLATION_SUSPENDED", status: 422, msg: "GitHub App installation is suspended"}
	}
	ghInstallationID := isc.int64Val("installation_id")

	jwt, err := p.appJWT(isc.str("app_id"))
	if err != nil {
		return "", time.Time{}, err
	}

	var minted struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", ghBaseURL, ghInstallationID)
	if err := newGHClient(jwt).post(context.Background(), url, map[string]any{}, &minted); err != nil {
		return "", time.Time{}, err
	}
	if minted.Token == "" {
		return "", time.Time{}, errors.New("github app: installation token response had no token")
	}

	if p.cache != nil {
		payload, _ := json.Marshal(cachedInstallationToken{Token: minted.Token, ExpiresAt: minted.ExpiresAt.Unix()})
		if enc, eErr := p.encrypt(string(payload)); eErr == nil {
			if ttl := time.Until(minted.ExpiresAt) - installationTokenSkew; ttl > 0 {
				p.cache.Set(cacheKey, enc, ttl)
			}
		}
	}
	return minted.Token, minted.ExpiresAt, nil
}

// appJWT signs an App JWT for the app row with the given id.
func (p *githubPlugin) appJWT(appRowID string) (string, error) {
	app, err := p.db.Query(`SELECT github_app_id, private_key_enc FROM github_apps WHERE id = $1`, appRowID)
	if err != nil {
		return "", err
	}
	if len(app.Rows) == 0 {
		return "", &appError{code: "GITHUB_APP_NOT_FOUND", status: 404, msg: "GitHub App not found"}
	}
	asc := newRowScanner(app.Columns, app.Rows[0])
	pemStr, err := p.decrypt(asc.str("private_key_enc"))
	if err != nil {
		return "", err
	}
	return signAppJWT(asc.int64Val("github_app_id"), pemStr, time.Now())
}

// resolveCredential returns the project's default credential.
func (p *githubPlugin) resolveCredential(projectID string) (string, time.Time, error) {
	return p.resolveCredentialFor(projectID, "")
}

// resolveCredentialFor returns the token a call made by accountID should use:
// the stored PAT, or an installation token — the account's own installation
// when the project maps one for it, otherwise the project's default. The
// second value is the token's expiry (zero for a PAT).
//
// A mapping that is no longer usable (installation suspended, or withdrawn
// from the project) fails closed instead of falling back to the default: a
// silent fallback would act on GitHub as a different identity than the one
// the project configured, which is exactly what the mapping exists to prevent.
func (p *githubPlugin) resolveCredentialFor(projectID, accountID string) (string, time.Time, error) {
	result, err := p.db.Query(
		`SELECT auth_type, access_token_enc, installation_id FROM github_integrations WHERE project_id = $1`,
		projectID,
	)
	if err != nil {
		return "", time.Time{}, err
	}
	if len(result.Rows) == 0 {
		return "", time.Time{}, &appError{code: "GITHUB_INTEGRATION_NOT_FOUND", status: 404, msg: "GitHub integration not found"}
	}
	sc := newRowScanner(result.Columns, result.Rows[0])
	if sc.str("auth_type") != authTypeApp {
		token, err := p.decrypt(sc.str("access_token_enc"))
		return token, time.Time{}, err
	}

	installationRowID := sc.str("installation_id")
	if accountID != "" {
		mapped, err := p.db.Query(
			`SELECT installation_id FROM github_agent_identities WHERE project_id = $1 AND account_id = $2`,
			projectID, accountID)
		if err != nil {
			return "", time.Time{}, err
		}
		if len(mapped.Rows) > 0 {
			mappedID := newRowScanner(mapped.Columns, mapped.Rows[0]).str("installation_id")
			allowed, err := p.projectMayUseInstallation(projectID, mappedID)
			if err != nil {
				return "", time.Time{}, err
			}
			if !allowed {
				return "", time.Time{}, &appError{code: "GITHUB_AGENT_IDENTITY_UNAVAILABLE", status: 422,
					msg: "The GitHub identity mapped to this account is suspended or no longer available to the project"}
			}
			installationRowID = mappedID
		}
	}
	if installationRowID == "" {
		return "", time.Time{}, &appError{code: "GITHUB_INTEGRATION_NOT_FOUND", status: 404, msg: "GitHub integration not found"}
	}
	return p.installationToken(installationRowID)
}
