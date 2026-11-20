# クライアントガイド

[← OpenOPQ Client](../README.ja.md)　·　[简体中文](./guide.md) · [English](./guide.en.md) · [繁體中文](./guide.zh-Hant.md) · **日本語**

## 配布ファイルを選ぶ

64 ビットの x86 Windows には `windows-amd64.zip`、Apple シリコン搭載 Mac には `darwin-arm64`、Intel Mac には `darwin-amd64` を選びます。Linux は CPU に合わせて `linux-amd64` または `linux-arm64` を選んでください。展開先に設定ファイルを作成します。

## 署名サービスとログイン

設定のテンプレートをコピーし、次の項目を編集します。URL はパスの接頭部を含めて提供元のものに置き換えてください。リモートの Basic 認証には HTTPS を使用し、ユーザー名やシークレットの特殊文字を URL エンコードします。

```json
{
  "FakeSignApi": "https://signer.example.com/service/sign",
  "Version": "QQNT-v7.0.2-53644",
  "Protocol": "both"
}
```

スマートフォンの QQ でコードを読み取り、管理画面でオンラインになったことを確認します。管理画面のキーを求められた場合、起動ログに表示されるファイルを確認してください。既定のパスは `data/console-key.txt` です。

## ボットの接続を確認

管理画面で対象アカウントの外部接続 URL をコピーします。以下はローカルの OneBot 11 HTTP の例です。認証を有効にした場合は HTTP トークンを付けてください。応答の `data.user_id` がログインした QQ 番号と一致することを確認します。

```sh
curl -H "Authorization: Bearer $OPENOPQ_HTTP_TOKEN" \
  http://127.0.0.1:8087/onebot/v11/get_login_info
```

イベントを受信するには OneBot 11 の WebSocket `/onebot/v11/ws` に接続します。複数アカウントでは管理画面に表示されるアカウント別 URL を使います。管理画面のキーと HTTP・WebSocket の各トークンは用途が異なります。

## ヘッドレス起動と移行

サーバーでは次のコマンドで QR ログインを開始できます。移行時は旧インスタンスを停止し、`Config.json` と `data/` ディレクトリ全体をコピーします。端末情報・セッション・キーを含むため、安全に保管してください。

```sh
./openopq --config Config.json --headless --account 123456789 --login --data-dir data
```

## ソースからビルド

Go 1.27+

```sh
go test ./...
go vet ./...
go build -trimpath -o openopq ./cmd/openopq
```
