package bearer_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// PatchJSON sends PATCH with the body as JSON and the token, and follows no redirect (SPEC §11.3,
// §15.6): a 303 would otherwise turn it into a GET of wherever the server names.
func TestPatchJSON(t *testing.T) {
	type got struct{ method, path, contentType, auth, body string }
	var reqs []got
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		reqs = append(reqs, got{r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Authorization"), string(b)})
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/elsewhere", http.StatusSeeOther)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	c := bearer.New(srv.URL, token, nil)

	resp, err := c.PatchJSON(context.Background(), "/x", map[string]string{"version": "2.0.0"})
	if err != nil || resp.Status != http.StatusOK {
		t.Fatalf("PatchJSON: %+v, %v", resp, err)
	}
	want := got{http.MethodPatch, "/x", "application/json", "Bearer " + token, `{"version":"2.0.0"}`}
	if len(reqs) != 1 || reqs[0] != want {
		t.Errorf("requests = %+v, want [%+v]", reqs, want)
	}

	reqs = nil
	resp, err = c.PatchJSON(context.Background(), "/redirect", map[string]string{})
	if err != nil || resp.Status != http.StatusSeeOther {
		t.Errorf("PatchJSON to a redirect: %+v, %v; want the 303 itself", resp, err)
	}
	if len(reqs) != 1 {
		t.Errorf("%d requests, want 1: the redirect is not followed", len(reqs))
	}
}

// SPEC §15.6: no error holds the token, even when the server repeats it in a problem.
func TestFailedLeavesOutTheToken(t *testing.T) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	c := bearer.New("https://co.invalid", token, nil)
	body := `{"type":"about:blank#` + token + `","title":"Bad ` + token + `","status":400,"detail":"got Bearer ` + token + `","instance":"/` + token + `"}`
	e := c.Failed(http.MethodGet, "/x", bearer.Response{
		Status: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/problem+json"}}, Body: []byte(body),
	})
	p := e.Problem
	if s := e.Error() + p.Type + p.Title + p.Detail + p.Instance; strings.Contains(s, token) {
		t.Errorf("the error holds the token: %q", s)
	}
	if p.Detail != "got Bearer [token]" {
		t.Errorf("detail %q, want the rest of the server's text", p.Detail)
	}
}
