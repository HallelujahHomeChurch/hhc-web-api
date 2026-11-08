# Recording public ID rollout

Migration 107 is additive: one reservation table with two unique indexes and a
format constraint. It changes no existing recording row. Reservations survive
recording deletion and must survive runtime rollback. Backfill does not publish
content or change existing UUIDs, grants, covers, comments or playback identity.

Schema creation is part of the normal approved PR merge/native release migration
Job. Do not deploy an unmerged image, run a local binary against production, or
alter permanent Job configuration to perform backfill. Obtain separate explicit
approval for schema rollout and backfill after reviewing current counts.

Before schema rollout, an already authorized read-only database session can count
`hhc_web.member_recording` and check `to_regclass('hhc_web.member_recording_public_id')`.
There is no existing HTTP endpoint providing an authoritative count; the admin list
is capped and must not be substituted. Production observations belong in private
coordinator evidence, not this generic runbook.

After the merged immutable image has released and migration 107 is verified, the
existing `hhc-web-migrate` Job supports execution-only overrides. The installed
Azure CLI `az containerapp job start --help` documents both `--args` and `--yaml`.
Use the complete existing container template via `--yaml`, following the existing
worker-preflight pattern, because args alone can replace the submitted container
without retaining its image/env/resources. JSON is valid YAML.

1. Verify the current Job image equals the accepted release's immutable digest,
   command is `/hhc-web-migrate`, and its existing environment contains only the
   `DATABASE_URL` secret reference. Never retrieve the secret value. Preserve
   current name/image/command/env/resources for the execution. An unexpected
   template must be reviewed, not guessed or silently reduced.
2. Produce a plan template containing those exact fields and
   `args: ["--recording-public-ids=plan"]`. This command runs no schema migration
   and writes no IDs. It reports only total/missing counts, uses one connection,
   and has a two-minute deadline.
3. Start exactly one execution with
   `az containerapp job start -g alive -n hhc-web-migrate --yaml PLAN_FILE`.
   Wait for that execution's success and retain only its sanitized count result,
   execution ID and immutable image identity. A failed/unknown execution is not
   approval to retry an apply.
4. Review the plan and obtain backfill approval. With the same accepted image and
   template, replace only args with `["--recording-public-ids=apply"]`. Start one
   execution at a time using the same `--yaml` route. Each commits at most 100 IDs
   and reports assigned/missing counts. Do not overlap a native migration/release.
5. Repeat within the approved row/batch scope until plan reports missing zero.
   Counts can change due to creators and old revisions; a zero-size batch can also
   mean another operation holds row locks. Recheck once older producer revisions
   are retired. Stop for an unexpected increase or exhausted approval scope.
6. Read back the persistent Job template and confirm it remains unchanged. The
   override must never be written with `job update`. Deploy Gateway/SDK, then the
   website canonical route and legacy redirects, and verify them before enabling
   short-link producers.

Generate the template from nonsecret metadata only; this example refuses any
unexpected environment shape and does not read or persist credential values:

```sh
az containerapp job show -g alive -n hhc-web-migrate \
  --query 'properties.template.containers[].{name:name,image:image,command:command,resources:resources,env:env[].{name:name,secretRef:secretRef}}' \
  -o json > containers.json
jq -e --arg image "$APPROVED_MIGRATION_IMAGE" '
  if length == 1 and .[0].name == "hhc-web-migrate"
    and .[0].image == $image and ($image | test("@sha256:[a-f0-9]{64}$"))
    and .[0].command == ["/hhc-web-migrate"]
    and .[0].env == [{"name":"DATABASE_URL","secretRef":"database-url"}]
  then {containers: [.[0] + {args:["--recording-public-ids=plan"]}]}
  else error("unexpected migration template or release image") end
' containers.json > plan.json
```

The plan/apply CLI path deliberately does not run migrations. Missing schema fails
closed. Cancellation rolls back the current batch; committed batches remain stable.
No down migration or reassignment is an acceptable rollback. Keep the temporary
metadata files only for the reviewed operation, then remove them under the task's
normal cleanup rules.
