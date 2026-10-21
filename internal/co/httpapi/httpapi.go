// Package httpapi holds what the CO's two HTTP APIs, the Margo API (SPEC §11.1) and the operator
// API (SPEC §11.3), write the same way: RFC 9457 problems and 405 answers.
package httpapi

import (
	"encoding/json"
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
