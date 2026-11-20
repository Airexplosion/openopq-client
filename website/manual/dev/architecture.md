# 架构与接入开发

一条群消息经过三个边界：**QQ 会话 → 本机协议网关 → 你的机器人**。客户端根据设备档案建立 QQ 会话，按需调用外部签名服务；网关把收到的消息映射成 OPQ 或 OneBot 事件；机器人通过 HTTP 或 WebSocket 调用动作。

| 想改哪一层 | 仓库入口 | 应检查的行为 |
| --- | --- | --- |
| CLI、程序装配 | [cmd/openopq](https://github.com/Airexplosion/openopq-client/tree/main/cmd/openopq) | 参数解析、配置加载、构建版本 |
| 网页控制台、多账号路由 | [internal/console](https://github.com/Airexplosion/openopq-client/tree/main/internal/console) | 选中账号和外部请求的作用域 |
| OPQ、OneBot 动作与事件 | [internal/api](https://github.com/Airexplosion/openopq-client/tree/main/internal/api) | 协议字段、错误码、事件形状 |
| QQ 客户端业务 | [internal/client](https://github.com/Airexplosion/openopq-client/tree/main/internal/client) | 执行动作、恢复连接、缓存媒体 |
| 外部签名请求 | [internal/sign](https://github.com/Airexplosion/openopq-client/tree/main/internal/sign) | 建立和恢复客户端与服务端之间的会话 |

写自己的机器人，可以从[标准库 Python 示例](../examples.md)开始，只连接公开协议端点，无需依赖这些 Go 内部包。先验证查询，再接事件，最后在测试群中验证会发送消息的动作。
