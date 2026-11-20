# OneBot 12 文件与分片

在 v12 中，发送图片或语音时使用当前账号的 `file_id`。文件很小时可以一次上传，然后在 `send_message` 的 image 或 voice 段中引用。

```json
{"action":"upload_file","params":{"type":"data","name":"hello.txt","data":"aGVsbG8="},"echo":"upload-1"}
```

上例上传内容是文本 hello，可用返回的 file_id 测试读取。发送图片时请上传实际图片字节。

| 动作 | 用法 |
| --- | --- |
| upload_file | type=data 时传 name 和 base64 编码的 data |
| get_file | 指定 file_id 和所需 type，例如 data |
| upload_file_fragmented | 按 prepare、transfer、finish 三个阶段上传 |
| get_file_fragmented | 先 prepare 获取元信息，再用 transfer 按 offset、size 读取 |

分片下载的 offset 从 0 开始，单次 transfer 的 size 至多 4 MiB，范围不能越过文件结尾。多账号环境要从同一账号的端点连续完成上传与读取。文件内容的 base64 和服务分配的 file_id 是两种不同的值。
