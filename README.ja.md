<p align="center"><img src="./docs/assets/banner.svg" width="100%" alt="OpenOPQ Client"></p>

<p align="center"><a href="./README.md">简体中文</a> · <a href="./README.en.md">English</a> · <a href="./README.zh-Hant.md">繁體中文</a> · <b>日本語</b></p>

<p align="center">
  <a href="https://github.com/Airexplosion/openopq-client/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/Airexplosion/openopq-client?style=flat-square&amp;color=7965cc&amp;logo=github"></a>
  <a href="https://github.com/Airexplosion/openopq-client/actions/workflows/ci.yml"><img alt="Client CI" src="https://github.com/Airexplosion/openopq-client/actions/workflows/ci.yml/badge.svg?branch=main"></a>
  <a href="./go.mod"><img alt="Go version" src="https://img.shields.io/github/go-mod/go-version/Airexplosion/openopq-client?style=flat-square&amp;color=31788c&amp;logo=go&amp;logoColor=white"></a>
  <a href="#usage"><img alt="Commercial use not granted" src="https://img.shields.io/badge/commercial%20use-not%20granted-77516f?style=flat-square"></a>
</p>

<p align="center"><a href="https://github.com/Airexplosion/openopq-client/releases/latest"><b>↓ ダウンロード</b></a>　·　<a href="./docs/guide.ja.md">利用ガイド</a>　·　<a href="https://github.com/Airexplosion/openopq-client/issues">問題を報告</a></p>

PC に表示した QR コードをスマートフォンの QQ で読み取り、ボットに QQ アカウントを接続します。複数のアカウントをローカルの管理画面で操作でき、サーバー上では画面なしでも動作します。

## 初回ログイン

> 互換性のある署名サービスが必要です。提供元にエンドポイントの URL と対応する QQ のバージョンを確認してください。

1. 配布ページからお使いの OS 用のアーカイブをダウンロードして展開します。`Config.example.json` を `Config.json` にコピーし、`FakeSignApi` に提供元の URL を入力します。
2. 展開先でターミナルを開き、お使いの OS に合ったコマンドを実行します。

**Windows · PowerShell**

```powershell
.\openopq.exe --config Config.json --port 8087
```

**macOS / Linux**

```sh
chmod +x ./openopq
./openopq --config Config.json --port 8087
```

[ローカル管理画面](http://127.0.0.1:8087/console/)でアカウントを追加し、スマートフォンの QQ で QR コードを読み取ります。

## ボットの接続

設定例では OPQ API と OneBot 11 が有効です。`Protocol` を `all` にすると OneBot 12 も有効になります。ログイン後はアカウント情報を取得して接続先を確認できます。

[設定・接続例・移行手順 →](./docs/guide.ja.md)

<a id="usage"></a>

## 利用条件

本プロジェクトは商用利用を許諾していません。第三者のコンポーネントにはそれぞれのライセンスが適用されます。
