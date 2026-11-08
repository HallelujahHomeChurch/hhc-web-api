# K4 first VOD handoff and Studio monitor sizing — 2026-10-16

## Scope

This follow-up preserves the passed normal Stop, automatic End, ready and natural publication from Windows commit `71a863a7f113bb17db40ba1edfe933cf8f371dab`. No recording, key or publication was manually changed. Existing diagnostic recordings must remain available.

Windows source: https://github.com/HallelujahHomeChurch/hhc-obs-plugin/blob/71a863a7f113bb17db40ba1edfe933cf8f371dab/docs/reviews/mac-k4-vod-transition-followup-2026-10-16.md

Recording `3e93b740-1915-4cad-b5b6-c379107c2b06`, capture `57764974a1111951f32c81b64f8a9aa5`, output `abeb1a4f-a053-4c18-bb09-64c30fe95f3d`; installed plugin `667ee287e75ed9382624938eb2360bf3fe70eb48`.

## Findings and changes

1. CMS publication writes a new VOD authority revision before Asset applies it. Previously watch/list could expose VOD using the older ready projection. The regression reproduces premature VOD exposure. Member VOD now requires non-revoked, announced VOD authority, no pending range, acknowledged current revision, matching epoch and exact public boundaries, and a ready projection. Asset grant checks and complete media validation are unchanged.
2. The retained logs show member-playback 409 at 2026-10-16 17:56:17.011583 Asia/Taipei (Gateway ingestion timestamp, request `d26832ec284d357b2d66019185ba6c3b`) and 200 at 17:56:38.0841114 (`56e32b999026ae34d45836acb4bb8438`). Matching CMS logs have the same request IDs/statuses. Gateway paths are sanitized; no historical Asset error body or grant-rejection trace was retained. The exact internal rejection branch for this incident is therefore unproven; the reproduced authority race is not presented as uniquely proven historical causation.
3. A failed Continue-to-recording action used the generic media-error Retry and could rebuild the ended replay. VOD errors now have a separate Retry that repeats the handoff, preserving the existing player and bookmark (position, paused state, rate and quality). Genuine media failures retain their own recovery behavior.
4. Studio monitoring remains 16:9 with a responsive size cap: at 1280×720 the preview is 640×360; at 1440×900 and 1920×1080 it is 854×480.375. The monitor is left aligned, key controls remain within it, settings tabs fit in the first viewport, and there is no horizontal overflow. Member watch-page player size is unchanged.

## Validation

- CMS: regressions reproduced the old readiness defect, then passed; recordings/PostgreSQL/HTTP suites pass with race detector, vet passes. Published-authority fixtures updated; no schema or migration.
- Website: 146 files / 1,216 tests, lint and production build pass. One existing Next navigation lint warning remains. New false-to-true retry test confirms the same player/bookmark and no live session restart. Independent code review found no Critical/Important issue; a separate thrown-promise regression was not added.
- Admin: lint/build and browser geometry checks pass. Full local suite hit load-sensitive timeouts (4 failed / 1,226 passed); the three affected suites subsequently pass in isolation (126 tests), with no timeout relaxation. Required remote CI and release results are recorded below.
- Local/browser readbacks and deployment smoke do not replace Windows first-transition media acceptance.

## Frozen contracts and access

K4 `k4-2026-10-15.rc1`; K3 `k3-2026-10-14.rc1`; B1 `b1-2026-10-10.rc1`; C1 `c1-2026-10-08.2`; inventory 1. Preserve 30000/1001 fps, 900 frames / 30.03 seconds, digest/idempotency, membership authorization and `[start,endExclusive)`.

Frozen K4 handoff at CMS commit `5e8c33ec1b3ca5628605fab0a578ca8312d67d1d`, `docs/obs-capture/studio-v2/windows-handoff.md`: SHA-256 `d63a5d3b5d6e2e7835e0e1f162fed3a2050f510a94d44759bfd4fdc30d064c2e`. Manifest SHA-256 `e984a5a54b80c817ba701f6923d73c02b57bf9c5b7d238f15442543c5ae18464`. CMS OpenAPI SHA-256 `81c25f1a40979082480cb9ef7cd22b759fdbfc1de28acca064a5bcea6e558511`.

Admin https://admin.alive.org.tw/content/broadcasts ; member https://www.alive.org.tw/zh-Hant/member-videos/{recordingId}. Existing authorized Console account needs `cms:recordings:read/write/publish`; viewers need their existing member-video entitlement. No new grants or OAuth settings. OBS retains its fixed key, without login/event/title steps. Privacy draft remains unpublished.

## Windows minimum retest

請先確認隨附平台回報已完成上述固定 CMS release 的實際 revision／digest／health 讀回，再核對目前聚會時段與實機測試授權。先前 18:30 的時間窗不是永久授權。本次不要求更新外掛或重設金鑰。

