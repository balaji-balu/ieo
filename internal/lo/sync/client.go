package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/balaji-balu/ieo/internal/contract"
)

// client makes the LO's requests to the CO's Margo API (SPEC §11.1). It sends the site token in
// the Authorization header and nowhere else, and follows no redirects (SPEC §15.6).
type client struct {
	base  string // COURL without a trailing slash
	token string
	http  *http.Client
}

func newClient(coURL, token string, transport http.RoundTripper) *client {
	return &client{
		base:  strings.TrimSuffix(coURL, "/"),
		token: token,
		http: &http.Client{
			Transport: transport, // nil: http.DefaultTransport
			// SPEC §15.6: a redirect is returned as the response, which is then Unreachable.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// requestError is a request that did not get the response the sync tick needs: a transport error,
// or an unexpected status with its problem, if the body is one (RFC 9457).
type requestError struct {
	path    string
	status  int // 0 for a transport error
	problem contract.Problem
	err     error // the transport error
}

func (e *requestError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("GET %s: %v", e.path, e.err)
	}
	if e.problem.Type != "" {
		return fmt.Sprintf("GET %s: status %d: %s (%s)", e.path, e.status, e.problem.Title, e.problem.Type)
	}
	return fmt.Sprintf("GET %s: status %d", e.path, e.status)
}

// attrs are the log fields of the failure.
func (e *requestError) attrs() []slog.Attr {
	attrs := []slog.Attr{slog.String("reason", e.Error())}
	if e.status != 0 {
		attrs = append(attrs, slog.Int("status", e.status))
	}
	if e.problem.Type != "" {
		attrs = append(attrs, slog.String("problem_type", e.problem.Type))
	}
	return attrs
}

const manifestPath = "/api/v1/deployments" // SPEC §11.1

// manifestResponse is the answer to GET /api/v1/deployments.
type manifestResponse struct {
	notModified bool
	body        []byte
	etag        string
}

// manifest gets the site's State Manifest, conditionally on etag when it is not empty (SPEC §8.2
// steps 1–2). Any status other than 200 and 304 is a *requestError.
func (c *client) manifest(ctx context.Context, etag string) (manifestResponse, *requestError) {
	h := http.Header{"Accept": {contract.ManifestMediaType}}
	if etag != "" {
		h.Set("If-None-Match", etag)
	}
	resp, err := c.get(ctx, manifestPath, h)
	if err != nil {
		return manifestResponse{}, err
	}
	switch resp.status {
	case http.StatusNotModified:
		return manifestResponse{notModified: true}, nil
	case http.StatusOK:
		return manifestResponse{body: resp.body, etag: resp.header.Get("ETag")}, nil
	default:
		return manifestResponse{}, failed(manifestPath, resp)
	}
}

// content gets a deployment YAML or a bundle by its path (SPEC §8.2 step 4). Any status other
// than 200 is a *requestError; a 404 means only that the digest is unavailable.
func (c *client) content(ctx context.Context, p string) ([]byte, *requestError) {
	resp, err := c.get(ctx, p, http.Header{})
	if err != nil {
		return nil, err
	}
	if resp.status != http.StatusOK {
		return nil, failed(p, resp)
	}
	return resp.body, nil
}

// response is a response read in full.
type response struct {
	status int
	header http.Header
	body   []byte
}

// get sends GET p with header and the token, and reads the whole body. The body is read without a
// limit: SPEC §8.2 sets none (balaji-balu/ieo#52).
func (c *client) get(ctx context.Context, p string, header http.Header) (response, *requestError) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+p, nil)
	if err != nil {
		return response{}, &requestError{path: p, err: err}
	}
	req.Header = header
	req.Header.Set("Authorization", "Bearer "+c.token) // SPEC §15.6; never logged (§15.4)
	resp, err := c.http.Do(req)
	if err != nil {
		// Only the path is reported: neither it nor the transport error holds the token.
		return response{}, &requestError{path: p, err: unwrapURLError(err)}
	}
	defer func() { _ = resp.Body.Close() }() // the body is read in full; a close error changes nothing
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return response{}, &requestError{path: p, err: err}
	}
	return response{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

// unwrapURLError drops the *url.Error wrapper, whose message repeats the method and URL that
// requestError already gives.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// failed returns the *requestError for an unexpected response, with its problem if the body is
// one.
func failed(p string, resp response) *requestError {
	e := &requestError{path: p, status: resp.status}
	if mt, _, err := mime.ParseMediaType(resp.header.Get("Content-Type")); err == nil && mt == contract.ProblemMediaType {
		// A problem that does not decode leaves only the status, which is enough to log.
		_ = json.Unmarshal(resp.body, &e.problem)
	}
	return e
}
