# B1 Mac 實作候選交付

本交付包版本為 `b1-mac-2026-10-10.rc2`；OBS `protocolVersion` 仍為
`b1-2026-10-10.rc1`。前者是文件／實作快照版本，後者是 capabilities 和 bindings
使用的 wire 值，不能混用。C1 仍為 `c1-2026-10-08.2`，inventory schema 1。

**目前是 PR 實作候選，尚未合併、發布或部署；沒有可宣稱已上版的 B1 revision。**
Mac 不等待 Windows W0 才開發；Windows marker/journal 的真實實作相容性仍需確認。
新建 B1 writers 預設關閉。Windows 真機／GPU 測試保持暫停，待使用者通知。

## 固定檔案

- `openapi.snapshot.yaml`：20 個 CMS 路徑，取自本次實作 canonical OpenAPI，含完整 request/response、錯誤、權限與引用 schema。
- `asset.openapi.snapshot.yaml`：4 個私有 Asset 路徑及完整引用 schema。Windows 不直接呼叫。
- `wire-fixtures.json`：保留 rc1 的 public command/marker fixtures；未改動 C1 fixtures。
- `implementation-manifest.json`：來源 commit 與 SHA-256。下載固定 commit 內的 manifest，再核對其列出的檔案。
- 上一層 rc1 snapshot/manifest 保持原 bytes。rc1 manifest 中的可變程式碼 hash 是在
  `39dd5a7d3797cf98b51fbe55cb7c6163b3fe1b91` 的快照，不能拿後續 HEAD 冒充同一版本。

## 本次相對 rc1 的補充

公開 OBS create/bind/control/ack wire 不變。會員 watch 新增 optional nullable
`recording`、`live`、`mediaStartSeconds`、`durationSeconds`，使獨立網址可直接取得播放
模型，無須先在列表找到項目。補齊預約列表、場次封面上傳／讀取路徑。

Mac 私有 producer 增加：

- capture create 的 optional `broadcastEpoch`：B1 在建立時就初始化 deny-all；省略仍走原 C1。
- range 的 `memberState = blocked | live | vod`：公開結束但尚未發布錄影時，舊直播憑證亦不可讀取；公開結束仍待補傳時保留已驗證前綴。
- preview 的獨立 `staff-preview` purpose、epoch/path 及限時 scope。不可交換成會員 grant。

Windows 不送這些私有欄位。舊 C1／CLI 不需改碼。三畫質 30000/1001 fps、900 幀／30.03 秒
分段、原始 inventory/digest、媒體完整驗證及冪等規則不变。

## Windows 實作介面

1. 登入沿用 `hhc-obs` public client、PKCE S256 和既有 Windows loopback 設定。
   需要 `cms:recordings:read`、`cms:recordings:write`；公布、開始／結束、緊急關閉及曝光策略
   另需 `cms:recordings:publish`。只帶 OAuth scopes 不會替帳號新增權限。
2. GET capabilities，確認 `b1-2026-10-10.rc1`，從 selectable 場次選擇並 bind。
   bind 回 202 不等於 capture 已就緒；GET 到 binding 非 null 才開始 C1 上傳，勿另建 capture。
3. active capture 每 2 秒輪詢 control，失敗依既有契約有界退避；無 active capture 停止輪詢。
4. 收到 start/end，依 OBS 當下 muxer 的下一個三畫質共同分段邊界選 marker，先耐久保存 journal，
   再用原 operationKey ACK。不能以平台落後的 verifiedSequence 代替本機實際邊界。
5. 例：start=8、endExclusive=20，公開範圍就是 [8,20)，段 8–19。
   End 只關閉公開範圍，不停止 OBS、本機錄影或原 YouTube output。
6. ACK 只確認命令／邊界；start 仍需範圍內三段完整 verified 才轉 live。
   title 編輯不使已合法 command ACK 過期；capture、epoch、command 必須匹配。
7. 正常 Stop／seal／補傳維持 C1；autoEnd 由最終 inventory 決定尾界，不能用當時落後水位截短。
   已 ACK 明確 End 不被後來 Stop 擴大。never-public 場次不把彩排自動發布。
8. 重啟後重播原 journal/key。409／412 先 GET 釐清，不盲目換 key；晚到取消命令或舊 epoch ACK 拒絕。

目前現存 `tools/broadcast-mock` 仍是 rc1 consumer mock；它可驗 Windows 控制流程，
不能作為本次真 DB／OAuth／媒體播放／私有 memberState 的證據，也不等於正式 API。

## 發布次序與啟用門檻

1. Asset API + Worker 的原子初始封鎖、公開範圍、memberState、preview purpose 先發布。
2. CMS 相容 readers／reconcile 發布，writers 保持關閉；Gateway 路由及 CSP 發布。
3. Shared client 正式 package 發布後，Admin/Web 更新 registry 版本及 lock，clean install/CI 通過再發布。
4. 驗證實際部署 revision、health、未登入/無權拒絕、會員播放、staff preview、範圍外與舊 grant 拒絕。
5. 以 reviewed configuration 開啟 writers；記錄 producer／consumer revision 後，才提供 Windows 正式測試入口。

PR 不等於 release。任何一環未完成都不能把 G1 或端到端驗收標成通過；不可略過失敗的 CI。
回滾先關 writers，保留讀取、收尾、revocation reconciliation 及新增資料表；已有 B1 記錄時
不能直接移除政策 producer 或 drop 欄位。

## 驗證證據與限制

Mac 本機已產生 29.97 fps 三畫質真解碼 fixture，完整驗證原 72 fragments；會員範圍
[8,20) 的 origin 為 240.261 秒、duration 為 360.36 秒。瀏覽器實際解碼、尾段拖曳、畫質切換、
非零原點、跟隨直播與主動暫停/DVR 分別驗證；詳見 Website `docs/broadcast-browser-validation.md`。
UI 桌面及 390px 檢查是瀏覽器模擬，不是實體手機驗收。

尚未通過：正式 producer 跨服務 smoke、既有 CLI 實際上傳完整 smoke、Windows marker 實機、
正常聚會負載、實際五分鐘斷線／十分鐘追趕、整场持續觀看、Safari native HLS。
不將合成投影推進當作真正斷網恢復。

原 Windows 失敗證據保留：capture `807adeb7faa8f073c4e6251baf6aa65e`，
復線後原播放器落後約 379 秒；後續共用負載報告約 388 秒。未在本輪真機重測，不能宣稱已修復。
Worker 排程／容量沒有因本功能擴容，也未新增 event bus。

## 最後綜合驗收

先一般聚會實際負載（OBS＋原錄影＋需要時 YouTube），再分開做競爭負載壓力測試。
檢查預約／固定網址、preview 不外洩、marker 前後不可讀、正常開播、Auto/手動畫質、斷線自動恢復、
DVR/暫停/倍速語意、Stop/seal/補傳、轉 VOD／自動發布、封面與標題延續、舊 CLI/C1。
延遲以實際解碼位置量測；2–3 分鐘且不持續累積是正常使用方向，原 ≤120 秒及五分鐘斷線／十分鐘
追趕結果仍獨立列報。不用改門檻或只看 live badge 宣稱舊失敗通過。
