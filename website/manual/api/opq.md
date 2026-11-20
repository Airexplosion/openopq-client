# OPQ 原生接口

已有 OPQ 插件通常使用同一个 HTTP 地址发送不同命令。配置中的 `Protocol` 设为 `opq`、`both` 或 `all`。

## 查询机器人所在的群

```sh
curl -X POST 'http://127.0.0.1:8087/v1/LuaApiCaller?funcname=MagicCgiCmd&timeout=10&qq=10001' \
  -H 'Content-Type: application/json' \
  -d '{"CgiCmd":"GetGroupLists","CgiRequest":{}}'
```

把 `10001` 换为已登录的机器人账号。启用 HTTP 鉴权时另加 Bearer 请求头。

| 字段 | 含义 |
| --- | --- |
| qq | 执行操作的机器人账号 |
| funcname | 入口函数，通用 CGI 调用使用 MagicCgiCmd |
| timeout | 请求超时，单位为秒 |
| CgiCmd | 具体操作，例如 GetGroupLists |
| CgiRequest | 操作参数对象 |
| CgiBaseResponse.Ret / ErrMsg | 业务返回码和错误信息；Ret 为 0 表示成功 |
| ResponseData.GroupLists | 本次群列表查询的结果，可读 GroupCode、GroupName、MemberCnt |

## 发送群文本

沿用上面的 HTTP 地址，替换 JSON 正文：

```json
{"CgiCmd":"MessageSvc.PbSendMsg","CgiRequest":{"ToUin":123456,"ToType":2,"Content":"你好"}}
```

`ToUin` 表示接收方；群聊使用群号和 `ToType: 2`，好友私聊使用好友 QQ 号和 `ToType: 1`。响应先检查 `CgiBaseResponse.Ret`。

## 查询实例与接收事件

`GET /v1/clusterinfo` 查询实例中的账号概况，返回数据可用于核对 QQUsers、QQUsersCounts。通过控制台复制所选账号的 OPQ WebSocket 入口；事件中的 CurrentQQ 标识事件所属账号。控制台的管理请求与机器人调用使用不同的鉴权凭据。
