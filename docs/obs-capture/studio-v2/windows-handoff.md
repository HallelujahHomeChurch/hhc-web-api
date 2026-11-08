# Windows K4 parallel implementation handoff

Status: **可以開始本機實作／CI；不可開始正式 K4 OBS 測試。**

Read this folder's contract.md, openapi.snapshot.yaml, wire-fixtures.json, identity-fixtures.json, acceptance.json and manifest.json from ONE immutable commit. Validate manifest SHA-256 plus every listed artifact and unchanged legacy hash before work. The PR/commit link is supplied in the sending message; do not substitute a moving branch head.

## 可直接貼給 Windows Codex

請接續 HHC Studio K4 外掛本機實作；平台 Mac 端正在平行實作，尚未部署 K4。請先從本訊息指定固定 commit 取得 docs/obs-capture/studio-v2，驗證 manifest 與 artifacts hashes。契約版本 k4-2026-10-15.rc1；B1 b1-2026-10-10.rc1、C1 c1-2026-10-08.2、K3 k3-2026-10-14.rc1、inventory 1 保持不變。

新輸出採 POST /api/ingest/streams/open，body 僅 protocolVersion、outputSessionId、streamKey。直接用現有 durable localId 作 outputSessionId，第一次請求前持久化 K4 opening intent，不能只寫 active.json。特別修正 key-controller.cpp 的 retireUnbound()：K4 open 可能已建立遠端 capture，不能因尚無 resolved intent 就 released=true；timeout→取消→重啟仍必須保留同 ID 恢復及清理。平台負責找場／建場並完成 binding，200 回傳非 null 的 B1 broadcast/bind-completed receipt、K3 session，以及 sourceId/outputSessionId/version；不要再執行 K3 resolve/bind。後續 control／ACK／上傳／Stop／seal／renew 使用現有 K3/B1/C1。

驗證回應 ID、receipt key、capture/epoch 一致再保存，token 只存 OS vault。回應遺失、409 wait/busy/capacity、429、503、重啟均保留同 ID。公開 End 但 Stop/seal 尚未完成時，同 ID open 可取回原 binding/session 收尾；只有 transport settled/fenced 才 terminal，terminal 不自動新建；下一次明確 Start 才產生新 ID。已存在 K3 journal 繼續舊流程，不能升級重綁。取消時若遠端結果不確定，保留 stopping intent，恢復同 ID 後走原 cleanup，不丟棄。

OBS 仍只有固定金鑰、開始／停止；不增加登入、場次選擇、標題、排程或公開按鈕。不改編碼／音軌／NVENC／三畫質／29.97 fps／900 幀／30.03 秒／媒體驗證。Console End 不停止其他 OBS 輸出。

先完成 schema/fixtures 匯入、HTTP bootstrap／journal 恢復、錯誤分類與 CTest／兩個 Windows CI。測試 lost response、same-ID retry、terminal、pending cancellation、legacy journal 升級，並回報實際 plugin commit、ZIP hash、CI與未完成事項。

若 initial response 遺失且委派權限被撤銷，403 時保存 intent/media 等 Console 授權處理，不能假裝自動恢復成功。

此固定契約不是部署就緒證明；目前只做本機／mock／CI，不啟動正式 K4。待 Mac 另交實際部署版本／flags／health 後，再先確認不在聚會期間及授權有效，依 video-studio-windows 計劃 W3 跑最小短場。已通過播放器／16 分鐘續傳／2.5 小時長場不重跑整套，歷史弱網及延遲失敗保留。不得重設金鑰、刪錄影、手動發布或貼憑證。
