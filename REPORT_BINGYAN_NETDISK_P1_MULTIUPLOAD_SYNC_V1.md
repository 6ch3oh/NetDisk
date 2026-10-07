# BINGYAN-NETDISK-P1-MULTIUPLOAD-SYNC-V1

日期：2026-10-06。分支：feature/full-netdisk-task。结果：PASS。未 commit/push，保留接管前全部未提交改动和索引。

## 验收结果

| 项目 | 结果 | 本轮实际依据 |
|---|---|---|
| MULTI_UPLOAD | PASS | 原生网页文件输入选择 3 个项目内合成文件，全部成功，逐文件状态/完成总数和下载内容一致；另一次 3 文件批次的非法名称收到真实 HTTP 400，其他 2 项成功；JS errors 为空 |
| AUTO_SYNC | PASS | Windows CLI 独立进程首次备份 3 文件，新建/修改自动上传，重启不增加未变文件，第二进程锁拒绝；本地删除后 5 个云端版本保留，逐个下载摘要一致 |
| 续传/失败/去重/安全 | PASS | 8 项真实 HTTP/MinIO 测试，含部分故障继续、跳过已有片、客户端及后端重启、完成回执丢失/过期对账、持久重试上限、变化中断防混合、Session 自动重新登录、状态损坏/账号目标绑定/路径 ID 拒绝、真实 MinIO 对象摘要及 Cookie 隔离 |
| TESTS | PASS | gofmt clean；go test ./...：42 项顶层 PASS；go vet ./...；Node 18 项 PASS；8 项同步专项 race PASS；Windows/Linux 构建；PowerShell 启动器实际运行及语法 PASS |
| 既有核心兼容 | PASS | 注册/登录/Session/CSRF、文件/目录隔离及 CRUD、分享、去重并发/ref_count、Range、分片、MinIO、ZIP、真实 TCP NFSv3、P2P TLS 及邮箱/统计/策略回归 |
| kernel NFS mount | NOT_RUN | 可选 opt-in 测试跳过；本轮 TCP NFSv3 真实协议回归已执行，未借用历史 mount 成绩 |

## 本轮变更

新增：

- cmd/netdisk-sync/main.go：轮询/单次 CLI、有限重试参数、密码 stdin/环境输入、信号退出。
- internal/backup/client.go、engine.go、lock_unix.go、lock_windows.go：沿用现有 API、内容版本、相对目录映射、分片续传、原子持久状态、账号/源/目标绑定、进程锁、读取边界。
- internal/netdisk/backup_test.go、backup_minio_test.go：8 项实际后端/SQL/文件/网络测试，故障仅在真实请求边界注入，绝非用 mock 代替实现。
- scripts/sync.ps1：隐藏密码输入、调用者相对目录解析、退出后环境清理；scripts/sync-smoke.cjs：显式隔离 loopback 服务上的独立 CLI 验收。
- docs/automatic-backup.md、TASK_BINGYAN_NETDISK_P1_MULTIUPLOAD_SYNC_V1.md、本 REPORT 和 p1 证据。

更新 README.md、TASKS.md、DEVLOG.md，详细说明留在独立文档。本轮没有改后端业务/schema 或前端业务：多选/3 并发/逐项状态/自动分片已存在，先接管再重新真实验收，未从头重写。初始已有的改动和文件完整保留。

构建产物（Git 忽略）：bin/netdisk-sync.exe、bin/netdisk-sync。其他最终 core/P2P 验证产物及合成夹具在 .tmp/p1 和 .tmp/p1-*。

## 启动

先使用 README 的独立演示服务（默认 http://127.0.0.1:38125），注册自己的 NetDisk 账号。项目目录：

~~~powershell
.\scripts\sync.ps1 -Username '你的用户名' -Root '明确指定的已有本地目录' -Server 'http://127.0.0.1:38125'
~~~

需重建 Windows 客户端：

~~~powershell
docker compose --profile tools run --rm --no-deps --name bingyan-netdisk-full-sync-build -e GOOS=windows -e GOARCH=amd64 tools go build -buildvcs=false -trimpath -o bin/netdisk-sync.exe ./cmd/netdisk-sync
~~~

停止用 Ctrl+C，相同参数重新启动恢复；多个源须用不同 State 路径。完整 Linux 命令、选项、恢复方式见 docs/automatic-backup.md。源码可从零重建，不依赖隐藏的后台 Agent。

## 已知限制

- 单向轮询普通文件，默认 10 秒；没有 GUI、OS watcher、自启动服务、空目录/链接同步、双向删除或自动还原。每轮完整流式哈希，大目录应延长间隔。
- 内容修改保留旧云端版本并消耗逻辑配额；不自动清理云端版本。仅取消自身未完成上传会话，绝不调用文件删除传播本地删除。
- 每个当前版本默认最多 5 次，次数跨重启；达到上限须修复原因后显式 retry-failed。未知 ID 的中断创建会话可能暂留配额，由后端到期回收。
- 同一状态已加 OS 锁；不同状态对同一源/目标的多个客户端不支持并行运行。状态含路径元数据，需私有保存。
- 已确认文件被手动删改云端后，若本地不变不会自动修复；经审查使用新状态对账可补缺失版本。不保留权限/时间戳/硬链接身份。
- kernel NFS mount 本增量 NOT_RUN。Linux CLI 已构建，Go/Linux Engine.Run 和锁真实运行；独立 CLI 进程验收平台为 Windows。

## 过程与边界

普通命令工具 helper 无法启动，切换到本线程的设备命令接口。Git 只用单条命令 safe.directory 参数，没有修改全局配置。PowerShell/sh 引号、初始测试 helper 返回类型/未用 import、浏览器相对文件路径和一次失败样本替换错误均已修正，并真实复测。最终原生绝对路径文件输入通过；先前 DataTransfer 也是实际浏览器/后端请求，没有网络 mock。

测试数据只在 t.TempDir、项目内合成夹具及新建 bingyan-netdisk-full-p1-* tmpfs 容器。MinIO 为本地隔离服务，复用项目已有编译工具/缓存，无云账号、付费服务或真实邮件。没有公开部署、Git remote 修改、用户数据删除、live 卷挂载/读写/迁移/重启。额外子 Agent/OpenCode/DeepSeek/后台 codex exec 均为 0；精确模型/API 调用数不可核实，不声称满足“≤8 次底层请求”。

证据：evidence/p1-checks.txt、p1-regression-go.txt、p1-node-checks.txt、p1-backup-race.txt、p1-sync-cli-checks.txt、p1-launcher-checks.txt、p1-browser-checks.txt、p1-multiupload-smoke.png。截图仅合成用户名/文件，无凭据或 capability。只清理本轮自己创建的容器和浏览器，不清理用户文件或既有卷。
