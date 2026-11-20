# HTTP、WebSocket 与事件上报

选择传输方式时先问：谁能主动连接谁？

| 场景 | 方式 | 连接方向 |
| --- | --- | --- |
| 写脚本查资料或发消息 | HTTP 动作 | 脚本请求 OpenOPQ，读取响应 |
| 机器人需要边收消息边回复 | 正向 WebSocket Universal | 机器人连接 OpenOPQ，在一条连接上收发 |
| 框架要求填写反向 WS 地址 | 反向 WebSocket | OpenOPQ 主动拨到框架监听的地址 |
| 消费者已有 HTTP 服务 | HTTP POST 上报 / Webhook | OpenOPQ 将事件 POST 到消费者 |

在“网关设置”按账号分别启停 OPQ、OneBot 11、OneBot 12 的 HTTP 或 WS 服务。端口设为 0 时使用共享端口；单独端口由当前账号监听。多账号共用端口时优先复制账号专属地址。

## v11 回调参数

`OneBot.message_format` 支持 `array` 和 `string`，默认为数组。反向 WS 的 `role` 可设 API、Event 或 Universal，默认 Universal；重连默认 3000 ms。HTTP 上报 `timeout_ms` 默认 5000。心跳间隔为 0 时关闭，启用时为 1000 到 3600000 ms。

## v12 事件缓冲

`OneBot.v12.event_buffer_size` 默认 1024。HTTP 消费者可用 `get_latest_events` 拉取；正向 WS 可直接接收。v12 使用独立的 webhooks、reverse_ws 和访问令牌配置。动作信封见 [OneBot 12](../api/v12.md)。
