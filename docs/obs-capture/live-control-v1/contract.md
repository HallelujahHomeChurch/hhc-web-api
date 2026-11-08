# B1 直播控制契約設計

版本：`b1-2026-10-10.draft1`。狀態：設計固定基線，尚非可用API／OpenAPI正式快照。不得以此宣稱部署或整合測試就緒。
Spec：[直播控制室](../../superpowers/specs/2026-10-10-live-control-room.md)。所有路由是新增規劃，C1原路由不修改。

## Common

同源`/api/admin`，OAuth scopes沿用`cms:recordings:read/write/publish`且與即時使用者權限交集。B1人員專用；不擴大CLI service principal。read讀取；write建立／編輯／bind／ack；publish另需用於公布／start／end／autoStart／autoPublish／emergency-close。OBS bind有曝光策略時也需publish。Admin操作者可不同於capture owner，但ack／上傳必須是原綁定actor及epoch。

JSON camelCase；UUID字串；時間RFC3339 UTC；sequence非負int32；epoch/revision正整數且不超JS安全整數；未知欄位拒絕。requestId/audit provenance採既有middleware。禁止token／signed URL落log。
所有mutation必須operationKey（沿C1長度及字元規則）。resource＋actor＋operationKey scope內，相同操作／相同正規化body重送回原receipt，重用key不同body為409；冪等lookup先於revision檢查，避免成功後重送變412。
B1 metadata／控制mutation body有expectedRevision（create除外）。command ack不驗當前metadata revision，而驗commandId＋epoch＋captureId；編輯標題不得使已合法marker失效。metadata与控制共用broadcast revision；讀回衝突後重新確認意圖才用新operationKey。
成功response採CMS既有envelope，以下定義其data payload；mutation為`{broadcast, receipt}`，receipt=`{operationKey,operation,state,commandId?}`，state=`accepted|completed|rejected`。GET取得最新broadcast；replay receipt維持原接受結果，不假裝原response會隨狀態更新。

## BroadcastView

`{recordingId, revision, phase, announced, scheduledAt, title, description, policy, cover, binding, boundaries, health, pendingCommand, archive}`。
- phase採spec列舉。scheduledAt為RFC3339或null（立即場次）；title/description沿既有recording限制。
- policy=`{autoStart:boolean,autoEnd:boolean,autoPublish:boolean}`。
- cover沿用既有auto/custom及uploadId規則，新增broadcast scope，不重新定義圖片上傳協定。
- binding=null或`{captureId,epoch,actorId}`；actorId僅staff response。metadata範圍不回傳credentials。
- boundaries=`{startSequence:null|integer,endSequenceExclusive:null|integer,revision:integer}`。
- pendingCommand=null或`{commandId,kind:'start'|'end',captureId,epoch,requestedAt,status:'pending'|'acknowledged'|'cancelled'|'rejected',boundarySequence:null|integer}`。
- health=`{state,lastConfirmedAt,lastPublishedAt,verifiedEndSeconds,queuedObjects,reason}`；未知timestamp／progress明確null，不用0冒充實測。對會員只提供必要狀態。
- archive=`{state:'none'|'processing'|'ready'|'published'|'failed',reason:null|string}`。

## Endpoints（data request → response）

| Method/path | Request | Success |
|---|---|---|
| GET `/api/admin/broadcast-capabilities` | 無 | 200 `{versions:['b1-2026-10-10.draft1'],maxActiveCaptures:1,markerMode:'next-common-segment'}`；實作冻结後version改正式B1 |
| POST `/api/admin/broadcasts` | `{operationKey,title,description,scheduledAt,policy,cover}`；cover省略則snapshot defaults | 201 mutation，原子建立recording＋broadcast，announced=false、phase=draft |
| GET `/api/admin/broadcasts?cursor=&limit=20&filter=selectable` | cursor沿現有pagination；limit 1–100 | 200 `{items:BroadcastView[],nextCursor:null|string}`；selectable僅未終止且可bind，仍須bind時交易檢查 |
| GET `/api/admin/broadcasts/{recordingId}` | 無 | 200 BroadcastView |
| PATCH `/api/admin/broadcasts/{recordingId}` | `{operationKey,expectedRevision,title?,description?,scheduledAt?,policy?,cover?}` | 200 mutation；bound後policy拒絕修改；title/description同recording交易更新 |
| POST `.../{recordingId}/announce` | `{operationKey,expectedRevision}` | 200 mutation；draft→scheduled或保留preview |
| POST `.../{recordingId}/cancel` | 同上 | 200 mutation；尚未bind才可cancel；有capture先依C1 abort並確認終止，防止留下上傳 |
| POST `.../{recordingId}/bindings` | `{operationKey,expectedRevision,protocolVersion}` | 202 mutation，持久化create intent後建立既有C1 capture；GET直到binding非null才開始上傳。capture只建立一次，失敗有可恢復receipt |
| POST `.../{recordingId}/start` | `{operationKey,expectedRevision}` | 202 mutation＋start command；需announced、bound、非autoStart、無停止意圖 |
| POST `.../{recordingId}/end` | 同上 | 202 mutation＋end command；無公開起點拒絕；已正常Stop也可由inventory證明終點 |
| POST `.../{recordingId}/commands/{commandId}/cancel` | `{operationKey,expectedRevision}` | 200 mutation；僅尚未ack start可取消，禁止撤銷已公開邊界 |
| GET `.../{recordingId}/control?captureId=&epoch=` | 僅綁定actor | 200 `{revision,phase,pendingCommand}`；active每2秒輪詢，失敗有界backoff至15秒，無active capture停止 |
| POST `.../{recordingId}/commands/{commandId}/ack` | `{operationKey,captureId,epoch,boundarySequence}` | 202 mutation；原actor、匹配command、start/end順序，boundary待媒體驗證；同command不能改boundary |
| POST `.../{recordingId}/emergency-close` | `{operationKey,expectedRevision,reason}` | 202 mutation；先durable拒絕新grant並發布revocation，GET確認封鎖生效；不等OBS ack、不自動發布VOD |
| GET `/api/member/recordings/{recordingId}/watch` | 原會員權限 | 200 `{recordingId,title,description,view,scheduledAt,serverNow,cover,stateRevision}`；view=`waiting|live|processing|vod|cancelled|expired|unavailable`；不回傳preview；無權仍401/403、不洩metadata |
| POST `/api/admin/broadcasts/{recordingId}/preview-access` | `{captureId,epoch}`，既有staff播放scope／CSRF規則 | 200 既有播放grant envelope，purpose=staff-preview、不可互換member grant |

