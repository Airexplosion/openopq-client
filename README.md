<p align="center"><img src="./docs/assets/banner.svg" width="100%" alt="OpenOPQ Client"></p>

<p align="center"><b>简体中文</b> · <a href="./README.en.md">English</a> · <a href="./README.zh-Hant.md">繁體中文</a> · <a href="./README.ja.md">日本語</a></p>

<p align="center">
  <a href="https://github.com/Airexplosion/openopq-client/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/Airexplosion/openopq-client?style=flat-square&amp;color=7965cc&amp;logo=github"></a>
  <a href="https://github.com/Airexplosion/openopq-client/actions/workflows/ci.yml"><img alt="Client CI" src="https://github.com/Airexplosion/openopq-client/actions/workflows/ci.yml/badge.svg?branch=main"></a>
  <a href="./go.mod"><img alt="Go version" src="https://img.shields.io/github/go-mod/go-version/Airexplosion/openopq-client?style=flat-square&amp;color=31788c&amp;logo=go&amp;logoColor=white"></a>
  <a href="./LICENSE.md"><img alt="PolyForm Noncommercial 1.0.0" src="https://img.shields.io/badge/license-PolyForm%20Noncommercial-77516f?style=flat-square"></a>
</p>

<p align="center"><a href="https://github.com/Airexplosion/openopq-client/releases/latest"><b>↓ 下载客户端</b></a>　·　<a href="./docs/guide.md">使用指南</a>　·　<a href="https://github.com/Airexplosion/openopq-client/issues">反馈问题</a></p>

在自己的电脑上扫码登录 QQ，把账号接到你的机器人。OpenOPQ Client 提供本地网页控制台，可以管理多个账号，也可以在服务器上无界面运行。

## 第一次扫码

> 准备一个兼容的签名服务地址。客户端安装包里没有签名服务，开始前请向服务提供方确认地址和 QQ 版本。

1. 从发行页下载对应系统的压缩包并解压。把 `Config.example.json` 复制为 `Config.json`，将服务方给的完整地址填入 `FakeSignApi`。
2. 在解压目录打开终端，运行对应系统的命令。

**Windows · PowerShell**

```powershell
.\openopq.exe --config Config.json --port 8087
```

**macOS / Linux**

```sh
chmod +x ./openopq
./openopq --config Config.json --port 8087
```

打开[本机控制台](http://127.0.0.1:8087/console/)，按提示添加账号，用手机 QQ 扫码。

## 连接机器人

模板默认同时开启 OPQ API 和 OneBot 11。要用 OneBot 12，可将 `Protocol` 改为 `all`。账号上线后，先查询登录信息，确认机器人连到了正确的账号。

[配置、连接示例与换机指南 →](./docs/guide.md)

<a id="usage"></a>

## 使用授权

本项目未授予商业使用许可。第三方组件适用各自的许可证。

[PolyForm Noncommercial 1.0.0](./LICENSE.md) · [Third-party notices](./THIRD-PARTY-NOTICES.txt)
