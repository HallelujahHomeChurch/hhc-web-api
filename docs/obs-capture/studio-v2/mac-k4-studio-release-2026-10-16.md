# K4 Studio alignment and normal-Stop follow-up — 2026-10-16

Status: platform release and read-only production smoke complete. Windows may run the minimum real-OBS follow-up only after confirming the current meeting schedule and valid user authorization.

## Scope and preserved contracts

No OBS wire changes. K4 `k4-2026-10-15.rc1`, K3 `k3-2026-10-14.rc1`, B1 `b1-2026-10-10.rc1`, C1 `c1-2026-10-08.2`, inventory 1. Preserve 30000/1001 fps, 900 frames / 30.03 seconds, digest, idempotency, full media verification and `[start,endExclusive)`.

Frozen K4 handoff at CMS commit `5e8c33ec1b3ca5628605fab0a578ca8312d67d1d`, `docs/obs-capture/studio-v2/windows-handoff.md`: SHA-256 `d63a5d3b5d6e2e7835e0e1f162fed3a2050f510a94d44759bfd4fdc30d064c2e`. Manifest SHA-256 `e984a5a54b80c817ba701f6923d73c02b57bf9c5b7d238f15442543c5ae18464`.

## Findings and changes

1. Direct normal Stop: accepted seal input persists as JSON `null`; its nonzero byte length incorrectly made the boundary reconciler treat it as an unfinished operation. The fix recognizes JSON null while retaining rejection of pending/nonempty intents. Regression covers the serialized operation. No media relaxation or synthetic segments.
2. Console: library distinguishes uploaded recordings from completed/processing live replays; drafts/active direct sessions are not library items. Key selector/actions belong to the monitor, title/details to Stream Settings, sharing to the header. Retention is the only general setting. Direct has no delete/emergency control.
3. Keys: CMS-owned versioned AES-256-GCM encryption; existing hashes remain authentication identities. Human write-scoped, audited, no-store reveal. Legacy keys are retained after a successful authenticated K4 output open, without rotation or source revision change. Before that, `409 stream_key_not_retained` is expected. Configuration/cipher failures remain unavailable, not fake legacy state.

## Preserved Windows evidence

Installed plugin `667ee287e75ed9382624938eb2360bf3fe70eb48`; ZIP SHA-256 `86f5f216a1d74bd445c55c7e3a8c10198feb68fec1f44c71657e6132c93beab6`.

Direct recording `89cf022d-e80e-479f-9bbe-d8590771cc59`, capture `32264350a74315406f3d119a72023cce`: normal Stop/seal/ready succeeded, public end was missing. Scheduled recording `d1618202-89ec-44c8-9082-0d7fcb242040`, capture `6c69d79ff2981de8d40380b2b926bd8f`: `[4,10)`, 180.18 seconds, archived/published, same-URL VOD decoded. Both are preserved; no manual End/publish/reset/delete.

## Minimal Windows follow-up

- Confirm current meeting schedule and valid testing authorization before real OBS. Do not infer continuing authorization from a previous time window.
- Keep the installed reusable key. No plugin login, event selection, title entry or wire update is required.
- One short direct test: open/reopen unused direct Console and retain the same URL; start output, verify signal/automatic public start, then normal Stop. Observe automatic End, fixed public boundary, seal, ready, natural publication, same-URL replay and actual decode. Record timestamps with UTC+8 and plugin/recording/capture/output IDs.
- After authenticated output open, verify legacy key can be explicitly copied to the authorized operator without resetting it. Do not record key bytes in reports/screenshots/logs. No eye/reveal UI is present; copy retrieves securely without rendering key bytes in the page.
- Confirm completed live appears in Live library, unused direct does not; monitor selector/create/manage, metadata under Stream Settings, header share, retention-only Settings.
- Do not rerun passed scheduled flow, player, 16-minute recovery or 2.5-hour media acceptance for this change. Keep historical 379/388-second lag and weak-network seek limitations separate.
- No-Console direct creation, response-loss/restart and concurrent Console cases remain distinct unless previously evidenced. Report untested cases explicitly.

## Release evidence

