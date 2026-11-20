# 无界面运行与升级

服务器上先用绝对路径固定配置、数据和日志位置。下面的目录、账号需要换成自己的；日志目录和数据目录应允许服务账号写入。

```sh
./openopq --headless --config /srv/openopq/Config.json \
  --data-dir /srv/openopq/data --account 123456789 --login \
  --qr-file /srv/openopq/data/login-qr.png --log-file /srv/openopq/client.log
```

`--headless` 关闭网页，HTTP、WebSocket 和控制接口仍然可用。首次登录时在受控设备上查看二维码；上线后用目标账号的 `get_login_info` 核对身份。设置系统服务时使用同一运行用户和工作目录，进程重启后才能读回原来的数据。

## 原地升级

1. 记录 `--version` 和正在使用的 `--config`、`--data-dir`，正常停止进程。
2. 备份配置与数据，解压新版到另一目录，保留旧程序便于回退。
3. 使用同一数据目录启动新程序；配置模板只用于对照新增字段。
4. 核对版本、登录账号，再发一条自己的测试消息观察事件。恢复失败时先查看[日志与诊断](../console/diagnostics.md)。
