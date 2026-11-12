# OpenOPQ Client

独立的 OpenOPQ 客户端，提供本地网页控制台、二维码登录、OPQ API、OneBot 11 / 12 和多账号会话管理。

**必须自行提供兼容的签名服务。** 本仓库不包含 signer 服务端、QQ SDK、Darling、签名镜像或签名服务地址，也不会下载、启动或自动选择签名器。`internal/sign` 是调用外部 HTTP 服务的客户端代码，不是签名算法。

## 快速开始

从 Releases 下载对应系统的客户端包并解压，将 `Config.example.json` 复制为 `Config.json`，填写 `FakeSignApi`：

```json
{
  "FakeSignApi": "https://signer.example.com:443/service/sign",
  "Version": "QQNT-v7.0.2-53644",
  "Protocol": "both",
  "Accounts": []
}
```

地址必须完整包含协议、主机、需要时的端口及 `/sign` 路径。示例域名不能直接使用；地址留空时程序会提示配置并退出。QQ 身份版本必须与签名服务匹配。

若签名服务使用 HTTPS Basic 鉴权，可以填写：

```text
https://用户名:密钥@signer.example.com:443/service/sign
```

用户名与密钥中的特殊字符需要 URL 编码；URL-safe 随机密钥可直接填写。这里支持的是 HTTP Basic，不能当作任意 Bearer/API Key 服务的通用协议。公网携带密钥时必须使用 HTTPS；仅本机回环地址允许 HTTP。不要分享或提交自己的 `Config.json`。

Windows：

```powershell
.\openopq.exe --config Config.json --port 8087
```

Linux / macOS：

```sh
chmod +x ./openopq
./openopq --config Config.json --port 8087
```

浏览器打开 `http://127.0.0.1:8087/console/`，按控制台提示添加账号并扫码。首次控制台认证信息按启动输出或 `data/console-key.txt` 获取。保留 `data/` 中的设备档案、加密登录状态及控制台配置；不要将它公开。

无网页运行：

```sh
./openopq --config Config.json --headless --account 你的QQ号 --login --data-dir data
```

默认 `both` 提供 OPQ 与 OneBot 11；`all` 另开启 OneBot 12。控制台中可设置协议接入、鉴权和事件转发。多账号数量和稳定性取决于所接签名服务，客户端不会解除服务端限制。

## 构建与验证

需要 Go 1.27 或更新版本：

```sh
go test ./...
go vet ./...
go build -trimpath -o openopq ./cmd/openopq
```

`python tools/package.py` 构建 Windows amd64、Linux amd64/arm64、macOS amd64/arm64 客户端包，并生成 SHA-256 校验文件。只打包客户端、空配置模板与说明文件。

## 范围

此仓库由客户端源码独立整理，以全新的 Git 历史发布。测试使用合成输入；私人抓包、账号数据、签名器工程和原部署配置不包含在这里。自动化测试通过不等于某个外部签名服务已通过真实扫码或长期多账号运行验证。

项目不是腾讯官方客户端。媒体转换功能按运行环境需要另行安装 FFmpeg 等外部工具。本仓库目前未附加开源许可授权。
