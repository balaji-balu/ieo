// Package httpapi holds what the CO's two HTTP APIs, the Margo API (SPEC §11.1) and the operator
// API (SPEC §11.3), do the same way: RFC 9457 problems, 405 and 500 answers, and reading a
// bounded request body.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/balaji-balu/ieo/internal/contract"
)

// Problem writes an RFC 9457 problem whose instance is the request path. An empty title is the
// status text, as about:blank requires.
func Problem(w http.ResponseWriter, r *http.Request, status int, typ, title, detail string) {
	if title == "" {
		title = http.StatusText(status)
	}
	WriteProblem(w, contract.Problem{Type: typ, Title: title, Status: status, Detail: detail, Instance: r.URL.Path})
}

// WriteProblem writes p as an RFC 9457 problem.
func WriteProblem(w http.ResponseWriter, p contract.Problem) {
	b, err := json.Marshal(p)
	if err != nil { // cannot happen: every field is a string or int
		b = []byte(`{"type":"about:blank","title":"Internal Server Error","status":500}`)
	}
	w.Header().Set("Content-Type", contract.ProblemMediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(p.Status)
	_, _ = w.Write(b)
}

// MethodNotAllowed answers 405 with the Allow header set to allow.
func MethodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		Problem(w, r, http.StatusMethodNotAllowed, contract.ProblemAboutBlank, "", "")
	}
}

// InternalError logs err with msg, the method, the path and attrs, and answers 500 without detail,
// so internals don't reach the caller. It never logs request headers (SPEC §15.4).
func InternalError(w http.ResponseWriter, r *http.Request, log *slog.Logger, msg string, err error, attrs ...any) {
	log.ErrorContext(r.Context(), msg, append([]any{"method", r.Method, "path", r.URL.Path, "error", err}, attrs...)...)
	Problem(w, r, http.StatusInternalServerError, contract.ProblemAboutBlank, "", "")
}

// ReadBody reads at most limit bytes of the request body. On failure it returns the status to
// answer, which the caller writes: 413 for a larger body, 400 for an unreadable one.
func ReadBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, int) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		return nil, http.StatusRequestEntityTooLarge
	case err != nil:
		return nil, http.StatusBadRequest
	}
	return b, 0
}
