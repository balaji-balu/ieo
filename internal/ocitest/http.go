package ocitest

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/errdef"
)

// Handler serves the registry over HTTP, read-only, as the OCI distribution API does: for a test
// that needs a registry client's transport, which Open leaves out. A test starts it on 127.0.0.1
// (G-F4).
//
// A request to host H for `/v2/<name>/…` reads the repository `H/<name>`, so one handler serves
// every registry host whose repositories were pushed. It serves `/v2/`, and GET and HEAD of
// `/v2/<name>/manifests/<tag or digest>` and `/v2/<name>/blobs/<digest>`. It asks for no
// credential: a test puts that in front. Tamper, Stall and BytesServed apply as they do to Open.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(r.serve)
}

func (r *Registry) serve(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "the registry is read-only")
		return
	}
	path, ok := strings.CutPrefix(req.URL.Path, "/v2/")
	if !ok {
		http.NotFound(w, req)
		return
	}
	if path == "" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}") // a test's client sees a failed write as a failed request
		return
	}
	name, kind, reference, ok := splitPath(path)
	if !ok {
		http.NotFound(w, req)
		return
	}
	repository := req.Host + "/" + name
	opened, err := r.Open(req.Context(), repository)
	if err != nil {
		writeError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
		return
	}
	repo := opened.(*target)
	desc, found := r.describe(req, repo, kind, reference)
	if !found {
		code := "BLOB_UNKNOWN"
		if kind == "manifests" {
			code = "MANIFEST_UNKNOWN"
		}
		writeError(w, http.StatusNotFound, code, "not known to registry")
		return
	}
	body, err := repo.Fetch(req.Context(), desc)
	if err != nil {
		return // stalled until the request ended: there is no one to answer
	}
	defer func() { _ = body.Close() }() // an in-memory reader: nothing to lose
	w.Header().Set("Content-Type", desc.MediaType)
	w.Header().Set("Docker-Content-Digest", desc.Digest.String())
	if req.Method == http.MethodHead {
		w.Header().Set("Content-Length", strconv.FormatInt(desc.Size, 10))
		return
	}
	// Read first: a tampered blob need not have the size its descriptor gives.
	data, err := io.ReadAll(body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UNKNOWN", "read failed")
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data) // a test's client sees a failed write as a failed request
}

// splitPath splits `<name>/manifests/<reference>` or `<name>/blobs/<digest>`.
func splitPath(path string) (name, kind, reference string, ok bool) {
	for _, k := range []string{"manifests", "blobs"} {
		if i := strings.LastIndex(path, "/"+k+"/"); i > 0 {
			name, reference = path[:i], path[i+len(k)+2:]
			return name, k, reference, reference != "" && !strings.Contains(reference, "/")
		}
	}
	return "", "", "", false
}

// describe returns the descriptor of what a request names: a pushed blob or manifest by its
// digest, or a manifest by its tag.
func (r *Registry) describe(req *http.Request, t *target, kind, reference string) (ocispec.Descriptor, bool) {
	r.mu.Lock()
	desc, ok := r.pushed[blobKey{t.repository, reference}]
	r.mu.Unlock()
	if ok || kind != "manifests" {
		return desc, ok
	}
	desc, err := t.Resolve(req.Context(), reference)
	if err != nil && !errors.Is(err, errdef.ErrNotFound) {
		return ocispec.Descriptor{}, false // stalled until the request ended
	}
	return desc, err == nil
}

// writeError answers with one error in the distribution API's form.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{ // a test's client sees a failed write as a failed request
		"errors": []map[string]string{{"code": code, "message": message}},
	})
}
