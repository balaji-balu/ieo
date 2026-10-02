package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/contract"
)

// maxBody bounds a request body. A capabilities report or status is a few kilobytes.
const maxBody = 1 << 20

// putCapabilities stores a device's capabilities (SPEC §8.3, §11.1).
func (s *server) putCapabilities(w http.ResponseWriter, r *http.Request) {
	site := siteOf(r)
	id, ok := s.deviceID(w, r)
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	caps, fields, err := contract.DecodeDeviceCapabilities(body)
	if err != nil {
		badBody(w, r, fields, err)
		return
	}
	created, err := s.reporter.ReportCapabilities(r.Context(), site, id, caps)
	switch {
	case errors.Is(err, deploy.ErrNotAuthorized):
		notAuthorized(w, r)
	case errors.Is(err, deploy.ErrGatewayNotFound):
		problem(w, r, http.StatusNotFound, contract.ProblemGatewayNotFound, "Gateway Not Found",
			"report the gateway "+string(site)+" before its hosts")
	case errors.Is(err, deploy.ErrInvalidRequest):
		invalidField(w, r, err)
	case err != nil:
		s.internalError(w, r, site, err)
	case created:
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// deleteCapabilities unregisters a host (SPEC §8.3, §11.1).
func (s *server) deleteCapabilities(w http.ResponseWriter, r *http.Request) {
	site := siteOf(r)
	id, ok := s.deviceID(w, r)
	if !ok {
		return
	}
	err := s.reporter.RemoveDevice(r.Context(), site, id)
	switch {
	case errors.Is(err, deploy.ErrNotAuthorized):
		notAuthorized(w, r)
	case errors.Is(err, deploy.ErrInvalidRequest):
		invalidField(w, r, err)
	case errors.Is(err, deploy.ErrNotFound):
		problem(w, r, http.StatusNotFound, contract.ProblemDeviceNotFound, "Device Not Found", "no such device")
	case err != nil:
		s.internalError(w, r, site, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// status records a deployment status (SPEC §8.1.2, §11.1). The Margo file lists no 404 for this
// operation, so a deployment that is not the caller's is a semantic error.
func (s *server) status(w http.ResponseWriter, r *http.Request) {
	site := siteOf(r)
	id, err := uuid.Parse(r.PathValue("deploymentId"))
	if err != nil || id.String() != r.PathValue("deploymentId") {
		semanticError(w, r, contract.FieldError{Field: "deploymentId", Message: "not a deployment ID in canonical form"})
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	st, fields, err := contract.DecodeDeploymentStatus(body)
	if err != nil {
		badBody(w, r, fields, err)
		return
	}
	created, err := s.reporter.ReportStatus(r.Context(), site, id, st)
	switch {
	case errors.Is(err, deploy.ErrInvalidRequest):
		invalidField(w, r, err)
	case err != nil:
		s.internalError(w, r, site, err)
	case created:
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// deviceID parses the path's device ID, or answers 422.
func (s *server) deviceID(w http.ResponseWriter, r *http.Request) (contract.DeviceID, bool) {
	id, err := contract.ParseDeviceID(r.PathValue("deviceId"))
	if err != nil {
		semanticError(w, r, contract.FieldError{Field: "deviceId", Message: err.Error()})
		return contract.DeviceID{}, false
	}
	return id, true
}

// readBody reads at most maxBody bytes of the request body, or answers 400 or 413.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		problem(w, r, http.StatusRequestEntityTooLarge, contract.ProblemAboutBlank, "", "")
		return nil, false
	case err != nil:
		problem(w, r, http.StatusBadRequest, contract.ProblemInvalidRequest, "Invalid Request", "unreadable request body")
		return nil, false
	}
	return b, true
}

// badBody answers a body that failed decoding: 400 for malformed JSON, 422 otherwise (SPEC
// §11.1).
func badBody(w http.ResponseWriter, r *http.Request, fields []contract.FieldError, err error) {
	if errors.Is(err, contract.ErrMalformed) {
		problem(w, r, http.StatusBadRequest, contract.ProblemInvalidRequest, "Invalid Request", "Malformed request body.")
		return
	}
	semanticError(w, r, fields...)
}

func semanticError(w http.ResponseWriter, r *http.Request, fields ...contract.FieldError) {
	writeProblem(w, contract.Problem{
		Type: contract.ProblemSemanticError, Title: "Semantic Error", Status: http.StatusUnprocessableEntity,
		Detail: "Request body includes a semantic error.", Instance: r.URL.Path, Errors: fields,
	})
}

// invalidField answers a deploy.InvalidField with 422, naming only the field and the rule, not
// the internal error chain.
func invalidField(w http.ResponseWriter, r *http.Request, err error) {
	var f *deploy.InvalidField
	if !errors.As(err, &f) {
		semanticError(w, r, contract.FieldError{Message: "invalid request"})
		return
	}
	semanticError(w, r, contract.FieldError{Field: f.Field, Message: f.Message})
}

func notAuthorized(w http.ResponseWriter, r *http.Request) {
	problem(w, r, http.StatusForbidden, contract.ProblemNotAuthorized, "Not Authorized",
		"the device belongs to another site")
}