- Audit #22: merge `65d36f0f5ef2ada6ea64e6b9432ee79d05c44726`, CI 38842729564, release 38843173619 succeeded. `audit-log--release-38843173619-1`, image `sha256:0ca403c4b2f892b69a212f80638d04528323ba6adf8ad49879c73c84888e1306`, healthy, 100% traffic.
- Gateway #168: merge `cc40e588ab3cb7569f308c297ca3f806dc9c00bb`, CI 38842784830, release 38844809330 succeeded. `api-gateway--0000209`, image `sha256:74bb9a2709d8a98e0b0012a8d623d42853a2535757d8face443cc854a4ec21d8`, healthy, 100% traffic; Admin 200 and unauthenticated reveal 401. First release stopped before deployment due to stale nonsecret rollback/audit prerequisite variables; updated only those four variables to verified deployed values before retry.
- CMS #228: merge `f1e9a0f5d5f306917020ad28f3e7b8102c658cdf`, CI 38844473218, release 38847036396 succeeded. `hhc-web-api--0000202`, image `sha256:10693a73e2340378ffecbd7516b3a90b9f7693489861f8c8a40d5f9613e49bdc`, healthy, 100% traffic. Writers, Studio writers, interactions and analytics remain true. Versioned key encryption v1 uses a dedicated Key Vault reference; no credential values recorded. Admin OpenAPI SHA-256 `81c25f1a40979082480cb9ef7cd22b759fdbfc1de28acca064a5bcea6e558511`.
- Client #126 (previous released client): merge `347b42727307955d03b82e03ae572a439be32b4e`, CI 38842785789 and v1.0.68 release 38847848860 succeeded.
- Admin #241: merge `afc1e7a446c8eec44d778c8706318f5500321782`, CI 38850857334 succeeded. Release 38851613122 succeeded at 2026-10-16T04:48:27Z; public bundle `/assets/index-DnuFafbw.js` contains the full merge commit and reveal route. Local 118 files / 1205 tests; independent targeted re-review 9 tests. Build/lint with published client passed.

## Additional finding during live readback

At 12:19 UTC+8 the original direct recording naturally entered processing, final [0,10), archive ready. Publication remained pending because the two VOD auto/live-auto cover jobs had already exhausted their retries while End was absent. Authenticated Admin cover discovery confirmed both groups failed; six worker conflict exits in 01:51–02:03 UTC corroborate the code path, without per-cover log attribution. Asset #109 gates automatic VOD cover claims on the final published range and has a one-time strictly fenced recovery for only those two cover IDs. It does not change CMS publication or any media bytes. Asset #109 release 38852562080 succeeded, merge 7875b25f7d0ca9a135092a86278752020421033a. Original direct naturally archived/published version 4 at 2026-10-16T05:15:54.693231Z (13:15:54.693231 Asia/Taipei), selected cover 40d710ca35646d68c18bd54684693113-1. No manual publication or End. Ready was 2026-10-16T01:51:17.232848Z; this recovery elapsed time is historical incident recovery, not fresh normal-Stop performance.


## Earlier production browser smoke (Admin #241; final #242 smoke below)

At 2026-10-16 12:50–12:54 Asia/Taipei, authenticated read-only browser checks confirmed monitor key selector and create/manage list; metadata under Stream Settings; header share before avatar; direct/scheduled tabs and return; library Create dropdown; retention-only Settings. Existing direct processing and scheduled archived rows appear in Live library. No key was reset, no new event created, no setting saved, no diagnostic recording mutated. Native OBS key reveal/backfill and fresh normal Stop remain Windows checks.

## Current backend readback

2026-10-16 14:14 Asia/Taipei: Asset asset-api--0000098, image digest sha256:2a49f6871b1154da1ad3cee7074cc2c2d1defb45a5ea8db15a98cc9352c7c152; recording worker sha256:b263d39c407f9ad3e7bbf11bc2e0b03c94b45167b8b2e79434394b1fd018609c, existing every-minute schedule, parallelism 1. API healthy/running/active, 100% latest traffic.

CMS current revision hhc-web-api--0000203, image sha256:572cba09a9c049b1ba1df9dfb9ea72a4d5281f5ab6c3e01e2736feaa908b1858, healthy/running/active, 100% latest traffic. This includes subsequent CMS #229 merge 48120b32db29d97d8023cd5e10def3214c56de3a in addition to the #228 repair.

## Control-room UI completion

