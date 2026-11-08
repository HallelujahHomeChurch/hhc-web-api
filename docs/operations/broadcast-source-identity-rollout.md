# Named-source identity foundation

This is the first M2 producer change, not K4 endpoint activation or a complete
named-key management API. No new public route or feature flag is enabled here.

Migration 093 replaces the singleton primary key with a stable UUID primary key.
It retains the original row, key hash, suffix, signing seed, revision and target.
The original row alone has singleton=true; new rows may use NULL, never false.
A unique filtered index retains the old WHERE singleton selector. The constraint
replacement is an expand-only exception pinned by SHA-256 in migration policy;
real PostgreSQL tests execute the original 091/092 DDL then 093 with a configured
key/target and compare the state. No row, recording or media is deleted.

Source-managed recordings gain a foreign key to the original source. Session
credential lookup uses that recording's source, not the newest target. For an
old binary creating a source-managed recording without source_id during rollout,
NULL source_id explicitly falls back to the legacy source; unknown/unmanaged
recordings fail closed. Other sources never become that fallback. The legacy
Console target adapter rejects a recording belonging to another named source.

Existing global input uniqueness and advisory locks remain in place: capacity
is still one, including legacy/C1 callers. No concurrency increase is implied by
being able to store multiple source identities. K4 open/ownership, named-source
CRUD, input admission and Console UI remain subsequent M2/M3 changes.

The new writer increments key_generation during a successful legacy key change.
Old writers do not know this field; do not enable K4/named-source writers until
all active runtimes are on the compatible producer. Rollback leaves the additive
columns and constraints in place; old WHERE singleton operations still address
only the legacy row. Keep new writers disabled during rollback.

Before a production merge/release, inspect the migration preview and actual
service revisions under the repository delivery authorization. After release,
verify the legacy selector has exactly one row and key hash/seed equality using
booleans only; never print their values. Neither local database tests nor CI
establish production migration success or Windows acceptance.
