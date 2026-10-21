// Package coapi is the LO's client of the CO's Margo API (SPEC §11.1). It holds the rules every LO
// request follows (SPEC §15.6): the site token goes in the Authorization header and nowhere else,
// no redirect is followed, and no error holds the token or the CO's URL.
//
// The transport is the caller's: it decides TLS and proxies (SPEC §15.6).
package coapi

import (
	"bytes"
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

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
)

// Client makes the LO's requests to the CO. It is safe for concurrent use.
type Client struct {
	base  string // the CO URL without a trailing slash
	token string
	http  *http.Client
}

// NewClient returns a Client for the CO at coURL that sends token. A nil transport means
// http.DefaultTransport.
func NewClient(coURL, token string, transport http.RoundTripper) *Client {
	return &Client{
		base:  strings.TrimSuffix(coURL, "/"),
		token: token,
		http: &http.Client{
			Transport: transport,
			// SPEC §15.6: a redirect is returned as the response, which the caller then fails.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// RequestError is a request that did not get the response its caller needs: a transport error, or
// an unexpected status with its problem, if the body is one (RFC 9457). It holds the method and
// path, never the CO's URL or the token.
type RequestError struct {
	Method string
	Path   string
	// Status is the HTTP status; 0 for a transport error.
	Status int
	// Problem is the response's problem; its Type is empty when the body is none.
	Problem contract.Problem
	// Err is the transport error; nil when the CO answered.
	Err error
}

func (e *RequestError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s %s: %v", e.Method, e.Path, e.Err)
	}
	if e.Problem.Type != "" {
		return fmt.Sprintf("%s %s: status %d: %s (%s)", e.Method, e.Path, e.Status, e.Problem.Title, e.Problem.Type)
	}
	return fmt.Sprintf("%s %s: status %d", e.Method, e.Path, e.Status)
}

func (e *RequestError) Unwrap() error { return e.Err }

// Attrs are the log fields of the failure: its reason, and the status and problem type when the
// CO answered.
func (e *RequestError) Attrs() []slog.Attr {
	attrs := []slog.Attr{slog.String("reason", e.Error())}
	if e.Status != 0 {
		attrs = append(attrs, slog.Int("status", e.Status))
	}
	if e.Problem.Type != "" {
		attrs = append(attrs, slog.String("problem_type", e.Problem.Type))
	}
	return attrs
}

// ManifestPath is the path of the site's State Manifest (SPEC §11.1).
const ManifestPath = "/api/v1/deployments"

// ManifestResponse is the answer to GET /api/v1/deployments.
type ManifestResponse struct {
	NotModified bool
	Body        []byte
	ETag        string
}

// Manifest gets the site's State Manifest, conditionally on etag when it is not empty (SPEC §8.2
// steps 1–2). Any status other than 200 and 304 is a *RequestError.
func (c *Client) Manifest(ctx context.Context, etag string) (ManifestResponse, *RequestError) {
	h := http.Header{"Accept": {contract.ManifestMediaType}}
	if etag != "" {
		h.Set("If-None-Match", etag)
	}
	resp, err := c.do(ctx, http.MethodGet, ManifestPath, h, nil)
	if err != nil {
		return ManifestResponse{}, err
	}
	switch resp.status {
	case http.StatusNotModified:
		return ManifestResponse{NotModified: true}, nil
	case http.StatusOK:
		return ManifestResponse{Body: resp.body, ETag: resp.header.Get("ETag")}, nil
	default:
		return ManifestResponse{}, failed(http.MethodGet, ManifestPath, resp)
	}
}

// Content gets a deployment YAML or a bundle by its path (SPEC §8.2 step 4). Any status other
// than 200 is a *RequestError; a 404 means only that the digest is unavailable.
func (c *Client) Content(ctx context.Context, p string) ([]byte, *RequestError) {
	resp, err := c.do(ctx, http.MethodGet, p, http.Header{}, nil)
	if err != nil {
		return nil, err
	}
	if resp.status != http.StatusOK {
		return nil, failed(http.MethodGet, p, resp)
	}
	return resp.body, nil
}

// StatusPath is the path a deployment's status is sent to (SPEC §11.1).
func StatusPath(id uuid.UUID) string { return "/api/v1/deployments/" + id.String() + "/status" }

// PostStatus sends s to the CO (SPEC §8.8). Only 200 and 201 are success; any other status is a
// *RequestError.
func (c *Client) PostStatus(ctx context.Context, s contract.DeploymentStatus) *RequestError {
	p := StatusPath(s.DeploymentID)
	body, err := json.Marshal(s)
	if err != nil {
		return &RequestError{Method: http.MethodPost, Path: p, Err: err}
	}
	resp, rerr := c.do(ctx, http.MethodPost, p, http.Header{"Content-Type": {"application/json"}}, body)
	if rerr != nil {
		return rerr
	}
	if resp.status != http.StatusOK && resp.status != http.StatusCreated {
		return failed(http.MethodPost, p, resp)
	}
	return nil
}

// response is a response read in full.
type response struct {
	status int
	header http.Header
	body   []byte
}

// do sends method p with header, body and the token, and reads the whole response body. The body
// is read without a limit: SPEC §8.2 sets none (balaji-balu/ieo#52).
func (c *Client) do(ctx context.Context, method, p string, header http.Header, body []byte) (response, *RequestError) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+p, r)
	if err != nil {
		return response{}, &RequestError{Method: method, Path: p, Err: err}
	}
	req.Header = header
	req.Header.Set("Authorization", "Bearer "+c.token) // SPEC §15.6; never logged (§15.4)
	resp, err := c.http.Do(req)
	if err != nil {
		// Only the path is reported: neither it nor the transport error holds the token.
		return response{}, &RequestError{Method: method, Path: p, Err: unwrapURLError(err)}
	}
	defer func() { _ = resp.Body.Close() }() // the body is read in full; a close error changes nothing
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return response{}, &RequestError{Method: method, Path: p, Err: err}
	}
	return response{status: resp.StatusCode, header: resp.Header, body: b}, nil
}

// unwrapURLError drops the *url.Error wrapper, whose message repeats the method and URL that
// RequestError already gives.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// failed returns the *RequestError for an unexpected response, with its problem if the body is
// one.
func failed(method, p string, resp response) *RequestError {
	e := &RequestError{Method: method, Path: p, Status: resp.status}
	if mt, _, err := mime.ParseMediaType(resp.header.Get("Content-Type")); err == nil && mt == contract.ProblemMediaType {
		// A problem that does not decode leaves only the status, which is enough to log.
		_ = json.Unmarshal(resp.body, &e.Problem)
	}
	return e
}
