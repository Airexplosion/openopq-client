# OneBot 11 示例

登录自己的测试账号后，在这个目录运行 `python client.py --help`。只需 Python 3.7+。默认连本机 8087 端口；多账号请把控制台显示的账号专属 OneBot 11 HTTP 基址填入环境变量 `OPENOPQ_ONEBOT_URL`，开启鉴权时另设 `OPENOPQ_HTTP_TOKEN`。

| 命令（尖括号内容替换为自己的值） | 数据从哪里来 | 结果 |
| --- | --- | --- |
| `python client.py reply <群号> <消息ID> "收到"` | 当前账号在该群收到的消息事件 | 发出一条引用回复，返回新消息 ID |
| `python client.py forward <转发资源ID>` | 收到的 forward 段的 data.id | 读取合并转发的消息列表 |
| `python client.py media <媒体标识>` | 收到的图片、视频或语音段的 file | 原文件在网关机器上的缓存路径、名称和大小 |
| `python client.py group-files <群号>` | 当前账号所在的群 | 根目录文件和文件夹 |
| `python client.py group-file-url <群号> <file_id> <busid>` | 上一步同一文件的两个字段 | QQ 返回的下载地址 |
| `python client.py voice <群号> clip.wav` | 本地音频文件 | 发出一条群语音 |

Windows x64 用户可从仓库下载 `tools/install-media-tools.ps1`，将目标选为客户端可执行文件旁的 `tools` 目录。脚本检查 SILK 和 FFmpeg 压缩包的 SHA-256，工具沿用各自许可证。引用和语音示例会实际发消息，请使用自己的测试群。
