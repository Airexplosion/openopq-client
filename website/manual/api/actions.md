# OneBot 11 动作索引

下面列出客户端路由识别的动作名称，方便按用途检索。兼容名称、账号权限和后端能力会影响执行结果。要判断自己的运行实例提供哪些动作，先请求 `get_supported_actions`。查看[调用格式](v11.md)、[消息段示例](messages.md)或[群文件字段](groups.md)。

## 账号与能力查询

| 动作 | 用途 |
| --- | --- |
| `get_login_info` | 读取当前登录账号与昵称 |
| `get_status` / `get_version_info` | 读取运行状态 / 协议实现版本 |
| `get_supported_actions` / `get_onebot_capabilities` | 列出当前实现报告的动作 / 能力 |
| `get_online_clients` / `nc_get_packet_status` | 查询在线终端信息 / 后端提供的连接统计 |
| `can_send_image` / `can_send_record` | 查询图片 / 语音发送能力 |

## 消息与会话

| 动作 | 用途 |
| --- | --- |
| `send_private_msg` / `send_group_msg` / `send_msg` | 私聊发送 / 群聊发送 / 按会话类型发送 |
| `delete_msg` / `get_msg` | 撤回 / 查询指定消息 |
| `get_group_msg_history` / `get_friend_msg_history` | 查询群聊 / 好友历史 |
| `mark_msg_as_read` / `mark_group_msg_as_read` / `mark_private_msg_as_read` / `_mark_all_as_read` | 标记消息或会话已读；最后一项是批量标记扩展 |
| `get_forward_msg` | 读取合并转发的节点 |
| `send_forward_msg` / `send_group_forward_msg` / `send_private_forward_msg` | 构造并发送合并转发 |
| `forward_friend_single_msg` / `forward_group_single_msg` | 将单条消息转发给好友 / 群 |
| `set_msg_emoji_like` | 为消息设置表情回应 |
| `set_essence_msg` / `delete_essence_msg` | 设置 / 移除群精华消息 |

## 好友与群成员

| 动作 | 用途 |
| --- | --- |
| `get_stranger_info` / `get_friend_list` / `get_friends_with_category` | 查询用户资料、好友列表、分组好友 |
| `set_friend_remark` / `delete_friend` / `send_like` | 设置好友备注、删除好友、点赞 |
| `get_group_list` / `get_group_info` / `get_group_info_ex` / `get_group_detail_info` | 列出群、查询群资料和扩展详情 |
| `get_group_member_info` / `get_group_member_list` | 查询单个成员 / 成员列表 |
| `get_group_honor_info` / `get_group_shut_list` / `get_group_at_all_remain` | 查询群荣誉、禁言名单、全体提醒剩余额度 |
| `group_poke` / `send_group_poke` / `friend_poke` / `send_poke` | 群内或好友戳一戳 |
| `send_group_sign` / `set_group_sign` | 群签到及其兼容名称 |

## 群管理与请求

| 动作 | 用途 |
| --- | --- |
| `set_group_name` / `set_group_remark` / `set_group_card` / `set_group_special_title` | 修改群名称、个人群备注、成员群名片或专属头衔 |
| `set_group_admin` / `set_group_kick` / `set_group_kick_members` | 设置管理员、移除单个或多个成员 |
| `set_group_ban` / `set_group_whole_ban` | 成员禁言 / 全群禁言 |
| `set_group_anonymous` / `set_group_anonymous_ban` | 匿名模式和匿名禁言兼容动作；能力以当前后端为准 |
| `set_group_leave` | 退出群聊 |
| `get_group_system_msg` / `get_group_ignore_add_request` / `get_group_ignored_notices` | 读取群申请或被过滤的请求通知 |
| `set_friend_add_request` / `set_group_add_request` | 处理好友 / 群申请，使用原事件的 flag |
| `.handle_quick_operation` | 将快速操作应用到对应事件 |

## 媒体与文件

| 动作 | 用途 |
| --- | --- |
| `get_image` / `get_record` / `get_video` / `get_file` | 读取图片、语音、视频或缓存的原始媒体 |
| `get_group_root_files` / `get_group_files_by_folder` | 列出群根目录 / 指定文件夹 |
| `get_group_file_url` / `get_private_file_url` | 获取群文件 / 私聊文件的下载地址 |
| `get_group_file_system_info` | 查询群文件数和空间用量 |
| `create_group_file_folder` / `delete_group_folder` | 创建 / 删除群文件夹 |
| `move_group_file` / `rename_group_file` / `delete_group_file` | 移动、重命名或删除群文件 |
| `rename_group_file_folder` / `set_group_file_folder_name` | 重命名群文件夹及其兼容名称 |
| `trans_group_file` | 群文件转存扩展 |
| `download_file` / `clean_cache` | 下载到网关缓存 / 清理缓存 |
| `ocr_image` / `.ocr_image` | 图片文字识别及兼容名称 |

## 其他兼容动作

| 动作 | 用途 |
| --- | --- |
| `check_url_safely` | 查询链接安全状态 |
| `get_cookies` / `get_csrf_token` / `get_credentials` / `get_clientkey` | 与登录凭证有关的兼容接口；响应可能包含账号敏感信息 |
| `get_rkey` / `nc_get_rkey` | 媒体资源访问参数 |
| `set_restart` | 重启兼容动作；以当前运行实例报告的支持情况为准 |
