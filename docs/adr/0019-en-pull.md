# 0019. EN pull: spool file, what is read and what counts as a mismatch

Status: Proposed · Date: 2026-10-10 · Spec: §5.1, §8.9, §9.1, §10

## Context
Roadmap slice D2b (balaji-balu/ieo#58) builds `internal/en/pull`, which fetches a component's
Margo Compose Archive (SPEC §8.9 step 4.2). The spec says to verify the layer's digest before
extracting, to read at most `en.pull.max_bytes` of a response, and to keep the layer in
`<en.data_dir>/pull/`. It leaves open:
- how a layer of up to 256 MiB is held while it is verified;
- how much is read when a registry sends more, or less, than the manifest says;
- which failures are `IEO-DIGEST-MISMATCH` and which `IEO-PULL-FAILED`;
- how "exactly one layer" (§5.1) is read when a manifest lists other layers too.

## Decision
- **Spool.** The layer is written to a new file `layer-<random>` (mode 0600) in the spool
  directory, hashed as it is written, and handed to the caller only once it matches. The caller
  closes it, which deletes the file. A failed pull deletes its file before it returns.
  - A file stays only if the EN dies during a pull or an extraction. The EN empties the spool
    directory when it starts (roadmap D5).
- **What is read.**
  - The manifest is refused from its descriptor if it is over the limit; none of it is read.
  - The layer is refused from the size its manifest gives if that is over the limit. Otherwise
    exactly that many bytes are read. What a registry sends beyond them is never read.
- **Mismatch.** The pull fails with `ErrDigestMismatch` (`IEO-DIGEST-MISMATCH`) if the registry
  answered and the bytes read are fewer than the manifest's size or hash to another digest. Every
  other failure is `IEO-PULL-FAILED`, including a read that ends with an error, a digest whose
  form or algorithm is not supported, and a tag that is not a Margo Compose Archive.
- **Digest algorithms.** The layer's digest is checked with the algorithm it names: SHA-256,
  SHA-384 or SHA-512. SPEC §4.2's `sha256:` form is about deployment digests, not OCI descriptors.
- **Manifest.** A Margo Compose Archive is an OCI image manifest with the archive `artifactType`
  and one layer in all, of the archive media type. A manifest with any second layer is refused.
- **Repository.** The puller takes the component's `repository` as the deployment gives it and
  removes the `oci://`; anything else is a failed pull. Opening the repository (transport,
  credentials, `en.registry.*`) is a function passed in, wired in roadmap D5.
- **Timeout.** `en.pull.timeout` is a deadline on one context for the whole pull. The caller's
  context ending is reported as that, not as a timeout.

## Consequences
- Peak disk use of a pull is the layer plus the extracted archive; memory use does not grow with
  the layer.
- A registry that cuts a response short without an error looks like a mismatch, not a failed
  pull. Both are retried by the LO, so only the reported code differs.
- A manifest read by tag is trusted for the layer's digest: the tag is the only identity the
  deployment gives a component (§4.2), so a registry that serves another manifest for the tag is
  not detected here.

## Options considered
- Hash the layer while extracting it and delete the result on a mismatch: unverified bytes would
  reach the extractor, against §8.9.
- Hold the layer in memory: 256 MiB per pull at the default limit.
- Read one byte past the manifest's size to detect a registry that sends more: it reads past
  `en.pull.max_bytes` when the layer is exactly at the limit, and the extra bytes are never used.
- Accept other layers beside the archive layer, as the application package does (§5.1): Margo
  gives the archive artifact one layer, and nothing would read the others.