1. 既有已發布場可先確認同 URL VOD 仍可播放；這不算首次轉換驗收。若需要重現首次轉換，只新增一場短直接直播即可，不重跑整份正常停止／長場驗收。
2. 保持原會員頁，正常收尾後不 reload。等待平台提供「繼續觀看錄影」，第一次點擊應直接取得 VOD 並實際解碼。記錄 watch view、playback HTTP 狀態、含時區時間、currentTime/duration、解析度、frames、readyState、paused、rate、MediaError；不可記錄憑證或媒體 signed URL。
3. 轉換前先保留一個可辨識的回看位置、暫停與 1.5 倍速，確認轉 VOD 保留操作語意。若第一次失敗，保留原頁與證據，再按畫面「重試」；確認重試 VOD 而非重建舊 replay。第一次失敗仍標失敗，不能用第二次成功改寫。
4. 真訊號下只補看控制室監控尺寸：16:9、有上限，設定頁籤與金鑰控制可正常操作。一般對話框不重測。
5. 不重跑已通過的預排、正常 Stop、自動 End／自然發布、16 分鐘續傳、指定切畫質與 2.5 小時收錄。歷史 379／388 秒落後、弱網 seek／連續播放、嚴格 120 秒壓力指標及正常聚會＋YouTube 負載仍分開保留。

回報實際安裝 plugin commit、recording/capture/output ID、部署版本、Asia/Taipei 時間與上述實際媒體數據；逐項標示通過／失敗／未測。此份修正沒有證明無既存草稿時的 Console 關閉自動建場，仍沿用原獨立驗收狀態。所有既有診斷場及指定原場繼續保留，不刪除、不手動發布、不重送 Stop／seal、不重建取代失敗場。

## Deployment evidence

- Admin #244: head `a0b39bbd2f156beb780d5f33825ad248b5ab4b88`, CI `38886837841` passed, merge `f47e208a19a8f6b8bf314e61854fb799b8c531e4`. Production Release `38887559728` succeeded at 2026-10-16T10:39:58Z (18:39:58 Asia/Taipei). Public `/assets/index-cqUsqYX4.js` returned 200 and contains the exact commit. Authenticated production geometry readback at approximately 18:42 Asia/Taipei confirms all three viewport measurements above, no injected style, no overflow, tabs visible. Production screenshot was captured and visually inspected.
- Website #215 merged as `2ccbd3689ae6a38f62e2cfc80e253371ef074c70` after CI `38886728286` passed. Release `38887383160` failed verification before deployment: an existing test checked native video src before asynchronous hls.js initialization completed. #216 retains the same exact expected src and waits for it (no timeout relaxation). Its head `bcae051cf0b9eb7c7cd21f2c530ad3bdb919d15b`, CI `38887929097` passed, merge `d94c89d69eb8fea1b0d8a2f72566fed62462874f`. Final runtime deployment evidence follows.

- Website final release `38888503488` succeeded at 2026-10-16T10:46:34Z (18:46:34 Asia/Taipei), revision `hhc-web--0000211`, image `sha256:d2e2ccfe288544d113a49df9c78e1bbd12bc5fb2a7afccb68f9832c3405bdc0d`. Azure readback: sole active revision, Healthy/Running, three replicas. Website has no direct ingress traffic allocation (reported revision trafficWeight 0); Gateway invokes it through Dapr. Do not describe this as a public 100% ingress rule. Release Dapr health checks passed; public `/health` returned Gateway healthy, not Website-specific health. Loaded public JS contains full commit `d94c89d69eb8fea1b0d8a2f72566fed62462874f`.
- Authenticated existing-recording smoke at 2026-10-16T10:48:12.369Z: 854×480, currentTime 2.017254, duration 218.4182, 70 decoded frames / 0 dropped, playing at 1×, readyState 4, MediaError null. This was a reloaded VOD page and is NOT first-transition acceptance. Paused after the observation. No diagnostic recording data was changed.

- CMS #231: head `ee46af1a0fbcd2bfb30cfcd4a139e4087aed6609`, CI `38886952097` passed all checks (including full Go/race and isolated runtime verification); merge `463e6dda35fcf15901ca1a5f280bfa15216c579e` at 2026-10-16T10:53:23Z. Pinned production release: https://github.com/HallelujahHomeChurch/hhc-web-api/actions/runs/38890188348 .
- This file fixes the code/contract/retest scope. CMS revision, image digest, live health/traffic/flags and observation time must be read back after that release and supplied with the final Windows prompt. This file by itself does not assert that deployment or Windows acceptance succeeded. If the pinned release fails or another code fix is needed, do not start formal testing from this document alone.


## Newly identified management UI follow-up (analysis only)

The completed-live library still links titles to the control room, uses “Reuse details” as its row action (opens a new scheduled-event form), labels archived items “Replay”, and uses End requested/scheduled time instead of actual public start time. It also omits recording expiry. These are confirmed remaining UI/data-projection gaps, not fixed by the monitor-size/VOD patch. Desired next change: common thumbnail/title recording rows, Open directly to recording settings, actual publication/expiry, actual live start date, correct Live-tab return, and no completed control-room management screen. Source classification and member URL remain unchanged. CMS needs an authoritative read model for start/publication/expiry; do not invent historical timestamps or change retention arithmetic. No OBS wire change is required. This follow-up was analyzed at the user's request and is not represented as implemented.
