# OpenOPQ Client

用自己的 QQ 号连接机器人。启动 OpenOPQ Client，在本机网页上扫码登录，就可以让支持 **OneBot 11、OneBot 12 或 OPQ API** 的应用通过这个账号收发消息。一个控制台可以管理多个账号。

[下载客户端](https://github.com/Airexplosion/openopq-client/releases/latest) · [查看配置模板](./Config.example.json) · [反馈问题](https://github.com/Airexplosion/openopq-client/issues)

## 第一次登录

开始前，你需要一个**兼容的 QQ 签名服务地址**。客户端负责登录和收发消息，签名服务需要单独准备。配置模板默认使用 `QQNT-v7.0.2-53644`，请和服务提供方确认版本一致。

1. 下载适合自己系统的压缩包，解压到一个以后还会用的目录。
2. 把目录里的 `Config.example.json` 复制一份，命名为 `Config.json`。打开副本，在 `FakeSignApi` 的空引号中填入服务方给你的完整地址，保留路径和末尾的 `/sign`。
3. 在解压目录打开终端，运行对应系统的命令。

**Windows · PowerShell**

```powershell
.\openopq.exe --config Config.json --port 8087
```

**macOS / Linux**

```sh
chmod +x ./openopq
./openopq --config Config.json --port 8087
```

打开 [本机控制台](http://127.0.0.1:8087/console/)，添加 QQ 号，用手机 QQ 扫码。控制台如果要求输入密钥，可以按启动日志给出的路径查看，默认是 `data/console-key.txt`。

## 接入机器人

在控制台里设置机器人使用的协议、鉴权和事件转发。配置模板里的 `Protocol` 默认是 `both`。

| 你的机器人使用 | `Protocol` 填写 |
| --- | --- |
| OPQ API 或 OneBot 11 | `both`（模板默认，同时开启两者） |
| 只用 OneBot 12 | `onebot12` |
| 三种接口都需要 | `all` |

## 换机器或升级时，要留哪些文件？

保留自己的 `Config.json` 和整个 `data/` 目录。前者记录签名服务地址和连接配置，后者保存设备身份、登录状态和控制台设置。换机器前先停止旧实例，再把这些文件一起迁移。向别人反馈问题时，请隐去配置中的密钥，也请勿上传账号数据。

<details>
<summary>签名服务地址带用户名和密码，怎么填？</summary>

如果服务方使用 HTTP Basic 鉴权，格式是 `https://用户名:密钥@主机/路径/sign`。用户名或密钥中的特殊字符需要做 URL 编码。远程服务请使用 HTTPS，本机回环地址可使用 HTTP。服务方给的是带路径前缀的地址时，保留完整路径。

</details>

<details>
<summary>在服务器上运行，不打开网页</summary>

把下面的 `123456789` 换成自己的 QQ 号，按终端提示完成登录。

```sh
./openopq --config Config.json --headless --account 123456789 --login --data-dir data
```

</details>

## 从源码构建

安装 Go 1.27 或更新版本，在仓库根目录运行：

```sh
go test ./...
go vet ./...
go build -trimpath -o openopq ./cmd/openopq
```

维护者可用 `python tools/package.py` 生成 Windows、Linux 和 macOS 的客户端压缩包及 SHA-256 校验文件。源码更新和可下载版本分别见[提交记录](https://github.com/Airexplosion/openopq-client/commits/main/)和[发行记录](https://github.com/Airexplosion/openopq-client/releases)。

OpenOPQ Client 是第三方客户端，与腾讯官方无隶属关系。仓库尚未声明开源许可证。
