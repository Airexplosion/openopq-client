# 命令行参数

| 参数 | 用途 |
| --- | --- |
| `--version` | 打印版本、提交及构建时间后退出 |
| `--port 8087` | 设置 HTTP/WS 共享端口 |
| `--config Config.json`、`--data-dir data` | 指定配置和持久化目录 |
| `--account QQ号`、`--login` | 选择设备档案并在启动时请求二维码 |
| `--qr-file 路径` | 指定二维码 PNG 的输出位置 |
| `--resume=false` | 本次启动跳过自动恢复 |
| `--open-browser` | 启动后打开管理页面 |
| `--headless` | 关闭管理网页，保留协议与控制接口 |
| `--quiet`、`--verbose`、`--log-file 路径` | 分别用于安静终端、逐请求记录、追加 JSON 文件日志 |
| `--attach-console 地址` | 附加已运行的本机控制台或账号会话，可重复使用 |

检查某个账号的启动行为时使用 `--account` 和明确的数据目录。想暂时绕开旧状态验证扫码，可用 `--resume=false`。参数以当前二进制的 `--help` 为准。
