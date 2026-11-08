# LINE member access projection

`GET /priv/line/member-access?locale=zh-Hant` is a private presentation endpoint for
`hhc-line-function-bot`. It requires the trusted Dapr caller/token and an Account
subject in the existing trusted user/provider headers. The Bot must resolve the
requester's LINE identity immediately before each call. Gateway/browser callers
and query-selected subjects are rejected.

The response contains only `qualified`, `editions` and `legal`. Qualification and
edition decisions come from the existing Operations authorities; legal acceptance
uses the same current common/member manifests and checks as protected content.
Unqualified users receive no editions and `not_applicable`; unavailable authorities
return 503 instead of a denial or partial snapshot. `locale` selects legal/UI
language independently of content grants. The supported edition order is the
existing five-entry bulletin policy list, including children English and excluding
unsupported children Simplified Chinese.

This snapshot is never a grant or cache of content authorization. Each subsequent
member bulletin/discovery/download request continues to recheck current access.
No account identifier, organization, role, membership record, entitlement code or
content is returned. The endpoint is `private, no-store`.

Roll out this additive producer before the Bot consumer. Older Bot versions are
unaffected; the new Bot treats an absent/unavailable endpoint as unknown membership.
The online discovery OpenAPI caller inventory now includes the Bot, matching the
existing member-route runtime guard. No public Gateway route is added.

Focused authorization/legal and OpenAPI contract tests cover spoofed callers,
cross-source identity selection, malformed decisions, revocation, independent UI
language and dependency failure. Native LINE/browser acceptance and production
rollout remain separate from these service tests.
