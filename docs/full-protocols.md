# Full Requirements：协议与演示

本轮仅在隔离容器、新演示卷和 `t.TempDir` 测试，未更新 `bingyan-netdisk-data`。不要运行旧 `scripts/verify.ps1`：该历史脚本会连接 38120 并重启原服务。本轮验收使用新 Go 测试。

## 勾选与批量操作

文件与文件夹前均有勾选框；表头全选只作用于当前目录，部分选择显示中间状态，切换目录/退出清除选择。批量重命名允许逐项输入名称，移动排除选中文件夹和子树作为目标，删除有确认并明确包含文件夹内容；沿用各资源的所有权/CSRF 校验，逐项执行与显示结果，失败继续其余项。成功的移动/重命名/删除取消勾选；分享部分失败时只保留失败项供重试，先前成功链接在当前页内存中保留可复制，导航/退出清空，刷新不能找回原 token。

`GET /api/download-selection?file_ids=<逗号分隔ID>&folder_ids=<逗号分隔ID>`（含 HEAD/Range）：每次校验全部本人资源，1–200 个去重条目；选中文件置于 ZIP 根目录、选中目录保留嵌套结构，重叠选择去重，同名稳定后缀防覆盖。复用现有 ZIP 流式写盘、15 分钟缓存、资源修改失效与 durable 清理。整组含外人/不存在资源时拒绝归档，路径仍按 Zip Slip 防护校验。

`DELETE /api/folders/{id}/tree`：明确的递归删除接口，原空文件夹删除接口保持原语义。先按 user_id 收集子树，在事务中释放文件引用、登记分片清理标记、取消会话并删除目录；只清理零引用 blob，共享副本保留。GC 失败返回 503，零引用 tombstone/清理表保留供恢复，不假报物理清理完成。

真实增量验收见 `evidence/selection-checks.txt`，只使用临时夹具、隔离 tmpfs 和自建 demo 的新检查账号。

## 普通上传与自动续传

统一使用普通上传选择框，支持多选和逐文件结果。后端 `/api/config` 返回大小限制、300,000,000 字节阈值与 8MiB 分片大小；恰好 300MB 走原 `POST /api/files`，更大文件由客户端自动调用已有分片 API。`NETDISK_MAX_UPLOAD_BYTES` 默认 1GiB，仍受账户剩余配额和会话预留约束。每批最多 3 个文件并发，每个大文件逐片发送，超限文件在请求前跳过。

分片创建的可选 `request_key` 是随机 128-bit 幂等键，按用户隔离；重复创建返回同一会话，参数变化返回 409。完成回执与文件发布同事务提交，重试 complete 不增加文件或 ref_count；回执随文件/账户删除或 24 小时到期清理。旧客户端不传该键仍兼容。写接口统一要求 Session 与 CSRF header。

浏览器后台对网络失败/408/429/5xx 最多尝试 3 次。当前标签页的 sessionStorage 保存会话元数据和已上传片的 SHA-256，不存登录 token；刷新后重新选择同一文件自动查状态、校验内容并跳过已上传片。更换内容会拒绝继续；退出登录/成功后清除记录。禁止存储的浏览器退回内存缓存，仅本次页面保留记录；关闭标签页后不能自动找回会话，服务端到期释放。分片摘要使用 WebCrypto，演示为 localhost，其他网页部署需 HTTPS。

## NFSv3

可选 `NETDISK_NFS_ADDR` 启动成熟 `github.com/willscott/go-nfs v0.0.4` server。原生运行建议 `127.0.0.1:2049`；Docker 演示监听容器内 `0.0.0.0:2049`，只发布宿主 `127.0.0.1:38126`。连接仅接受 loopback/private IP。

登录后 `POST /api/nfs/exports`（或设置页）创建 256-bit capability 导出路径；`GET /api/nfs/exports` 列出不含 token 的导出，`DELETE /api/nfs/exports/{id}` 撤销。有效期 24 小时，账户删除/密码修改使导出失效。每次操作重新验证导出和 `user_id`，包括已经挂载和已经打开的文件。

在启用 NFS client、允许 mount 的 Linux 环境中，将刚创建的路径放入私有 shell 变量 `NFS_EXPORT_PATH`，再执行：

```sh
mkdir -p /tmp/netdisk-mount
mount -t nfs -o vers=3,tcp,port=38126,mountport=38126,nolock \
  "127.0.0.1:$NFS_EXPORT_PATH" /tmp/netdisk-mount
# 完成后：umount /tmp/netdisk-mount
```

