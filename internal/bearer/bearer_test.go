package bearer_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/balaji-balu/ieo/internal/bearer"
)

// Get takes a nil header, and leaves the caller's header without the token (SPEC §15.4).
func TestGetHeader(t *testing.T) {
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
	}))
	defer srv.Close()
	b := make([]byte, 32)
	_, _ = rand.Read(b)            // never fails (crypto/rand)
	token := hex.EncodeToString(b) // generated: no token in fixtures (SPEC §15.6)
	c := bearer.New(srv.URL, token, nil)

	if _, err := c.Get(context.Background(), "/x", nil); err != nil {
		t.Fatalf("Get with a nil header: %v", err)
	}
	h := http.Header{"Accept": {"application/json"}}
	if _, err := c.Get(context.Background(), "/x", h); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(h) != 1 || h.Get("Authorization") != "" {
		t.Errorf("the caller's header became %v", h)
	}
	for _, a := range auth {
		if a != "Bearer "+token {
			t.Error("a request did not carry the token")
		}
	}
}
