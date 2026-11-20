# 事件与动作响应

机器人通常同时处理两类输入：用户产生的消息事件，以及之前发出的动作响应。在 v11 Universal WS 上它们共用连接。

```json
{"action":"get_login_info","params":{},"echo":"login-check-1"}
```

发送端把 `echo` 和待处理请求关联起来。收到带相同 echo 的响应后检查 retcode；收到含 `post_type` 的 v11 事件时，按事件类型分发。不要把下一帧一律当成刚才的响应。

| v11 字段 | 什么时候读 |
| --- | --- |
| self_id | 先确定是哪个机器人账号收到事件 |
| post_type | message、notice、request、meta_event 等大类 |
| message_type、group_id、user_id | 决定消息来自私聊还是群聊，以及发送方 |
| message_id、message | 保存可引用的消息标识，按顺序读取消息段 |
| flag、sub_type | 处理好友/群申请时沿用原请求给出的值 |

v12 事件按 `type`、`detail_type` 和 `sub_type` 分类，账号信息位于 `self`。使用 v12 的事件处理器时，明确按 v12 字段读取。断线重连后若动作响应丢失，先核对实际结果，再决定是否重发会产生消息的动作。
