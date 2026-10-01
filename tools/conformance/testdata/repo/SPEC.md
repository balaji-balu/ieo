# Fixture spec

## 17. Test and Validation Matrix

Unless otherwise noted, §17.1–§17.2 are `Core Conformance`.

### 17.1 Contracts

Prose before the list is not a bullet, even when it says MUST.

- Device ID parsing splits on the first `/` only.
- Site IDs with characters outside RFC 3986 unreserved are rejected; `any` is rejected
  as a site ID.

### 17.2 Sync

- First sync stores the manifest.
- First sync with an empty bundle stores nothing.
- Unmatched bullet stays a gap.

### 17.8 Site Integration Profile (RECOMMENDED)

- Golden path: import app → deploy directed → delete.

### 17.10 Goal Coverage

| Goal | Bullets |
| --- | --- |
| G1 | §17.1 |

Gaps: none.

## 18. Checklist

- Not a §17 bullet.
