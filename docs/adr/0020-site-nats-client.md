# 0020. Site NATS client: one package, endless reconnect, nothing kept while disconnected

Status: Proposed · Date: 2026-10-10 · Spec: §4.2, §7.6, §11.2, §15.4, §15.6, §16.2, §16.7 · Refines: 0005

## Context
Roadmap slice D5 (balaji-balu/ieo#58) connects the LO and the EN to the site NATS server. Until
Appendix B step 4 the server is external and the tiers of a site share one username and password
(SPEC §15.6, ADR 0005). The spec gives the subjects, the messages and the credential rules. It
leaves open:
- where the subject format and the §15.6 checks live, since two tiers need both;
- what a tier does when the server cannot be reached at startup, or stops answering later;
- what happens to a message published while the connection is down;
- how the code is tested without a fake that hides the behaviour under test (reconnect).

The old code (`internal/natsbroker`) connects once with no credentials and no TLS, and is deleted
with the Git-based LO (ADR 0002).

## Decision
- **One package.** `internal/sitenats` is the only new code that imports `nats.go`. `cmd/lo` and
  `cmd/en` get a `*nats.Conn` from it. It owns:
  - the subject format (SPEC §4.2, §11.2): building each subject from a site ID and host ID, the
    LO's wildcard subjects, and reading the host ID back from a subject. No other package formats
    or splits a subject;
  - the connection settings below.
  Message types, schemas and handlers stay where they are: `internal/contract` and each consumer.
- **IDs in subjects.** A site ID or host ID that contains `.` is refused (SPEC §4.2). The ID types
  in `internal/contract` refuse it when they parse, so the CO, the LO and the EN all do; the subject
  builders accept only those types.
- **URL.** One server URL per tier (`lo.nats.url`, `en.nats_url`).
  - The scheme must be `tls://`, unless the tier's `nats_insecure` is true (SPEC §15.6). With it, a
    warning at startup names the URL.
  - A URL with user information (`user:pass@host`) is a configuration error, with or without
    `nats_insecure`: the password comes only from the environment (SPEC §15.4), and a URL is
    logged.
  - Both are checked before any connection is tried, and fail as configuration errors (exit code 2).
- **TLS.** With `tls://` the server's certificate is verified against `nats.ca_file` when set, or
  the system roots. `nats_insecure` never turns verification off: it only allows a URL without TLS.
- **Credentials.** The username and password are given to the client as options. They are never
  put in a URL, a log line, an error or a status message. The package logs the URL and the server's
  reason for a failure, nothing else from the connection.
- **Startup.** A server that cannot be reached is not a startup failure. The tier starts, tries
  again every 2 seconds without end, and logs each failure at warning level. A refused login is
  treated the same way: it is logged and tried again, since the fix is on the server or in a
  restart with other credentials.
- **Reconnect.** A lost connection is retried the same way, without limit. On every connect and
  reconnect the package calls one function its caller gave it:
  - the EN publishes its inventory (SPEC §7.6, §16.7);
  - the LO publishes `site.<s>.inventory.request` (SPEC §16.2). ENs whose own connection never
    dropped would otherwise not know the LO missed their messages.
- **While disconnected.** Nothing is buffered. A publish fails at once with an error; the caller
  logs it and goes on. State recovers it (SPEC §7.6): status events are in the EN store and reach
  the LO in the next inventory.
- **Commands.** The LO sends a command as a request with `lo.command.ack_timeout`. "No subscriber"
  from the server and a timeout are the same outcome for the caller (SPEC §8.5, §11.2).
- **Shutdown.** A tier first drains its subscriptions, so no handler is running or still to come,
  then waits for its own work (on the EN, `Dispatcher.Wait`), then closes the connection.
- **Tests.** The package and the wiring that uses it are tested against a real `nats-server`
  started in the test process on a loopback port chosen by the system (module
  `github.com/nats-io/nats-server/v2`, imported only from `_test.go` files). Tests that need no
  connection (planner, executor, dispatcher) keep their fakes.

## Consequences
- An EN on a host whose site server is down stays up, keeps its workloads, and joins when the
  server returns. Nothing restarts it in a loop.
- A wrong password does not stop the tier. It shows only as a repeating warning, so an operator
  must read the log or, from roadmap M, the metrics.
- A status event published during an outage is lost as a message. The LO sees the state at the
  next inventory, at most `en.inventory.interval` after reconnecting, and at once on reconnect.
- An Apply command carries the whole deployment. One larger than the server's maximum payload
  (1 MiB by default) cannot be sent; the send fails and is logged like a rejected command.
- The test binary of a few packages links `nats-server`. It is not in any shipped binary. These
  tests open a loopback socket, which the "no network" line of G-F4 allowed only for fake
  registries until now; `docs/coding-guidelines.md` is updated in the PR that adds the first one.
- From Appendix B step 4 the LO runs the server and credentials are per host. The subject code and
  the reconnect rules stay; the URL and credential rules are replaced.

## Options considered
- A wrapper interface over the connection (`Publish`, `Request`, `Subscribe`) with a fake for
  tests: every method would forward to `nats.go` (G-A4), and reconnect, the behaviour §17.6 asks
  about, would be simulated by the fake rather than exercised.
- Subject strings built where they are used, in `cmd/lo` and `cmd/en`: two places would know the
  format, and the `.` rule would be checked in neither or both (G-A2).
- Exit when the server cannot be reached at startup, and let the service manager restart the tier:
  the LO would stop syncing with the CO because of a site-side fault, and the EN would depend on a
  restart policy that hosts may not have.
- Keep the client's default buffer for messages published while disconnected: on reconnect the LO
  would get old status events first, and each inventory published during the outage, before the
  one that is true now.
- Encode IDs so that `.` can appear in a subject: subjects would no longer read as the IDs they
  carry, in logs and in server permissions (roadmap O), to allow a character no site needs.
