# Entitlement and Dapr incident tracing

This instrumentation locates failures; it does not fix or certify the underlying
platform fault. Keep the livestream release hold until explicitly lifted.

The Operations client preserves the three-second timeout and fail-closed policy.
It propagates the validated audit request ID and W3C trace context (no baggage),
and emits a sampled `operations.entitlement_check` client span. Every failure or
call lasting at least one second also emits `operations_entitlement_call`, even
when the trace is not sampled. The event includes outcome, last phase, status,
connection reuse and elapsed milestones (`-1` means the milestone was not seen).
No request/response body, member ID, entitlement code, URL, token, or raw transport
error is exported. Returned errors still preserve context cancellation/deadline.

Phases: `connection`, `request_write`, `response_headers`, `response_status`,
`response_body`, `decode`, `complete`. `connection_ms` ends at GotConn and includes
pool/dial time. These are cumulative milestones, not additive stage durations.
The HTTP connection is to local Dapr: waiting for response headers includes the
remote sidecar, Operations dispatch and its DB work. That phase alone does not
prove a Dapr defect. A successful HTTP decision of deny remains a successful call.

Application readiness only checks local initialization. A separate
`dependency_health` heartbeat runs every 30 seconds with independent two-second
DB and Dapr budgets. DB phases distinguish `acquire` and `ping`; Dapr distinguishes
`response_headers` and `response_status`. Each event includes dependency, phase,
outcome, status and elapsed time, including success/recovery. Monitor INFO logs;
no heartbeat for an active replica for 90 seconds is unknown, not healthy. No
production alert configuration is changed by this source patch.

Website access logs also include `application_error_code` from the existing
error writer, without reading or storing response bodies. This is the application
classification before capture wire adapters translate codes. Match it by request
ID; a Dapr-generated error with no AP log remains a separate transport blind spot.

## Read-only correlation

Start with a failed Gateway request ID. Use a narrow UTC time range and keep the
original timestamps, replica names and immutable release identities.

```kusto
let request = "REPLACE_WITH_REQUEST_ID";
ContainerAppConsoleLogs_CL
| where TimeGenerated between (datetime(2026-10-30T12:22:00Z) .. datetime(2026-10-30T12:25:00Z))
| where ContainerAppName_s in ("api-gateway", "hhc-web-api", "operations-api")
| where Log_s contains request
| project TimeGenerated, ContainerAppName_s, ContainerGroupName_s, ContainerName_s, Log_s
| order by TimeGenerated asc
```

Match `operations_entitlement_call` to Operations `http_request` and
`operations_db_call`. A missing Operations completion event is not proof that the
request never arrived: it may still be running or logging may be delayed. A
query-phase failure is different from acquisition failure. Operations pool wait
counters are process totals, not one request's wait. No authorization decision
may be reconstructed from a missing trace or a diagnostic correlation ID.

Then correlate the affected replicas with `ContainerAppSystemLogs_CL` probe and
termination events and Dapr console errors. Generic ProbeFailed messages may not
identify a container; only explicit daprd failure/termination identifies a
sidecar restart. Control-stream resets and missing platform metrics are clues,
not proof of a node/network root cause. Preserve per-pod resource samples and
platform events for provider investigation; the client instrumentation cannot
recover historical Dapr CPU throttling or node stacks.

## Acceptance before release

Run client fault cases (headers/body deadline, upstream rejection, malformed
response), redaction/trace propagation tests, readiness tests, and Operations
pool-exhaustion/query-lock tests against disposable PostgreSQL. Complete both
repositories' CI. After a separately authorized release, verify correlation on
normal traffic and collect natural failures without fault-injecting production.
