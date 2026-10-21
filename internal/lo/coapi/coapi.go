// Package coapi makes the LO's requests to the CO's Margo API (SPEC §11.1). It holds the rules of
// SPEC §15.6 for every one of them: the site token goes in the Authorization header and nowhere
// else, no redirect is followed, and no error holds the token. What is asked for, and what an
// answer means, is the caller's: the sync loop's (SPEC §8.2) and the status outbox's (SPEC §8.8).
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

	"github.com/balaji-balu/ieo/internal/contract"
)

// Client sends requests to one CO with one site token. It is safe for concurrent use.
type Client struct {
	base  string // the CO URL without a trailing slash
	token string
	http  *http.Client
}

// New returns the Client for the CO at coURL. transport carries the requests; nil means
// http.DefaultTransport. The caller's transport decides the proxy and the certificates it trusts
// (SPEC §15.6).
func New(coURL, token string, transport http.RoundTripper) *Client {
	return &Client{
		base:  strings.TrimSuffix(coURL, "/"),
		token: token,
		http: &http.Client{
			Transport: transport, // nil: http.DefaultTransport
			// SPEC §15.6: a redirect is returned as the response, which the caller treats as failed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Response is a response read in full.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// RequestError is a request that did not get the response its caller needs: a transport error,
// or an unexpected status with its problem, if the body is one (RFC 9457). Its text holds the
// method and path, never the CO URL or the token.
type RequestError struct {
	Method  string
	Path    string
	Status  int // 0 for a transport error
	Problem contract.Problem
	Err     error // the transport error
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

// Attrs are the log fields of the failure.
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

// Get sends GET p with header, which may be nil and is not changed, and the token, and reads the
// whole body. Only a transport error is a
// *RequestError: every status is returned as a Response. The body is read without a limit: SPEC
// §8.2 sets none (balaji-balu/ieo#52).
func (c *Client) Get(ctx context.Context, p string, header http.Header) (Response, *RequestError) {
	return c.do(ctx, http.MethodGet, p, header, nil)
}

// PostJSON sends POST p with body encoded as JSON and the token, and reads the whole response
// body. As with Get, only a transport error, or a body that does not encode, is a *RequestError.
func (c *Client) PostJSON(ctx context.Context, p string, body any) (Response, *RequestError) {
	b, err := json.Marshal(body)
	if err != nil {
		return Response{}, &RequestError{Method: http.MethodPost, Path: p, Err: err}
	}
	return c.do(ctx, http.MethodPost, p, http.Header{"Content-Type": {"application/json"}}, b)
}

func (c *Client) do(ctx context.Context, method, p string, header http.Header, body []byte) (Response, *RequestError) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+p, r)
	if err != nil {
		return Response{}, &RequestError{Method: method, Path: p, Err: err}
	}
	if header != nil {
		req.Header = header.Clone() // the token goes into the request's headers, not the caller's
	}
	req.Header.Set("Authorization", "Bearer "+c.token) // SPEC §15.6; never logged (§15.4)
	resp, err := c.http.Do(req)
	if err != nil {
		// Only the path is reported: neither it nor the transport error holds the token.
		return Response{}, &RequestError{Method: method, Path: p, Err: unwrapURLError(err)}
	}
	defer func() { _ = resp.Body.Close() }() // the body is read in full; a close error changes nothing
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, &RequestError{Method: method, Path: p, Err: err}
	}
	return Response{Status: resp.StatusCode, Header: resp.Header, Body: b}, nil
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

// Failed returns the *RequestError for resp, an unexpected response to method p, with its problem
// if the body is one.
func Failed(method, p string, resp Response) *RequestError {
	e := &RequestError{Method: method, Path: p, Status: resp.Status}
	if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err == nil && mt == contract.ProblemMediaType {
		// A problem that does not decode leaves only the status, which is enough to log.
		_ = json.Unmarshal(resp.Body, &e.Problem)
	}
	return e
}
