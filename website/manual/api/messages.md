# 消息段、引用与转发

v11 的 `message` 可以是有序的段数组。机器人接到事件时，先读取段的 `type`，再解释 `data`。要保留混合消息的顺序，按数组从头到尾处理。

| v11 消息段 | 关键字段 | 用途 |
| --- | --- | --- |
| text | data.text | 普通文本 |
| at | data.qq | 提及指定账号 |
| reply | data.id | 引用当前账号缓存中的原消息 |
| image / record | data.file | 图片或语音内容标识 |
| forward | data.id | 收到的合并转发资源，可交给 get_forward_msg |

## 回复一条群消息

假设收到的事件含 `group_id=123456` 和 `message_id=456`。发给 `send_group_msg` 的正文可以是：

```json
{"group_id":123456,"message":[{"type":"reply","data":{"id":"456"}},{"type":"text","data":{"text":"今晚八点见"}}]}
```

原消息和回复要属于同一账号、同一会话。读合并转发时取的是 forward 段里的资源 ID；`get_forward_msg` 成功后遍历 `data.messages`。语音要单独发，不能把 record 和 reply 放到同一个消息数组里。[运行示例](../examples.md)
