# 安装与校验

在[发行页](https://github.com/Airexplosion/openopq-client/releases/latest)展开 **Assets**，下载客户端包和同一版本的 `SHA256SUMS.txt`。压缩包名中的架构指运行客户端的机器。

| 机器 | 文件名结尾 | 确认方法 |
| --- | --- | --- |
| Windows x64 | windows-amd64.zip | 系统设置 → 系统 → 系统信息，查看系统类型 |
| Intel Mac / Apple 芯片 Mac | darwin-amd64.tar.gz / darwin-arm64.tar.gz | 苹果菜单 → 关于本机 |
| Linux x86_64 / aarch64 | linux-amd64.tar.gz / linux-arm64.tar.gz | 运行 `uname -m` |

**核对下载。** 在 Windows PowerShell 中执行 `Get-FileHash .\文件名.zip -Algorithm SHA256`；Linux 用 `sha256sum 文件名.tar.gz`；macOS 用 `shasum -a 256 文件名.tar.gz`。与清单中同名文件的一行比较。

**确认程序能启动。** 解压到固定目录，Windows 运行 `.\openopq.exe --version`，Linux/macOS 运行 `./openopq --version`。这一步不需要签名服务；输出版本正确后再[配置并扫码](start.md)。
