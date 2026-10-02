package auth_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"

	"github.com/balaji-balu/ieo/internal/co/auth"
	"github.com/balaji-balu/ieo/internal/contract"
)

// fakeStore records every hash it is given, so a test can check what the CO keeps.
type fakeStore struct {
	sites map[contract.SiteID]auth.TokenHash
	puts  []auth.TokenHash
}

func newFakeStore(sites ...contract.SiteID) *fakeStore {
	f := &fakeStore{sites: map[contract.SiteID]auth.TokenHash{}}
	for _, s := range sites {
		f.sites[s] = auth.TokenHash{}
	}
	return f
}

func (f *fakeStore) PutSiteToken(_ context.Context, site contract.SiteID, hash auth.TokenHash) (bool, error) {
	if _, ok := f.sites[site]; !ok {
		return false, nil
	}
	f.sites[site] = hash
	f.puts = append(f.puts, hash)
	return true, nil
}

func (f *fakeStore) SiteByToken(_ context.Context, hash auth.TokenHash) (contract.SiteID, bool, error) {
	for s, h := range f.sites {
		if h == hash && h != (auth.TokenHash{}) {
			return s, true, nil
		}
	}
	return "", false, nil
}

// SPEC §15.6: the CO stores only the SHA-256 of a token generated from 256 random bits, and maps a
// token to its site.
func TestIssueStoresOnlyHashAndVerifyMapsTokenToSite(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore("site-1", "site-2")
	tokens := auth.New(store)

	t1, err := tokens.Issue(ctx, "site-1")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	t2, err := tokens.Issue(ctx, "site-2")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if t1 == t2 {
		t.Fatal("two sites got the same token")
	}
	// 256 bits are at least 43 characters in any text encoding of 6 bits or less per character.
	if len(t1) < 43 {
		t.Errorf("token %d characters long, want at least 43 (256 random bits)", len(t1))
	}
	if len(store.puts) != 2 || store.puts[0] != sha256.Sum256([]byte(t1)) {
		t.Errorf("store got %x, want only the SHA-256 of each token", store.puts)
	}

	for token, want := range map[string]contract.SiteID{t1: "site-1", t2: "site-2"} {
		got, err := tokens.Verify(ctx, token)
		if err != nil || got != want {
			t.Errorf("Verify = %q, %v; want %q", got, err, want)
		}
	}
}

func TestVerifyRejectsUnknownAndEmptyTokens(t *testing.T) {
	ctx := context.Background()
	tokens := auth.New(newFakeStore("site-1"))
	token, err := tokens.Issue(ctx, "site-1")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	for _, bad := range []string{"", "x", token + "x", strings.ToUpper(token)} {
		if bad == token {
			continue
		}
		if site, err := tokens.Verify(ctx, bad); !errors.Is(err, auth.ErrUnknownToken) {
			t.Errorf("Verify(%q) = %q, %v; want ErrUnknownToken", bad, site, err)
		}
	}
}

func TestIssueReplacesOldToken(t *testing.T) {
	ctx := context.Background()
	tokens := auth.New(newFakeStore("site-1"))
	old, err := tokens.Issue(ctx, "site-1")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := tokens.Issue(ctx, "site-1"); err != nil {
		t.Fatalf("issue again: %v", err)
	}
	if _, err := tokens.Verify(ctx, old); !errors.Is(err, auth.ErrUnknownToken) {
		t.Errorf("old token: err %v, want ErrUnknownToken", err)
	}
}

func TestIssueForUnknownSite(t *testing.T) {
	_, err := auth.New(newFakeStore()).Issue(context.Background(), "site-9")
	if !errors.Is(err, auth.ErrUnknownSite) {
		t.Errorf("err %v, want ErrUnknownSite", err)
	}
}
