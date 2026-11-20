# 群资料、成员与群文件

群号贯穿这组接口。用 `get_group_list` 找到机器人所在的群，再把返回的 `group_id` 传给下一步。

| 想做什么 | v11 动作与参数 | 读取结果 |
| --- | --- | --- |
| 查看群成员 | get_group_member_list(group_id) | 遍历成员记录，按 user_id 区分 |
| 查看一个成员 | get_group_member_info(group_id, user_id) | 群名片、昵称等后端提供的资料 |
| 看根目录 | get_group_root_files(group_id) | files 和 folders 两个集合 |
| 打开子目录 | get_group_files_by_folder(group_id, folder_id) | 下一级文件和文件夹 |
| 获取下载链接 | get_group_file_url(group_id, file_id, busid) | data.url |
| 看存储额度 | get_group_file_system_info(group_id) | file_count、limit_count、used_space、total_space |

## 从列表拿到下载地址

先列根目录。从同一条文件记录取 `file_id` 和 `busid`，连同群号请求下载地址。不要把文件名当成 file_id，也不要把文件夹的 ID 用作文件 ID。Python 示例的 `group-files` 和 `group-file-url` 对应这两步。

群成员管理动作还受登录账号在 QQ 群里的权限约束，接口存在并不保证账号能执行。