它映射 SQLite 逻辑目录，不导出 blob 目录、数据库或真实宿主路径。写入使用有大小限制的 scratch 文件，Close 时核对并发版本，在事务中替换原文件内容、调整 dedupe 引用和配额；失败保留原内容。文件 ID/分享引用在覆盖时保持。创建、读写、seek/truncate、目录、移动、删除和 mode/mtime 实际实现；不支持硬/软链接、NFS 锁和 ACL。重复逻辑名称会拒绝歧义访问，先在网页按文件 ID 改名。NFS 本身无传输加密，仅用于本地/可信私有网络。

真实验证入口：`TestNFSRealProtocolLogicalReadWriteDirectoriesIsolationRevocation`、`TestNFSReplacementQuotaAttributesChrootAndAccountDeletion`。`NETDISK_TEST_NFS_MOUNT=1` 启用 `TestNFSKernelMount`，需隔离 Linux 容器的 SYS_ADMIN 和 `nfs-common`；默认跳过会明确标 `NOT_RUN`。

## P2P

新增 `POST /api/shares` 的 `resource_type: "p2p"`（resource_id 为本人文件 ID）。此类分享不能通过普通匿名下载接口取得正文。所有者 `POST /api/p2p/{share_id}/offer` 登记私有 IP:port、SHA-256、大小和临时 TLS 证书 fingerprint；本地源必须与逻辑文件匹配。匿名 `GET /s/{token}` 仅返回连接信息，有效期 10 分钟。

客户端 `cmd/netdisk-p2p` 支持两个独立进程。将本人登录会话放入私有环境变量 `NETDISK_SESSION`，不写入源码、证据或命令日志。先在网页上传演示文件并取得 ID，然后：

```sh
# /test 为独立 demo 容器只读二进制目录；原生编译可替换为 bin 路径。
/test/netdisk-p2p send --server http://127.0.0.1:8080 \
  --file-id "$FILE_ID" --source /data/synthetic-source.bin \
  --listen 127.0.0.1:0 --link-file /data/private-peer-link
# 另一客户端读取私有 link 文件（LAN 时用实际私有 IP 和固定端口）：
/test/netdisk-p2p receive --link-file /data/private-peer-link --dest /data/received.bin
```

链接文件仅创建一次，不覆盖已有文件。接收端校验证书 fingerprint、长度和 SHA-256，再以无覆盖的原子方式发布新文件；坏 hash/token/证书不产生最终文件。发送端在发送正文前再次请求连接信息，撤销/源删除后不能用旧连接信息开始传输。应用仅提供 signaling，正文经两个客户端的 TLS 1.3 连接，未实现 NAT 穿透/TURN/公网服务，也未把内网演示说成互联网 P2P。

## The end? 扩展

- `POST /api/me/email-code`、`POST /api/me/email`：显式 `NETDISK_DEV_EMAIL=true` 开启本地模式，认证页面接收 6 位随机码；只存 hash，10 分钟过期、5 次错误上限、30 秒重发间隔、单次使用、邮箱唯一。不发送真实邮件。
- `POST /api/me/password`：校验旧 bcrypt 密码，生成新 bcrypt hash，事务中撤销全部登录 Session、NFS 导出和邮箱验证码。
- `GET /api/me/stats`：文件/目录数、逻辑用量、上传会话预留、本人去重内容大小。`NETDISK_QUOTA_BYTES` 默认 1 GiB；普通上传、分片预留/完成和 NFS 写入统一限额；删除/取消/过期释放额度。
- `GET /api/shares/{id}/analytics`：本人分享的目录访问、下载请求、P2P signaling 次数及最近 100 条事件，保留 30 天。只计已通过 capability/范围验证的请求；不计完成下载字节/独立访客，不存 IP、完整 token 或 presigned URL。
- `GET /api/storage/candidates`：本人 local blob >=1 MiB、请求次数 <=2、7 天未访问且文件已存在 7 天，最多 20 项。`POST /api/storage/policy` 同时需要 Session/CSRF 和 `X-NetDisk-Maintenance`，执行已有上传→远端完整 hash 验证→metadata 切换→durable 本地清理流程。该策略为可解释的人工执行策略，不声称真实云自动运维。

所有登录态写接口共享既有 CSRF 与参数化 SQL；私有资源按 `user_id` 校验。导出/分享只保存 capability hash。详细当轮结果见 `evidence/full-requirements-checks.txt`。
