# 0021. EN registry access: Docker `config.json` credentials, HTTPS unless told otherwise

Status: Proposed · Date: 2026-10-10 · Spec: §5.1, §6.3, §8.9, §15.3, §15.4 · Refines: 0019

## Context
The EN pulls each component's Compose archive from an OCI registry (SPEC §8.9 step 4.2). ADR 0019
left "opening the repository (transport, credentials, `en.registry.*`)" as a function passed to the
puller, to be wired in roadmap D5 (balaji-balu/ieo#58). SPEC §6.3 names two keys and says no more:
`en.registry.auth_file` (credentials) and `en.registry.insecure`. Open:
- the format of the credentials file;
- what `insecure` allows;
- which host a credential may be sent to.

Registry credentials are a §15 area (SPEC §15.4).

## Decision
- **Package.** `internal/en/registry` returns the `pull.OpenRepository` the puller uses. It is the
  only EN package that knows about registry transport and credentials.
- **File format.** `en.registry.auth_file` is a file in the Docker `config.json` format. The EN
  reads only its `auths` object: for each registry host, `auth` (base64 of `username:password`), or
  `username` and `password`, or `identitytoken`.
  - `credsStore` and `credHelpers` are ignored, with one warning at startup that names the key.
    The EN never runs a credential helper: it would be a program chosen by a file.
  - The file is read once, at startup. A file that is set but missing, unreadable or not valid is a
    configuration error (exit code 2) that names the file and never its contents.
  - No file set means every pull is anonymous.
- **Which host gets a credential.** The entry whose key equals the repository's registry host
  (`host` or `host:port`, compared exactly, after removing a scheme and path from the key) is used
  for that registry, and for the token service that registry names when it asks for a token. No
  entry is ever sent to another registry. A registry with no entry is reached anonymously.
  - Docker Hub is the one registry with several names. `docker.io`, `index.docker.io` and
    `registry-1.docker.io` are the same registry here, in a key and in a repository, so the entry
    `docker login` writes (`https://index.docker.io/v1/`) is used for `docker.io/<org>/<app>`.
  - At startup the EN logs the registry hosts it holds an entry for, never the entries, so an
    operator can see which pulls will be anonymous.
- **Transport.**
  - By default HTTPS, with the registry's certificate verified against the system roots.
  - `en.registry.insecure: true` makes the EN use plain HTTP for every registry. It logs a warning
    at startup, which also says so when credentials are configured, since they are then sent in
    the clear. It does not mean "HTTPS without verification": that mode does not exist.
  - Redirects are followed, as registries redirect layer downloads to storage hosts. The
    `Authorization` header is not sent to another host on a redirect.
  - The environment's proxy settings are honored (`HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`), unlike
    the LO's Margo client (SPEC §15.6): edge hosts often reach a registry only through a proxy.
    Over HTTPS the credential travels inside TLS and the proxy does not see it. With
    `en.registry.insecure` it is in the clear to the registry and to any proxy on the way; the
    startup warning says so, and names the proxy host when one is set.
- **Secrets.** No credential, `auth` value or token is logged, put in an error, or put in a status
  message (SPEC §15.4). Errors name the registry host and the HTTP status.
- **Limits and integrity** stay in the puller (ADR 0019): `en.pull.max_bytes`, `en.pull.timeout`
  and the layer digest check apply whatever this package returns.

## Consequences
- An operator can use the file `docker login` wrote, if it holds static entries. A host that uses a
  credential helper needs a file written for the EN.
- Changing credentials needs an EN restart.
- A registry with a private CA is not reachable over HTTPS until a CA key is specified; on the
  laptop harness the registry is plain HTTP with `en.registry.insecure` (ADR 0009).
- With `insecure`, a registry that serves only HTTPS is not reachable: the setting is for the whole
  EN, not per registry.
- Tests use the fake registry in `internal/ocitest`, with credentials made up in the test itself;
  none is kept in a fixture file (SPEC §15.6).

## Options considered
- An IEO file format (`host → username, password`): the smallest thing to parse, but a new format
  to document, and operators would convert a file they already have.
- Run credential helpers named in the file: needed for some cloud registries, but the EN would
  execute a program named by configuration with the EN's privileges.
- Per-registry `insecure` (a list of hosts, as the Docker engine has): more precise, and a spec
  change to a key that §6.3 defines as a boolean. To propose if a site needs both.
- Ignore the proxy environment, as the LO does for the CO: it would make the registry unreachable
  on hosts that have no direct route to it.
