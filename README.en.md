<p align="center"><img src="./docs/assets/banner.svg" width="100%" alt="OpenOPQ Client"></p>

<p align="center"><a href="./README.md">简体中文</a> · <b>English</b> · <a href="./README.zh-Hant.md">繁體中文</a> · <a href="./README.ja.md">日本語</a></p>

<p align="center">
  <a href="https://github.com/Airexplosion/openopq-client/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/Airexplosion/openopq-client?style=flat-square&amp;color=7965cc&amp;logo=github"></a>
  <a href="https://github.com/Airexplosion/openopq-client/actions/workflows/ci.yml"><img alt="Client CI" src="https://github.com/Airexplosion/openopq-client/actions/workflows/ci.yml/badge.svg?branch=main"></a>
  <a href="./go.mod"><img alt="Go version" src="https://img.shields.io/github/go-mod/go-version/Airexplosion/openopq-client?style=flat-square&amp;color=31788c&amp;logo=go&amp;logoColor=white"></a>
  <a href="./LICENSE.md"><img alt="PolyForm Noncommercial 1.0.0" src="https://img.shields.io/badge/license-PolyForm%20Noncommercial-77516f?style=flat-square"></a>
</p>

<p align="center"><a href="https://github.com/Airexplosion/openopq-client/releases/latest"><b>↓ Download</b></a>　·　<a href="./docs/guide.en.md">User guide</a>　·　<a href="https://github.com/Airexplosion/openopq-client/issues">Issues</a></p>

Scan a QR code on your computer to connect a QQ account to your bot. Manage multiple accounts in the local web console, or run the client headless on a server.

## First sign-in

> You need a compatible signing service. Ask your provider for the full endpoint and matching QQ version before installing the client.

1. Download and extract the archive for your platform. Copy `Config.example.json` to `Config.json`, then set `FakeSignApi` to the full endpoint supplied by your provider.
2. Open a terminal in the extracted directory and run the command for your system.

**Windows · PowerShell**

```powershell
.\openopq.exe --config Config.json --port 8087
```

**macOS / Linux**

```sh
chmod +x ./openopq
./openopq --config Config.json --port 8087
```

Open the [local console](http://127.0.0.1:8087/console/), add an account and scan the QR code with QQ on your phone.

## Connect your bot

The template enables OPQ API and OneBot 11. Set `Protocol` to `all` to enable OneBot 12 as well. Once signed in, request your login information to check which account the bot is using.

[Configuration, connection examples and migration →](./docs/guide.en.md)

<a id="usage"></a>

## Usage rights

This project grants no permission for commercial use. Third-party components retain their own license terms.

[PolyForm Noncommercial 1.0.0](./LICENSE.md) · [Third-party notices](./THIRD-PARTY-NOTICES.txt)