其餘媒體宣告、confirm、sign、Stop／seal／abort繼續C1；綁定流程server持久化必要C1create key及request，不要求OBS偷偷重算body。bindings GET未就緒不得另行create capture。正常Stop後phase透過平台reconcile推進。
解绑重試透過新binding請求：僅舊capture已終止且從未公開任何segment，交易增加epoch並保留歷史；不提供任意替換active binding。

## Error semantics

沿用CMS標準錯誤envelope與requestId，新增code：
- 400 `broadcast_invalid`：欄位／時間／sequence格式錯，不重試相同輸入。
- 401/403：沿用auth錯誤，禁止在無權狀態反覆送marker。
- 404 `broadcast_not_found`：不存在／不可見；不得fallback建立新場。
- 409 `broadcast_state_conflict`、`broadcast_already_bound`、`broadcast_active_limit`、`broadcast_epoch_mismatch`、`broadcast_boundary_conflict`、`broadcast_operation_conflict`：讀回狀態，禁止盲目換key重試。
- 412 `broadcast_revision_conflict`：重新GET並讓操作者／明確流程確認新狀態。
- 422 `broadcast_protocol_unsupported`：禁止bind；要求外掛升級，不靜默降級自動公開。
- 429/503：遵守Retry-After，以同operationKey有界退避；保留journal。
- marker若指向未到達的合法未來段，保持pending validation；若最終inventory證明start超尾、end≤start或end超尾則failed且不公開。不得調整原duration使其通過。

## Asset private producer extension（Mac內部，同樣需OpenAPI契約測試）

- PUT `/priv/recording-captures/{captureId}/broadcast-range`：CMS Dapr caller only；body=`{recordingId,epoch,rangeRevision,startSequence,endSequenceExclusive,revoked}`。start/end允許null表示尚未公開／尚未結束；revision正整數。首次建立範圍start=null禁止會員媒體。相同epoch/revision/body回200，較舊revision或不同body同revision409；新epoch須合法重新綁定且尚未播出，由CMS及Asset持久關係共同驗證。
- GET `/priv/recording-captures/{captureId}/broadcast-projection`：200 `{epoch,rangeRevision,state,startSequence,endSequenceExclusive,mediaStartSeconds,durationSeconds,revision}`，state=`pending|ready|failed`；未就緒時間欄位null，不虛報0；兩個revision分別是控制範圍與實際媒體投影。
- 輸入通過後原子持久化範圍與發布工作；200表示已接受持久狀態，GET ready及rangeRevision匹配才代表Worker可觀察政策。emergency-close在CMS先拒新grant、再讀回Worker政策確認封鎖，UI在此之前維持正在關閉。
- 範圍未結束可持續推進verified pointer，但freeze end後禁止擴大。marker尚未被完整媒體證實時保留pending；保持正常完整驗證。
- private錯誤沿既有Asset envelope：400 invalid、403 caller拒絕、404 capture不存在、409 range/epoch conflict、503 unavailable；公開Gateway全部拒絕`/priv/*`。

## Ready gate

M0產出canonical OpenAPI完整components／examples／所有上述error、consumer mock、JSON fixtures及SHA256 manifest；Windows回覆version／commit／hash與marker實作可行性後標為b1-2026-10-10.1（若日期已變，採正式冻结日），不再改同版本bytes。現有C1manifest／fixtures byte hash不變。
此資料夾manifest只固定規劃文件，不是上述ready gate。
