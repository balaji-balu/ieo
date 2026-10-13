package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

// target is one repository as the puller reads it. It adds one thing to the registry client it
// wraps: its errors are rebuilt, since the client's own quote request URLs and what a server sent
// (ADR 0021).
type target struct {
	repo *remote.Repository
	// registry is the registry as an error names it; host is where its requests go. They differ
	// for Docker Hub only.
	registry, host string
}

func (t *target) Resolve(ctx context.Context, reference string) (ocispec.Descriptor, error) {
	desc, err := t.repo.Resolve(ctx, reference)
	return desc, t.failure("resolve", err)
}

func (t *target) Fetch(ctx context.Context, desc ocispec.Descriptor) (io.ReadCloser, error) {
	body, err := t.repo.Fetch(ctx, desc)
	if err != nil {
		return nil, t.failure("fetch", err)
	}
	return &content{ReadCloser: body, t: t}, nil
}

// content is what a Fetch returned, with the errors of reading it rebuilt as the others are.
type content struct {
	io.ReadCloser
	t *target
}

func (c *content) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	switch {
	case err == nil:
		return n, nil
	case errors.Is(err, io.EOF):
		return n, io.EOF // as it is: a reader's callers compare with it
	}
	return n, c.t.failure("read", err)
}

func (t *target) Exists(ctx context.Context, desc ocispec.Descriptor) (bool, error) {
	exists, err := t.repo.Exists(ctx, desc)
	return exists, t.failure("look up", err)
}

// failure is the error of one operation on a repository. Its text is built from what this package
// knows to be free of secrets: hosts, an HTTP status, error codes and a network failure.
type failure struct {
	registry string
	op       string
	detail   string
	// answered is the host that failed the operation when it is not the registry: a token
	// service or the target of a redirect.
	answered string
	// kind is what errors.Is finds: a context error, errdef.ErrNotFound or
	// io.ErrUnexpectedEOF; nil otherwise. The
	// registry client's error is not kept, as it could then be printed.
	kind error
}

func (f *failure) Error() string {
	text := fmt.Sprintf("registry %s: %s: %s", f.registry, f.op, f.detail)
	if f.answered != "" {
		text += " (from " + f.answered + ")"
	}
	return text
}

// Unwrap returns the kind of failure, if it has one.
func (f *failure) Unwrap() error { return f.kind }

// failure returns err, an error of the registry client, as a *failure; nil for nil.
func (t *target) failure(op string, err error) error {
	if err == nil {
		return nil
	}
	f := &failure{registry: t.registry, op: op}
	var refused *errcode.ErrorResponse
	var unsent *url.Error
	switch {
	case errors.As(err, &refused):
		if refused.URL != nil {
			f.answered = refused.URL.Host
		}
	case errors.As(err, &unsent):
		if u, perr := url.Parse(unsent.URL); perr == nil {
			f.answered = u.Host
		}
	}
	if f.answered == t.host {
		f.answered = ""
	}

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		f.kind, f.detail = context.DeadlineExceeded, context.DeadlineExceeded.Error()
	case errors.Is(err, context.Canceled):
		f.kind, f.detail = context.Canceled, context.Canceled.Error()
	case refused != nil:
		f.detail = statusText(refused.StatusCode) + errorCodes(refused.Errors)
	case errors.Is(err, errdef.ErrNotFound):
		f.kind, f.detail = errdef.ErrNotFound, statusText(http.StatusNotFound)
	case errors.Is(err, auth.ErrBasicCredentialNotFound):
		f.detail = statusText(http.StatusUnauthorized) + ": the registry asks for a credential and none is configured for it"
	case errors.Is(err, io.ErrUnexpectedEOF):
		f.kind, f.detail = io.ErrUnexpectedEOF, "the answer ended early"
	case unsent != nil:
		// What Go's HTTP client or this package says of a request with no answer: a dial, TLS
		// or proxy failure, or a refused redirect. It quotes no URL.
		f.detail = truncate(unsent.Err.Error(), maxDetailBytes)
	default:
		f.detail = "the registry's answer is not one of a registry"
	}
	return f
}

func statusText(status int) string {
	return fmt.Sprintf("%d %s", status, http.StatusText(status))
}

// The bounds of what a server decides in an error's text.
const (
	maxDetailBytes = 200
	maxErrorCodes  = 4
	maxCodeBytes   = 64
)

// errorCodes returns the registry's error codes (`MANIFEST_UNKNOWN`), in brackets, and nothing if
// there is none. A code is taken only if it looks like one: a server chooses it, as it does the
// message that is left out.
func errorCodes(errs errcode.Errors) string {
	var codes []string
	for _, e := range errs {
		if len(codes) < maxErrorCodes && isErrorCode(e.Code) {
			codes = append(codes, e.Code)
		}
	}
	if len(codes) == 0 {
		return ""
	}
	return " [" + strings.Join(codes, ", ") + "]"
}

func isErrorCode(code string) bool {
	if code == "" || len(code) > maxCodeBytes {
		return false
	}
	for _, r := range code {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
