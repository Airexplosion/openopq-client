<p align="center"><img src="./docs/assets/banner.svg" width="100%" alt="OpenOPQ Client"></p>

<p align="center"><a href="./README.md">简体中文</a> · <a href="./README.en.md">English</a> · <b>繁體中文</b> · <a href="./README.ja.md">日本語</a></p>

<p align="center">
  <a href="https://github.com/Airexplosion/openopq-client/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/Airexplosion/openopq-client?style=flat-square&amp;color=7965cc&amp;logo=github"></a>
  <a href="https://github.com/Airexplosion/openopq-client/actions/workflows/ci.yml"><img alt="Client CI" src="https://github.com/Airexplosion/openopq-client/actions/workflows/ci.yml/badge.svg?branch=main"></a>
  <a href="./go.mod"><img alt="Go version" src="https://img.shields.io/github/go-mod/go-version/Airexplosion/openopq-client?style=flat-square&amp;color=31788c&amp;logo=go&amp;logoColor=white"></a>
  <a href="#usage"><img alt="Commercial use not granted" src="https://img.shields.io/badge/commercial%20use-not%20granted-77516f?style=flat-square"></a>
</p>

<p align="center"><a href="https://github.com/Airexplosion/openopq-client/releases/latest"><b>↓ 下載用戶端</b></a>　·　<a href="./docs/guide.zh-Hant.md">使用指南</a>　·　<a href="https://github.com/Airexplosion/openopq-client/issues">回報問題</a></p>

在自己的電腦上掃碼登入 QQ，將帳號連到你的機器人。你可以透過本機網頁控制台管理多個帳號，也可以在伺服器上以無介面模式執行。

## 第一次掃碼

> 你需要相容的簽章服務。開始前請向服務提供者確認完整網址和對應的 QQ 版本。

1. 從發行頁下載適合你系統的壓縮檔並解壓縮。將 `Config.example.json` 複製為 `Config.json`，把服務提供者給的完整網址填入 `FakeSignApi`。
2. 在解壓縮後的目錄開啟終端機，執行對應的指令。

**Windows · PowerShell**

```powershell
.\openopq.exe --config Config.json --port 8087
```

**macOS / Linux**

```sh
chmod +x ./openopq
./openopq --config Config.json --port 8087
```

開啟[本機控制台](http://127.0.0.1:8087/console/)，依指示新增帳號，用手機 QQ 掃碼。

## 連接機器人

範本預設啟用 OPQ API 和 OneBot 11。將 `Protocol` 設為 `all` 也會啟用 OneBot 12。帳號上線後，先查詢登入資訊，確認機器人連到正確的帳號。

[設定、連線範例與遷移指南 →](./docs/guide.zh-Hant.md)

<a id="usage"></a>

## 使用授權

本專案未授予商業使用許可。第三方元件適用各自的授權條款。
