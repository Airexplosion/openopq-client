# 基础配置与签名服务

从 [Config.example.json](https://github.com/Airexplosion/openopq-client/blob/main/Config.example.json) 复制出配置，再逐项修改。启动时 `--config` 决定读取哪份文件。

| 字段 | 怎么选 |
| --- | --- |
| FakeSignApi | 服务方提供的完整 HTTP(S) 地址，以 /sign 结尾，保留路径前缀；不接受 query 或 fragment |
| Version | 必须与签名服务支持的 QQ 身份版本一致，模板为 QQNT-v7.0.2-53644 |
| Protocol | opq、onebot11、onebot12、both 或 all；模板使用 both |
| Accounts | 启动时配置的账号列表，首次上手可保留空数组，在控制台添加 |
| AutoReconnect | QQ 网络中断后是否自动尝试重连 |
| PersistLogin / AutoRestoreLogin | 分别决定是否保存登录状态、启动时是否尝试自动恢复 |
| MediaCacheMiB | 每账号媒体磁盘额度，省略或 0 使用 256 MiB |

服务提供方若使用 HTTP Basic，远程连接应使用 HTTPS，用户名和密钥中的特殊字符须 URL 编码。配置中含签名凭据，提交问题时只提供字段名、版本和去掉凭据后的路径结构。

修改后先重启客户端，再在“运行概览”核对签名服务状态。账号已经上线但机器人调用异常，继续检查[协议传输](transports.md)。
