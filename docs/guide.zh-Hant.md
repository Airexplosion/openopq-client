# 用戶端使用指南

[← OpenOPQ Client](../README.zh-Hant.md)　·　[简体中文](./guide.md) · [English](./guide.en.md) · **繁體中文** · [日本語](./guide.ja.md)

## 選擇安裝檔

64 位元 x86 Windows 選 `windows-amd64.zip`。Apple 晶片的 Mac 選 `darwin-arm64`，Intel Mac 選 `darwin-amd64`。Linux 請按處理器選 `linux-amd64` 或 `linux-arm64`。先解壓縮，再建立設定檔。

## 設定簽章服務並登入

複製設定範本，保留其餘設定。下方列出要填寫的欄位。請以服務提供者給的完整網址取代範例，保留路徑前綴。遠端 Basic 驗證請使用 HTTPS，帳號或密鑰中的特殊字元須做 URL 編碼。

```json
{
  "FakeSignApi": "https://signer.example.com/service/sign",
  "Version": "QQNT-v7.0.2-53644",
  "Protocol": "both"
}
```

用手機 QQ 掃碼，在控制台確認帳號已上線。若控制台要求密鑰，請依啟動日誌的路徑讀取，預設為 `data/console-key.txt`。

## 驗證機器人連線

在控制台選擇目標帳號並複製對外的連線網址。下例使用本機 OneBot 11 HTTP 端點。若已啟用驗證，請附上 HTTP 存取權杖。傳回的 `data.user_id` 應與登入的 QQ 相同。

```sh
curl -H "Authorization: Bearer $OPENOPQ_HTTP_TOKEN" \
  http://127.0.0.1:8087/onebot/v11/get_login_info
```

OneBot 11 的正向 WebSocket 路徑為 `/onebot/v11/ws`，可用來接收事件。多帳號請選控制台提供的專屬網址。控制台密鑰、HTTP 和 WebSocket 權杖分別用於不同連線。

## 無介面啟動與遷移

伺服器可使用下列指令啟動掃碼登入。換機前先停止舊執行個體，再複製 `Config.json` 和整個 `data/` 目錄。這些檔案含裝置身分、工作階段與密鑰，請妥善保存。

```sh
./openopq --config Config.json --headless --account 123456789 --login --data-dir data
```

## 從原始碼建置

Go 1.27+

```sh
go test ./...
go vet ./...
go build -trimpath -o openopq ./cmd/openopq
```

## 聊天狀態指令

在群組或私聊單獨傳送 `#oop`，即可查看版本、執行平台和運行時間。用登入用戶端的 QQ 傳送 `#oop off` 關閉回覆，或用 `#oop on` 重新開啟。也可在 WebUI 的閘道與工作階段設定中切換。預設開啟，各帳號獨立儲存設定。
