# 快速开始

准备一个与 QQ 版本匹配的签名服务地址，再从 [Releases](https://github.com/Airexplosion/openopq-client/releases/latest) 下载并解压客户端。Windows x64 选 `windows-amd64.zip`，Apple 芯片选 `darwin-arm64`，Intel Mac 选 `darwin-amd64`，Linux 按 CPU 选择。

## 扫码上线

复制 `Config.example.json` 为 `Config.json`，填写 `FakeSignApi`。保留服务方提供的完整路径，以 `/sign` 结尾。模板的 `Version` 是 `QQNT-v7.0.2-53644`。

```powershell
.\openopq.exe --config Config.json --port 8087
```

macOS 和 Linux 在解压目录执行：

```sh
chmod +x ./openopq
./openopq --config Config.json --port 8087
```

打开本机的 `http://127.0.0.1:8087/console/`，添加 QQ 号，用手机 QQ 扫码。控制台初始密钥见启动日志指定的文件。配置模板的 `both` 同时开启 OPQ 和 OneBot 11；要同时使用 OneBot 12，将 `Protocol` 改为 `all` 再重启。

## 第一个请求

选择刚登录的账号，从控制台复制它的 OneBot 11 HTTP 地址。单账号默认端口示例：

```sh
curl http://127.0.0.1:8087/onebot/v11/get_login_info
```

启用 HTTP 鉴权后加 `Authorization: Bearer <HTTP_TOKEN>` 请求头。成功时 `retcode` 为 0，`data.user_id` 和 `data.nickname` 是机器人的 QQ 号和昵称。多账号使用对应的 `/accounts/<session>/onebot/v11` 基址。

## 下一步

已有机器人框架先对照它的协议版本：[OneBot 11](api/v11.md)、[OneBot 12](api/v12.md) 或 [OPQ](api/opq.md)。要处理语音、引用或群文件，直接运行[实战示例](examples.md)。

升级或迁移前停止旧实例，保留 `Config.json` 和完整的 `data/`。后者保存设备档案和登录状态。