Admin PR #242 and frontend-platform PR #127 complete the 20-area audit. Monitor stays 16:9 across waiting/loading/error/processing/archive; keys use selector/reset/copy with no plaintext view; create/manage dialogs use consistent footer and named actions. Details use one outlined form and one atomic Save for metadata and staged thumbnail selection. Custom upload is conditional on a single switch. Stream Settings/Analytics/Health are the only monitor tabs; internal IDs and diagnostics are not operator-facing. Direct/scheduled entry and return navigation are consistent; completed live keeps its original URL and Live classification. Scheduled creation distinguishes new/reuse and supports management even without keys. General Settings contains retention only.

The shared Menu repair is necessary: real-browser second-click dismissal failed despite the initial component tests. The final fix registers against the mounted trigger document and reads the delayed portal reference at event time; browser verified three open/close cycles and Escape focus restoration. Regression covers delayed portal mounting.

Browser local mock checks cover metadata-only and combined cover Save, upload/crop cancel and accept, dirty Cancel/discard, key creation/rename/delete cancellation, scheduled create/reuse, loading/error/empty states, three tabs, light/dark, and 1280/1440/1920 desktop widths. Custom-thumbnail overflow exposed and fixed an inaccessible dialog footer. Local fixture renders are not real OBS decoding evidence.

Independent review additionally corrected definitive-save-rejection lock and unchanged pending-cover blocking; newly staged cover readiness is not overwritten by old props. Replay thumbnails reuse existing authenticated bounded retry instead of swallowing transient 404 indefinitely. No broad retry or permission bypass was introduced.

Remaining limitations: frozen BroadcastView does not expose every actual recording timestamp; missing values are labelled unavailable. Native datetime segments were entered by real pointer/keyboard and produced a valid value; full native popup calendar selection was not exercised. Live chat, charts and unsupported YouTube transport/vertical/monetization controls are intentionally absent. Current aggregate statistics remain real data. Real OBS signal and fresh direct normal-Stop behavior require the focused Windows check.

Local final Admin verification: 120 files / 1226 tests passed with two workers; build/lint and delivery policy checks passed. The unconstrained parallel local rerun hit resource-contention timeouts and was stopped; no tests were skipped or thresholds relaxed. Admin PR #242 exact 7e3d5b8 CI also passed. Shared UI PR #127 exact 22057664256e7ad711d38b41ffb440b3dc8b0ab7 CI 38860536410 passed. Final published-dependency CI is recorded separately below.

Shared UI PR #127 merged as `364ba6232ee4984a2ae89bb23baec0f846819a66`; official v1.0.69 release run `38866394135` succeeded. Authenticated registry readback and Admin install confirmed published UI 1.0.69.

## Windows copy/paste checklist

請接續本文件固定版本做最小重測，先確認目前不在聚會時段且實機授權仍有效。先前 18:30 的期限不是永久授權。

本次不用修改外掛 wire、重新登入、選場或輸入 title；沿用原固定金鑰，不重設。請先記錄實際安裝 plugin commit，不能用候選版本代替。

1. 一場新的短直接直播即可。若現有來源已設定直接直播，可先關閉 Console 後啟動 OBS，確認後續開 Console 指向同一場；這可補無 Console 起播證據。若此前條件不成立，先從 Console 準備直接直播，將無 Console 案例標為未驗證，不額外修改來源模式。
2. 原 Console 保持開啟：確認訊號與自動開播；正常停止 HHC 輸出，不手動 Console End／publish。觀察自動 End、公開邊界、Stop／seal、ready、自然發布及同 URL 實際 VOD 解碼。各階段紀錄 Asia/Taipei 時間及 recording/capture/output ID。
3. 若使用舊金鑰，成功認證開啟後，只在需要時驗證安全複製可用；勿將金鑰放入報告、截圖或 log。不可重設金鑰來代替驗收。
4. UI 僅做真訊號下確認：監控 16:9、金鑰選單二次點擊收合、詳細資料同一次儲存、結束後 Live 分類與原 URL 延續。Mac 已驗證一般對話框與版面，不需重跑整份 UI 測試。
5. 已通過的預排、16 分鐘續傳、播放器指定流程與 2.5 小時長場不重做。保留歷史 379／388 秒落後、弱網 seek 與未驗證的正常聚會＋YouTube 負載限制。

