# 单向自动备份

工具：项目内 `cmd/netdisk-sync`。指定已有本地目录后，定期检查普通文件，将新增/修改内容上传到自建 NetDisk。网页仍使用现有多选入口，最多 3 个文件并发，有逐文件状态、失败隔离和完成总数。

## Windows 启动

在本项目目录运行。`bin/netdisk-sync.exe` 是本轮构建产物（Git 忽略），重建命令：

~~~powershell
docker compose --profile tools run --rm --no-deps --name bingyan-netdisk-full-sync-build -e GOOS=windows -e GOARCH=amd64 tools go build -buildvcs=false -trimpath -o bin/netdisk-sync.exe ./cmd/netdisk-sync
~~~

先启动自己的 NetDisk（本项目独立演示启动方式见 README，默认地址 `http://127.0.0.1:38125`），注册自己的账号，再运行：

~~~powershell
.\scripts\sync.ps1 -Username '你的用户名' -Root '明确指定的本地目录' -Server 'http://127.0.0.1:38125'
~~~

脚本隐藏输入密码，仅通过进程环境传入并在退出后清理；不写入参数、状态或日志。Root 相对路径按调用者当前 PowerShell 目录解析。默认状态为项目 `.tmp/netdisk-sync/state.json`，必须在源目录之外，每个源目录使用不同状态路径。例如先创建自己的测试目录，再运行：

~~~powershell
.\scripts\sync.ps1 -Username '你的用户名' -Root '.tmp\backup-example' -State '.tmp\backup-example-state\state.json' -RemoteName '我的自动备份'
~~~

首次备份目录已有文件，随后新增/修改在下一轮检查时上传。Ctrl+C 停止；相同参数再次启动可恢复。加 `-Once` 只检查一轮。达到重试上限后，修复原因再加 `-RetryFailed`，显式重置未完成文件预算。

## Linux 启动

Go 1.27；下面的隐藏输入示例使用 Bash：

~~~sh
go build -trimpath -o bin/netdisk-sync ./cmd/netdisk-sync
read -r -s -p 'NetDisk password: ' NETDISK_SYNC_PASSWORD; printf '\n'
export NETDISK_SYNC_PASSWORD
./bin/netdisk-sync -server http://127.0.0.1:38125 -username YOUR_USERNAME -root ./YOUR_EXPLICIT_DIRECTORY -state .tmp/YOUR_BACKUP_STATE/state.json
unset NETDISK_SYNC_PASSWORD
~~~

也可由凭据工具向 stdin 提供密码，并加 `-password-stdin`。不要把密码放入参数或脚本。无本机 Go 可用 Compose tools 构建 Linux 二进制。HTTP 只允许 loopback；其他地址须使用 HTTPS，正常验证证书。

## 行为和恢复

- 默认 10 秒轮询，2 个文件工作者，支持 1–8 并发；单文件依次发送分片，默认 8 MiB。小文件也使用分片 API，获得幂等和续传能力。限制/配额由后端执行。
- 相对目录映射为逻辑云端文件夹，不建立空目录。默认备份根名来自目录名和规范化绝对路径摘要，可用 `-remote-name` 指定，`-remote-parent` 选择本人目标目录 ID。
- 云端文件名是“原名最多 120 字符 + [相对路径 SHA-256-内容 SHA-256]”，避免截断和版本碰撞。不兼容的目录名会规范化并附原名摘要。状态 `entries` 保留完整相对路径与最新版本，便于对应原名。
- 每轮有限缓冲流式 SHA-256 识别变化，同大小/同修改时间的改动仍能检测。不同路径的同内容保留各自逻辑文件，物理内容由后端 dedupe/ref_count 共享。
- 修改创建新云端版本，保留旧版本；本地删除或重命名不删除云端文件。仅可能取消本客户端的未完成上传会话，以释放分片和预留配额。
- 状态绑定源目录、服务器、账号 ID、目标和分片大小；先保存随机 request_key 和尝试次数，临时文件 fsync 后替换状态。OS 进程锁在退出/崩溃时释放，留存的锁文件不需要删除。
- 默认每个路径的当前内容最多 5 次尝试，退避 30/60/120…秒，上限 15 分钟。次数和下一次时间跨重启保存，达到上限暂停，其他文件继续；新内容获得新预算，`-retry-failed` 只重置未完成项。401 最多自动重新登录并重试原请求一次。
- 续传查询已有片，逐片核对摘要，跳过一致的片。组合内容必须与扫描摘要一致才完成，源文件中途变化不会发布混合内容。
- 完成响应丢失/24 小时服务端回执过期：查确定的版本名，流式下载验证摘要后确认，避免再发布。S3/MinIO 跳转用无 Cookie 的独立客户端，防止相同主机不同端口泄露 Session。
- 状态损坏、绑定不符、锁冲突或写入失败会拒绝继续，不静默重建。保留状态，先排查；经审查确需新状态，相同源和目标可通过云端摘要对账恢复。
- `os.OpenRoot` 限制读取边界，跳过子级 symlink/junction、FIFO/设备等特殊条目，拒绝整盘根目录；状态目录必须在源之外，避免反复备份状态。

参数详见 `bin/netdisk-sync.exe -help` / `bin/netdisk-sync -help`。客户端沿用 Session、CSRF 请求头和后端所有权校验。

## 第一版限制

这是运行中的单向轮询进程，没有 GUI、开机自启服务、OS 实时 watcher、双向删除、版本清理或自动还原。完整扫描 I/O 与指定目录总大小成正比，大目录建议增大 `-interval`。仅备份普通文件内容，不保留权限/时间戳/硬链接身份，空目录及链接跳过。

旧版本占逻辑配额，配额不足会进入有限重试。创建会话响应丢失且源立即变化时，未知 ID 的旧会话可能暂留配额，后端到期回收。手动删除/更改云端后，已确认且本地未变的项不会自动修复；显式新状态对账可重新上传缺失版本。不同状态文件不能对同一源/目标并行运行；同一状态已强制进程锁。状态包含原始路径元数据，应保持私有。

## 本轮验收

8 项 Go 真实 HTTP/MinIO 同步测试及 race 通过；Windows CLI 独立进程完成 3 文件、新增/修改、进程重启、重复进程锁、删除保留及 5 版本下载摘要。全量 Go 42 项顶层测试和 Node 18 项通过；可选 kernel NFS mount 本轮 NOT_RUN，真实 TCP NFSv3 回归通过。详见项目 REPORT 和 `evidence/p1-checks.txt`。
