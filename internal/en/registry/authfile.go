package registry

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"oras.land/oras-go/v2/registry/remote/auth"
)

// maxAuthFileBytes is the most of an auth file that is read. A config.json with a few entries is
// a few KiB; a file far larger is not one.
const maxAuthFileBytes = 1 << 20

// The names of Docker Hub. They are one registry (ADR 0021): `docker login` writes its entry
// under index.docker.io, a repository names docker.io, and requests go to registry-1.docker.io.
const dockerHub = "docker.io"

// registryOf returns the registry a host belongs to: the host itself, or dockerHub for each of
// Docker Hub's names. Hosts are compared exactly, port included (ADR 0021).
func registryOf(host string) string {
	switch host {
	case "docker.io", "index.docker.io", "registry-1.docker.io":
		return dockerHub
	}
	return host
}

// authFile is what the EN reads of a Docker config.json (ADR 0021).
type authFile struct {
	// Credentials holds the credential of each registry that has a static entry, by registryOf
	// its host.
	Credentials map[string]auth.Credential
	// Helpers names the keys of the file that ask for a credential helper (`credsStore`,
	// `credHelpers`), sorted. The EN runs none.
	Helpers []string
}

// Hosts returns the registries that have a credential, sorted.
func (f authFile) Hosts() []string {
	return slices.Sorted(maps.Keys(f.Credentials))
}

// dockerConfig is the part of a Docker config.json the EN looks at.
type dockerConfig struct {
	Auths       map[string]dockerAuth `json:"auths"`
	CredsStore  string                `json:"credsStore"`
	CredHelpers map[string]string     `json:"credHelpers"`
}

// dockerAuth is one entry of `auths`. `registrytoken` is not read (ADR 0021).
type dockerAuth struct {
	Auth          string `json:"auth"` // base64 of `username:password`
	Username      string `json:"username"`
	Password      string `json:"password"`
	IdentityToken string `json:"identitytoken"`
}

// readAuthFile reads the Docker config.json at path. The error is a *ConfigError, which names
// path and, for an entry that is at fault, its registry host: never a value of the file.
func readAuthFile(path string) (authFile, error) {
	data, reason := readLimited(path)
	if reason != "" {
		return authFile{}, &ConfigError{File: path, Reason: reason}
	}
	var cfg dockerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return authFile{}, &ConfigError{File: path, Reason: "not a Docker config.json: " + decodeReason(err)}
	}
	file := authFile{Credentials: map[string]auth.Credential{}}
	if cfg.CredHelpers != nil {
		file.Helpers = append(file.Helpers, "credHelpers")
	}
	if cfg.CredsStore != "" {
		file.Helpers = append(file.Helpers, "credsStore")
	}
	for _, key := range slices.Sorted(maps.Keys(cfg.Auths)) { // a stable first error
		host := hostOfKey(key)
		if host == "" {
			return authFile{}, &ConfigError{File: path, Reason: "an entry of auths has a key with no registry host"}
		}
		cred, reason := cfg.Auths[key].credential()
		if reason != "" {
			return authFile{}, &ConfigError{File: path, Reason: fmt.Sprintf("the entry for %s: %s", host, reason)}
		}
		if cred == auth.EmptyCredential {
			continue // what `docker login` leaves when the credential is in a helper
		}
		if _, twice := file.Credentials[host]; twice {
			return authFile{}, &ConfigError{File: path, Reason: fmt.Sprintf("two entries name the registry %s", host)}
		}
		file.Credentials[host] = cred
	}
	return file, nil
}

// readLimited returns the content of the file at path, or why it cannot be used. The reason
// holds what the operating system said, which includes the path and nothing of the content.
func readLimited(path string) (data []byte, reason string) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "cannot be read: " + osReason(err)
	}
	defer func() { _ = f.Close() }() // read-only: nothing to lose
	data, err = io.ReadAll(io.LimitReader(f, maxAuthFileBytes+1))
	if err != nil {
		return nil, "cannot be read: " + osReason(err)
	}
	if len(data) > maxAuthFileBytes {
		return nil, fmt.Sprintf("larger than %d bytes", maxAuthFileBytes)
	}
	return data, ""
}

// osReason returns the operating system's part of a file error, without the path the
// *ConfigError already names.
func osReason(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// decodeReason says why the file is not JSON of the expected shape, without quoting it: the
// text of a JSON error can hold a character or a value of the file.
func decodeReason(err error) string {
	var syntax *json.SyntaxError
	var mistyped *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		return fmt.Sprintf("not valid JSON at byte %d", syntax.Offset)
	case errors.As(err, &mistyped):
		return fmt.Sprintf("a value of the wrong type at byte %d", mistyped.Offset)
	}
	return "not valid JSON"
}

// hostOfKey returns the registry a key of `auths` names: the key without a scheme and a path, so
// that `https://index.docker.io/v1/` and `reg.example:5000` are both understood (ADR 0021).
func hostOfKey(key string) string {
	key = strings.TrimPrefix(key, "https://")
	key = strings.TrimPrefix(key, "http://")
	host, _, _ := strings.Cut(key, "/")
	return registryOf(host)
}

// credential returns what the entry holds, or why it cannot be decoded. `auth` is used when
// `username` and `password` are there too; an entry with none of the forms gives
// auth.EmptyCredential.
func (a dockerAuth) credential() (cred auth.Credential, reason string) {
	cred = auth.Credential{Username: a.Username, Password: a.Password, RefreshToken: a.IdentityToken}
	if a.Auth != "" {
		decoded, err := base64.StdEncoding.DecodeString(a.Auth)
		if err != nil {
			return auth.EmptyCredential, "auth is not base64"
		}
		username, password, ok := strings.Cut(string(decoded), ":")
		if !ok || username == "" {
			return auth.EmptyCredential, "auth is not the base64 of username:password"
		}
		cred.Username, cred.Password = username, password
		return cred, ""
	}
	if (cred.Username == "") != (cred.Password == "") {
		return auth.EmptyCredential, "username and password are not both set"
	}
	return cred, ""
}