請回報實際 plugin commit、recording/capture/output ID、含時區時間、解碼解析度與播放位置，逐項標示通過／失敗／未測。既有診斷場與指定原場暫保留，不刪除、不重送 Stop／seal、不手動發布；所有紀錄不得含金鑰、token、cookie、密碼或 signed URL。

## Final consumer release

Admin PR #242: head d385cdaf97af43363cef333e2314c76177f2168e; CI 38870086337 succeeded. Squash merge 2036b6f36d4dfa4b05fed1b6dbc1fe2a0903ad5e at 2026-10-16T08:02:27Z. Production Release 38871049057 succeeded at 2026-10-16T08:13:36Z; public /assets/index-tkzuxN1o.js returned 200 and contained the full merge commit. Final published-package local suite 120 files / 1226 tests, build/lint/policy/Bicep checks passed.

Fresh Azure readback at 2026-10-16 16:04 Asia/Taipei confirmed the same CMS 0000203 / Asset 0000098 images above, both Healthy/Running/active with 100% latest traffic. Recording worker image unchanged; cron */1 * * * *, parallelism 1. BROADCAST_WRITERS_ENABLED, STUDIO_WRITERS_ENABLED, VIDEO_INTERACTIONS_ENABLED and VIDEO_ANALYTICS_ENABLED all true. No production configuration was changed in this UI release.


Final UI smoke 2026-10-16 16:14–16:18 Asia/Taipei confirmed authenticated archived control-room loading, monitor ratio 1163/654.1875 = 16:9, no horizontal overflow, no key eye control, real second-click menu dismissal (after exit animation), key manager, the three monitor tabs, a 718px details dialog with 1px textarea border, exactly one thumbnail switch and one Save action. Live library showed only the two completed test events; Settings contained retention only with the requested deletion note; Create dropdown contained upload/create-live. No production writes were performed.

This smoke identified two remaining archived-state issues, fixed in Admin #243: the old live cover snapshot could be missing despite a selected ready VOD cover; the health badge could show a current interruption after archival. Shared BroadcastCoverPreview now reads the selected ready VOD cover for archived views, retaining authenticated scope, bounded retries, JPEG validation and object URL cleanup; active streams keep the live cover endpoint. Terminal/processing health badges use lifecycle state. No selection/data mutation or permission fallback. Independent review found no material issues; 121 files / 1230 tests, focused 49 tests, build and lint passed. PR #243 head faff21c2e1d25682b216b6615ab944a15cf2c198 CI 38873241702 succeeded; merge 2a000ce515b11958a8f98fa3f90cadf064499270 at 2026-10-16T08:31:52Z. Release 38874312198 succeeded at 2026-10-16T08:42:18Z. Public site and /assets/index-B-2irZYE.js returned HTTP 200; the bundle contains full commit 2a000ce515b11958a8f98fa3f90cadf064499270.


## Final production acceptance and entry points

At 2026-10-16 16:43–16:44 Asia/Taipei, authenticated browser read-only smoke loaded both retained replay-list thumbnails at 1280×720, including the original missing-live-cover case. Its control room loaded the selected VOD thumbnail at 1280px natural width, measured 1133×637.3125 (16:9), no horizontal overflow, and displayed current Archived state. Historical signal interruption entries remain historical facts. No record update, manual End/publication, key reset or deletion was performed.

Admin: https://admin.alive.org.tw/content/broadcasts . Member playback: https://www.alive.org.tw/zh-Hant/member-videos/{recordingId}. Use the existing authorized test account; Console permissions remain cms:recordings:read/write/publish, membership playback entitlement remains required. OBS uses its existing fixed key, with no new OAuth/login requirement. No account grant or auth model was changed by this release. Privacy draft was not published.

Production browser automation verified DOM, layout geometry and interactions. Local candidate screenshots were inspected and are committed in Admin audit docs. The final production screenshot API timed out; do not represent those absent captures as screenshot evidence. This does not replace Windows real-signal playback evidence.

Current Admin deployment is #243 (2a000ce...), including #242; earlier #241/#242 release entries above preserve chronology only. This document-only CMS change does not deploy CMS: release workflow excludes docs/**. Current runtime CMS remains 48120b32 / hhc-web-api--0000203, with #228 repair included.
