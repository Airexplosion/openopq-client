# 客户端使用指南

[← OpenOPQ Client](../README.md)　·　**简体中文** · [English](./guide.en.md) · [繁體中文](./guide.zh-Hant.md) · [日本語](./guide.ja.md)

## 选择安装包

Windows 选 `windows-amd64.zip`。Apple 芯片的 Mac 选 `darwin-arm64`，Intel Mac 选 `darwin-amd64`。Linux 按处理器选择 `linux-amd64` 或 `linux-arm64`。解压后在同一目录创建配置和启动程序。

## 签名与登录

复制配置模板，保留其他设置。下面是要填写的字段示例。将示例域名换成服务方给出的地址，保留其路径前缀。带 Basic 鉴权的远程地址应使用 HTTPS，用户名或密钥中的特殊字符需要 URL 编码。

```json
{
  "FakeSignApi": "https://signer.example.com/service/sign",
  "Version": "QQNT-v7.0.2-53644",
  "Protocol": "both"
}
```

用手机 QQ 扫码后，在控制台确认账号已上线。若控制台要求密钥，按启动日志提示读取，默认文件为 `data/console-key.txt`。

## 验证机器人连接

在控制台选择目标账号并复制外部客户端端点。下面以本机 OneBot 11 HTTP 接口为例，配置了鉴权时请加上 HTTP 访问令牌。返回的 `data.user_id` 应当是你刚登录的 QQ。

```sh
curl -H "Authorization: Bearer $OPENOPQ_HTTP_TOKEN" \
  http://127.0.0.1:8087/onebot/v11/get_login_info
```

接收事件可使用 OneBot 11 的正向 WebSocket，路径为 `/onebot/v11/ws`。多账号时请使用控制台给出的账号专属地址。控制台登录密钥、HTTP 令牌和 WebSocket 令牌各有用途。

## 无界面启动与换机

服务器上可用下列命令启动扫码登录。换机器前停止旧实例，复制 `Config.json` 和整个 `data/` 目录；其中含设备档案、会话和密钥，请妥善保管。

```sh
./openopq --config Config.json --headless --account 123456789 --login --data-dir data
```

## 从源码构建

Go 1.27+

```sh
go test ./...
go vet ./...
go build -trimpath -o openopq ./cmd/openopq
```

## 聊天状态指令

在群聊或私聊单独发送 `#oop`，客户端会回复版本、运行平台和运行时长。用登录客户端的 QQ 发送 `#oop off` 关闭回复，或发送 `#oop on` 恢复。也可以在 WebUI 的「网关与会话设置」里切换。默认开启，每个账号独立保存。
