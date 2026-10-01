# Margo specification — pinned files

The files here come from one upstream commit (SPEC §17.1, ADR 0010). Do not edit them; a pin change
replaces this directory (see `docs/margo-pins.md`).

- Repository: https://github.com/margo/specification
- Commit: `f209a7f21304a906bf637db044a293f7b0b1e586`

| File | Source | SHA-256 |
| --- | --- | --- |
| `workload-management-api-1.0.0-rc.3.yaml` | copied from `system-design/specification/margo-management-interface/workload-management-api-1.0.0-rc.3.yaml` | `39697def1f8a5b96940fd48234d4d060c9bbad3aac8255958c344cd9183fa045` |
| `application-description.linkml.yaml` | copied from `src/specification/applications/application-description.linkml.yaml` | `a0a97f69e666512efe1812aa57db39e4c6a37ea3c6a614620b6a1f16003757e0` |
| `application-description.schema.json` | generated from the LinkML file above (command below) | `fa7912e80f3f6037cbcbc93888a8d6dda5a853fccf7055dddad463601ad4d79e` |

Margo defines the Application Description schema in LinkML, which Go can't validate against. The
JSON Schema is LinkML's own translation, generated with `linkml` 1.11.1 (Python), and is
reproducible byte for byte:

```sh
pip install linkml==1.11.1
gen-json-schema --top-class ApplicationDescription application-description.linkml.yaml > application-description.schema.json
```

The CO validates Application Descriptions against the generated file (SPEC §5.3,
`internal/contract`).

## Known upstream defects

`internal/contract` works around these in memory, never in these files: the §17.1 contract tests for
the OpenAPI file, and Application Description validation for the generated schema.

| Schema | Defect | Workaround |
| --- | --- | --- |
| `UnsignedAppStateManifest` | `required` lists `bundle.mediaType`, `bundle.digest` and `bundle.url`. JSON Schema reads these as literal top-level property names, so every valid manifest fails validation. | The test validator drops `required` entries that contain a `.` before compiling. |
| `application-description.schema.json`: `ApplicationDescription`, `DeploymentProfile`, `Component` | The LinkML slot `x-placeholder-extensions` stands for any `x-<name>-extensions` key, but the generator emits a literal property `x_placeholder_extensions` and closes `DeploymentProfile` and `Component` (`additionalProperties: false`). Real vendor extensions are rejected there. | Replace the property with `patternProperties: {"^x-.+-extensions$": {type: object}}`. |
| `application-description.schema.json`: `Configuration.schema` items | LinkML subclasses (`TextValidationSchema`, …) are emitted as separate definitions, and the items reference only the closed base `Schema`, so fields such as `maxLength` are rejected. Margo's own valid examples fail. | Replace each `$ref` to `Schema` with `anyOf` over its five subclasses. |
