package sync

import (
	"context"
	"net/http"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/coapi"
)

// client makes the sync loop's requests to the CO's Margo API (SPEC §11.1); coapi holds the token
// and redirect rules (SPEC §15.6).
type client struct {
	api *coapi.Client
}

func newClient(coURL, token string, transport http.RoundTripper) *client {
	return &client{api: coapi.New(coURL, token, transport)}
}

// requestError is a request that did not get the response the sync tick needs.
type requestError = coapi.RequestError

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
	resp, err := c.api.Get(ctx, manifestPath, h)
	if err != nil {
		return manifestResponse{}, err
	}
	switch resp.Status {
	case http.StatusNotModified:
		return manifestResponse{notModified: true}, nil
	case http.StatusOK:
		return manifestResponse{body: resp.Body, etag: resp.Header.Get("ETag")}, nil
	default:
		return manifestResponse{}, coapi.Failed(http.MethodGet, manifestPath, resp)
	}
}

// content gets a deployment YAML or a bundle by its path (SPEC §8.2 step 4). Any status other
// than 200 is a *requestError; a 404 means only that the digest is unavailable.
func (c *client) content(ctx context.Context, p string) ([]byte, *requestError) {
	resp, err := c.api.Get(ctx, p, http.Header{})
	if err != nil {
		return nil, err
	}
	if resp.Status != http.StatusOK {
		return nil, coapi.Failed(http.MethodGet, p, resp)
	}
	return resp.Body, nil
}
