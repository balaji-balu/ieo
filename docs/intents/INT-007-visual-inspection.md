---
artifact_id: INT-007
issue:
title: Visual inspection (edge-server tier)
repo: ieo-examples (apps/visual-inspection/)
status: draft
priority: P3
depends_on: [INT-003]
affected_spec: ["§8.6", "§8.9", "§14.2", "§18.2 (placement beyond memory)"]
human_checkpoint: owner agrees it is worth doing
---

# INT-007: Visual inspection (edge-server tier)

## Problem
Edge servers exist to run heavy workloads such as vision inference close to the line. IEO has no
example with a large image, high resource needs or an accelerator, so capacity placement, slow
pulls of large images and (later) GPU placement are untested.

## Outcome
A visual-inspection app that takes frames from the INT-003 camera simulator, runs a small ONNX
defect-detection model on an edge server, and reports pass/fail per item with a running defect
rate.

## Acceptance criteria
- Declares capacity requirements that keep it off gateway-class hosts.
- Runs on CPU on the laptop; a GPU, when present, is optional and detected.
- The model version is changeable through a Margo parameter or a new package version, without
  rebuilding the app.
- The first install over a bandwidth-limited link (INT-002) completes and reports progress
  meaningfully rather than timing out silently.
- Known-defect frames from the simulator are detected at the rate the model's test set shows.

## Success metrics
- Placement never puts it on the BeagleBoard; it fails with a clear error when no server host
  is eligible.

## Non-goals
- Training or tuning the model.
- GPU scheduling (recorded as an input for the §18.2 placement work).

## Open questions
- Which small, openly licensed defect dataset and model to use?
