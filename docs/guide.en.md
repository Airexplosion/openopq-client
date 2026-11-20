# Client guide

[← OpenOPQ Client](../README.en.md)　·　[简体中文](./guide.md) · **English** · [繁體中文](./guide.zh-Hant.md) · [日本語](./guide.ja.md)

## Choose a download

Use `windows-amd64.zip` on 64-bit x86 Windows, `darwin-arm64` on Apple silicon or `darwin-amd64` on Intel Macs. On Linux, choose `linux-amd64` or `linux-arm64` to match your CPU. Extract the archive before creating the configuration file.

## Configure signing and sign in

Copy the configuration template and edit the fields shown below. Replace the example URL with the endpoint from your provider, including any path prefix. Use HTTPS for remote Basic authentication and URL-encode special characters in the username or secret.

```json
{
  "FakeSignApi": "https://signer.example.com/service/sign",
  "Version": "QQNT-v7.0.2-53644",
  "Protocol": "both"
}
```

Scan with QQ on your phone and check that the account appears online. If the console requests a key, look for the path in the startup log. The default is `data/console-key.txt`.

## Check your bot connection

Select the account in the console and copy its external client endpoint. The example below queries a local OneBot 11 HTTP endpoint. If authentication is enabled, include your HTTP access token. Check that `data.user_id` matches the QQ account you signed into.

```sh
curl -H "Authorization: Bearer $OPENOPQ_HTTP_TOKEN" \
  http://127.0.0.1:8087/onebot/v11/get_login_info
```

For events, connect your bot to the OneBot 11 forward WebSocket at `/onebot/v11/ws`. When running multiple accounts, use the account-specific URL shown in the console. The console key, HTTP token and WebSocket token serve different purposes.

## Headless mode and migration

Use the command below to start QR login on a server. To move the client, stop the old instance and copy `Config.json` and the entire `data/` directory. Keep these files private because they contain device profiles, session data and keys.

```sh
./openopq --config Config.json --headless --account 123456789 --login --data-dir data
```

## Build from source

Go 1.27+

```sh
go test ./...
go vet ./...
go build -trimpath -o openopq ./cmd/openopq
```
