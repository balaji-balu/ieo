# Margo Workload Management API — pinned copy

`workload-management-api-1.0.0-rc.3.yaml` is copied unchanged from upstream (SPEC §17.1,
ADR 0010). Do not edit it; a pin change replaces this directory (see `docs/margo-pins.md`).

- Repository: https://github.com/margo/specification
- Commit: `f209a7f21304a906bf637db044a293f7b0b1e586`
- Source path: `system-design/specification/margo-management-interface/workload-management-api-1.0.0-rc.3.yaml`
- SHA-256 of the file: `39697def1f8a5b96940fd48234d4d060c9bbad3aac8255958c344cd9183fa045`

## Known upstream defects

The §17.1 contract tests (`internal/contract`) work around these in memory, never in this file.

| Schema | Defect | Workaround |
| --- | --- | --- |
| `UnsignedAppStateManifest` | `required` lists `bundle.mediaType`, `bundle.digest` and `bundle.url`. JSON Schema reads these as literal top-level property names, so every valid manifest fails validation. | The test validator drops `required` entries that contain a `.` before compiling. |
