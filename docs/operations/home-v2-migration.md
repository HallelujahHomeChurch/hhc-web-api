# Home v2 Migration

Use the `Home v2 Migration` workflow from `main`. The released image must match the latest ready `hhc-web-api` revision. The operation reuses the production content-import Container App Job but overrides its command with `/hhc-web-home-v2-migrate`.

## Plan

Run `mode=plan`. This is read-only and emits exactly one JSON report containing:

- published Home, Site Settings, and ordered Location IDs/versions;
- five converted locale SHA-256 values;
- the three published external links, Location count, and empty Banner state;
- `updates=1`, `inserts=0`, `deletes=0`, `warnings=0`, `conflicts=0`;
- `sourceSHA256` and `planSHA256` review gates.

Save the workflow run, immutable image digest, full report, `sourceSHA256`, and `planSHA256`. Any missing locale, unsafe URL, duplicate key/order, stale draft, or noncanonical public projection stops the plan.

## Apply

Do not apply during Task 3. During the approved cutover, run `mode=apply` from the original workflow attempt and enter the reviewed `sourceSHA256` and `planSHA256`. The production environment approval is the manual gate.

The workflow reruns plan before apply and rejects source drift, a changed runtime/job image, reruns, or mismatched hashes. Apply holds the dedicated advisory lock, converts only the fixed Home aggregate to one `home.v2` draft, leaves the five live `home.v1` projections unchanged, clears the draft Banner, and is idempotent.

Rollback before v2 publication is to leave or restore the Home v1 revision as draft; no public projection, asset grant, or Blob is changed by this migration operation.
