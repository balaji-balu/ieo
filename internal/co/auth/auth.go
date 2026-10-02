// Package auth issues and verifies the interim per-site bearer tokens of the Margo API (SPEC §15.6,
// ADR 0005). Appendix B step 4 replaces it with mutual TLS.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/balaji-balu/ieo/internal/contract"
)

var (
	// ErrUnknownToken means the token is empty or no site has it.
	ErrUnknownToken = errors.New("unknown token")
	// ErrUnknownSite means there is no such site to issue a token for.
	ErrUnknownSite = errors.New("unknown site")
)

// TokenHash is the SHA-256 of a token, the only form the CO stores.
type TokenHash [sha256.Size]byte

// Store keeps each site's token hash.
type Store interface {
	// PutSiteToken replaces the token hash of site, and reports false if there is no such site.
	PutSiteToken(ctx context.Context, site contract.SiteID, hash TokenHash) (bool, error)
	// SiteByToken returns the site whose token has hash, and whether there is one.
	SiteByToken(ctx context.Context, hash TokenHash) (contract.SiteID, bool, error)
}

// Tokens issues and verifies site tokens.
type Tokens struct {
	store Store
}

// New returns Tokens that keep their hashes in store.
func New(store Store) *Tokens {
	return &Tokens{store: store}
}

// Issue returns a new token for site, replacing its old one. The token is 256 random bits,
// base64url-encoded; only its SHA-256 is stored (SPEC §15.6).
func (t *Tokens) Issue(ctx context.Context, site contract.SiteID) (string, error) {
	var b [32]byte
	_, _ = rand.Read(b[:]) // never fails (crypto/rand, Go 1.24)
	token := base64.RawURLEncoding.EncodeToString(b[:])
	ok, err := t.store.PutSiteToken(ctx, site, sha256.Sum256([]byte(token)))
	if err != nil {
		return "", fmt.Errorf("issue token for site %s: %w", site, err)
	}
	if !ok {
		return "", fmt.Errorf("issue token for site %s: %w", site, ErrUnknownSite)
	}
	return token, nil
}

// Verify returns the site of token, or ErrUnknownToken. Tokens are looked up by hash, so the
// lookup time does not depend on how much of a guess matches a real token.
func (t *Tokens) Verify(ctx context.Context, token string) (contract.SiteID, error) {
	if token == "" {
		return "", ErrUnknownToken
	}
	site, ok, err := t.store.SiteByToken(ctx, sha256.Sum256([]byte(token)))
	if err != nil {
		return "", fmt.Errorf("verify token: %w", err)
	}
	if !ok {
		return "", ErrUnknownToken
	}
	return site, nil
}
