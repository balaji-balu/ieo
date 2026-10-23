package edgectl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"

	"github.com/balaji-balu/ieo/internal/bearer"
)

// client sends edgectl's requests to the CO's operator API (SPEC §11.3). bearer holds the token,
// TLS, proxy and redirect rules (SPEC §15.6).
type client struct {
	api *bearer.Client
}

// newClient returns the client of cfg. With an http:// CO URL it first writes the SPEC §15.6
// warning to stderr, naming the URL and never the token.
func newClient(cfg config, stderr io.Writer) (*client, error) {
	transport, err := bearer.Transport(cfg.CAFile)
	if err != nil {
		return nil, usageError(fmt.Sprintf("%s: %v", keyCAFile, err))
	}
	if bearer.IsHTTP(cfg.COURL) {
		_, _ = fmt.Fprintf(stderr, "edgectl: warning: %s is set: the token is sent to %s without TLS\n",
			keyCOInsecure.spec, cfg.COURL)
	}
	return &client{api: bearer.New(cfg.COURL, cfg.Token, transport)}, nil
}

// get sends GET p and returns the body of a 200 response.
func (c *client) get(ctx context.Context, p string) ([]byte, error) {
	resp, rerr := c.api.Get(ctx, p, http.Header{"Accept": {"application/json"}})
	return c.body(http.MethodGet, p, resp, rerr, http.StatusOK)
}

// post sends POST p with v as JSON and returns the body of a response with one of the ok statuses.
func (c *client) post(ctx context.Context, p string, v any, ok ...int) ([]byte, error) {
	resp, rerr := c.api.PostJSON(ctx, p, v)
	return c.body(http.MethodPost, p, resp, rerr, ok...)
}

// patch sends PATCH p with v as JSON and returns the body of a 200 response.
func (c *client) patch(ctx context.Context, p string, v any) ([]byte, error) {
	resp, rerr := c.api.PatchJSON(ctx, p, v)
	return c.body(http.MethodPatch, p, resp, rerr, http.StatusOK)
}

// body returns resp's body if its status is one of ok, and a failure otherwise.
func (c *client) body(method, p string, resp bearer.Response, rerr *bearer.RequestError, ok ...int) ([]byte, error) {
	if rerr == nil {
		if slices.Contains(ok, resp.Status) {
			return resp.Body, nil
		}
		rerr = c.api.Failed(method, p, resp) // its problem holds no token (SPEC §15.6)
	}
	return nil, failureOf(rerr)
}

// failure is a failed request, or a response edgectl cannot read: exit code 1 (SPEC §11.3).
type failure struct {
	msg string
	err error // the transport error, for errors.Is
}

func (f *failure) Error() string { return f.msg }
func (f *failure) Unwrap() error { return f.err }

// failureOf returns the failure of e. A problem gives its title and detail (SPEC §11.3); any other
// answer its status. A redirect is not followed (SPEC §15.6), and its target is not printed.
func failureOf(e *bearer.RequestError) *failure {
	switch {
	case e.Err != nil:
		return &failure{msg: e.Error(), err: e.Err}
	case e.Problem.Title != "" && e.Problem.Detail != "":
		return &failure{msg: e.Problem.Title + ": " + e.Problem.Detail}
	case e.Problem.Title != "":
		return &failure{msg: e.Problem.Title}
	}
	msg := fmt.Sprintf("%s %s: status %d %s", e.Method, e.Path, e.Status, http.StatusText(e.Status))
	if e.Status >= 300 && e.Status < 400 {
		msg += ": the CO answered with a redirect, which edgectl does not follow"
	}
	return &failure{msg: msg}
}

// isFailure reports whether err is a failure.
func isFailure(err error) bool {
	var f *failure
	return errors.As(err, &f)
}
