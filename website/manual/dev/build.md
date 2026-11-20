# 源码构建与贡献

安装 Go 1.27 或更新版本，克隆公开仓库并在根目录执行：

```sh
git clone https://github.com/Airexplosion/openopq-client.git
cd openopq-client
go test ./...
go vet ./...
go build -trimpath -o openopq ./cmd/openopq
```

Windows 可把输出名改成 `openopq.exe`。使用 `python tools/package.py` 构建五个平台的压缩包，发行包附 `BUILD.json` 和 SHA-256 清单。实际 QQ 登录需要单独配置兼容的签名服务。

## 修改文档或接口

文档源码位于 `website/manual/`，侧栏在 `mkdocs.yml`。安装 `mkdocs-material==9.7.1` 后运行 `python website/build.py`，再用 `python -m http.server --directory build/pages 8000` 本地预览。

提交接口改动时说明动作、协议版本和账号作用域；行为变化提供可复现的最小输入与期望响应，优先使用虚构数据。在 PR 中写清运行过的测试。
