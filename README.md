# 離職頭像倒數器

依離職日期產生每天不同的正方形頭像。你可以在本機預覽、下載並自行更換 Slack 頭像；也可以授權 Slack，讓程式每天自動更新**自己的**頭像。

預設使用**純本機模式**，不需要 Slack app、token 或全天運作的 Docker。

## 快速開始

需要 Docker 與 Docker Compose。在專案目錄執行：

```sh
docker compose up -d --build
```

Compose 只將網頁埠發布到本機的 `127.0.0.1`，不會對區域網路開放。開啟 [http://127.0.0.1:8080](http://127.0.0.1:8080)，接著：

1. 上傳原圖。長方形圖片可完整留白，或調整裁切位置與縮放，做成正方形頭像。
2. 設定開始日、離職日、時區，以及想疊加的效果。
3. 按「儲存設定與原圖」。開始日起，頁面會顯示**今日頭像**；按「下載今天的頭像」，再自行到 Slack 更換。

右側可選擇任意日期查看所有已勾選效果；每個效果旁的「預覽」按鈕也有獨立的日期拉桿。調整中的設定可以即時預覽，**下載和 Slack 更新使用已儲存的設定**。

停止程式：`docker compose down`。這不會刪除圖片或設定；再次啟動即可沿用。

## 使用模式

在「基本設定 → 使用模式」選擇並儲存：

| 模式 | 你需要做什麼 | 程式會做什麼 |
| --- | --- | --- |
| 純本機（預設） | 開啟頁面、下載今日圖片，自己更換頭像 | 依時區產圖；不連接或呼叫 Slack |
| Slack 自動更新 | 建立 Slack app，完成本人 OAuth 授權 | 倒數期間每天上傳一次到自己的 Slack 頭像 |

純本機模式在開始日前仍可選日期預覽；開始日起顯示今日頭像，離職日後顯示最終效果。它不會自行修改 Slack 頭像，所以不需要讓電腦或 Docker 全天運作；開啟頁面時，程式仍須正在執行。

從舊版升級且設定檔沒有模式欄位時，程式會保留原本的 **Slack 自動更新**模式。切換到純本機後，既有 OAuth token 仍留在本機資料中，方便日後切回。**切換模式不會卸載已安裝在工作區的 Slack app**；若要移除，須另外到 Slack 工作區或 app 管理頁卸載。

## 選用：Slack 自動更新

純本機使用者可以略過本節。若要自動更新：

1. 到 [Slack API Apps](https://api.slack.com/apps) 建立 app，選擇自己的工作區。
2. 在 **OAuth & Permissions → User Token Scopes** 加入 `users.profile:write`。這是 **user token** 權限，不是 bot token 權限。[`users.setPhoto` 官方文件](https://docs.slack.dev/reference/methods/users.setPhoto/)列出此 scope。
3. 在 **Redirect URLs** 加入 `http://127.0.0.1:8080/oauth/callback`。若改用其他本機埠，Slack 設定與下方的 `SLACK_REDIRECT_URL` 必須一致。
4. 建立環境檔，填入 app 的 Client ID 與 Client Secret：

   ```sh
   cp .env.example .env
   # 編輯 .env 中的 SLACK_CLIENT_ID 和 SLACK_CLIENT_SECRET
   docker compose up -d
   ```

5. 在網頁選擇「Slack：每日自動更新」並儲存，再按「連接 Slack」，由本人完成授權。可按「測試連線」確認狀態。

工作區可能要求管理員批准 app；若授權被拒絕，回呼頁會顯示 Slack 回傳的原因。手動貼上 user token 也無法繞過工作區的 app 安裝與審核政策；此版僅提供 OAuth 授權，不提供貼 token 欄位。[Slack OAuth 流程說明](https://docs.slack.dev/authentication/installing-with-oauth/)

程式只要求 `users.profile:write` user scope，不會傳送訊息。授權回呼會檢查一次性 `state`；若 app 啟用了 token rotation，程式會更新並保存新的 user token。測試連線使用 `auth.test`，上傳使用官方 `users.setPhoto`，兩者都檢查回應中的 `ok`，不只看 HTTP 狀態碼。沒有 Slack 授權時，純本機預覽和下載仍可使用。

### 自動更新的時機

- 在設定時區的開始日至離職日（含），程式每天上傳一次。啟動、設定變更或完成授權時立即檢查；當天成功後會等到該時區的隔天零點，不再反覆檢查或呼叫 Slack。
- 失敗會記錄原因，同一天每隔 1 小時重試；若先跨日，會在新的一天零點檢查。重啟時會立即檢查並只補跑**當天**，不追補過去日期。排程使用 Go 程式內的計時器，不依賴 cron。
- 「立即更新頭像」會上傳**今天**的已儲存結果。即使今天已成功，也會再次上傳；倒數期間的成功計入當日完成紀錄。
- 離職日後停止自動更新，仍可預覽、下載，以及明確地手動更新。

電腦關機、Docker 停止或斷網時無法準時自動上傳。工作區政策、管理員審核、權限或 Slack API 錯誤也可能阻止上傳。自動模式須讓電腦、Docker 和網路持續可用。

## 圖片與日期規則

可勾選任意多種效果：由左至右灰階、逐漸模糊、逐漸像素化、薩諾斯消散、外圈倒數進度條、剩餘天數角標。先套用照片效果，再畫進度條與文字，因此角標不會被模糊或像素化。未勾選的效果不影響輸出。

薩諾斯消散會讓照片從右側逐漸碎裂，粒子向右飄散並淡出。開始日保持完整，離職日及之後留下白底；進度條與天數角標仍保留。同一張原圖、設定與日期會產生相同的靜態 PNG。

日期以設定的 IANA 時區之曆日計算。開始日進度為 0，離職日為 100%；開始日前維持 0%，離職日後維持 100%。若開始日與離職日相同，該日直接是 100%。剩餘天數最低為 0。所有預覽、下載與 Slack 上傳共用同一套圖片產生流程。

支援 PNG、JPEG、GIF（GIF 使用第一幀）。原圖最多 10 MB、8000×8000 且不超過 2000 萬像素；輸出為 640×640 PNG。選檔後，即使日期尚未填好也能先看正方形原圖。舊版 `data/original.png` 可以讀取；若要重新調整舊版長方形原圖的裁切，請重新上傳原始圖片。

## 資料保存、備份與移除

Docker 將資料存在專案的 `./data` 目錄，重建映像或容器不會清除：

| 路徑 | 內容 |
| --- | --- |
| `data/source.img` | 上傳的原圖；舊版可能是 `data/original.png` |
| `data/state.json` | 設定、OAuth token 與最近 100 次執行紀錄 |
| `.env` | 選用的 Slack Client ID、Client Secret 與 redirect URL |

`data/` 和 `.env` 不應提交到 Git，也不應分享。備份時先執行 `docker compose down`，複製整個 `data/` 目錄；若使用 Slack 自動模式，也一併備份 `.env`。還原時放回相同位置後重新啟動。

若要移除，執行 `docker compose down`，再自行刪除 `data/` 與 `.env`。刪除前請確認是否還需要原圖或授權資料。這不會替你卸載 Slack 工作區內的 app。

## 不用 Docker：原生執行

安裝 Go 1.23 或更新版本；程式使用純 Go 依賴，macOS／Windows 可直接編譯，不需要 Node.js、CGO、外部圖片程式或資料庫服務。

macOS／Linux：

```sh
go build -o countdown-avatar .
DATA_DIR=./data ADDR=127.0.0.1:8080 ./countdown-avatar
```

Windows PowerShell：

```powershell
go build -o countdown-avatar.exe .
$env:DATA_DIR = 'data'
$env:ADDR = '127.0.0.1:8080'
./countdown-avatar.exe
```

原生程式不會自行讀取 `.env`。若啟用 Slack 模式，還須在執行環境設定 `SLACK_CLIENT_ID`、`SLACK_CLIENT_SECRET`、`SLACK_REDIRECT_URL`。時區資料已內嵌於執行檔。

## 開發與測試

```sh
go test ./...
docker build -t countdown-avatar:local .
```

測試使用 mock Slack API 驗證 OAuth、token 更新、上傳失敗、並行去重、重啟補跑、純本機模式不呼叫 Slack，以及預覽／下載／上傳產圖一致性。測試不會更新真實 Slack 頭像。
