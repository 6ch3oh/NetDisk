# BINGYAN-NETDISK-P1-MULTIUPLOAD-SYNC-V1

目标：收尾网页多文件上传和显式指定目录的单向自动备份。/goal 已建立；本文件作为 /plan 和项目内恢复检查点。

约束：仅当前可见线程写入；OpenCode=0、DeepSeek=0、子 Agent=0。不 commit/push、不改 remote、不接入第三方云盘、不触碰 bingyan-netdisk-data。仅指定测试目录、t.TempDir、全新 bingyan-netdisk-full- 隔离资源。默认不传播本地删除，不删除用户文件。保留接管的全部未提交改动。

计划（顺序实施）

1. 核对 AGENTS/README/TASKS/DEVLOG/测试/证据和分支，回归当前源码。
2. 多文件上传：接管已有 multiple/3并发/逐文件状态/错误隔离/总数反馈；运行行为测试与隔离浏览器至少三文件及单项失败验收，修复实际缺陷。
3. 自动备份：新增项目内 Go CLI，指定目录轮询、相对路径映射、内容哈希、版本保留、分片/重试上限、持久状态、单实例锁和并发上限；无删除请求。
4. 最低必要测试：实际 HTTP 后端中新建/修改/重启/删除保留/去重/中断续传/错误隔离/路径边界；gofmt、go test ./...、go vet ./...；构建 Windows/Linux 客户端。
5. 交付使用说明、脱敏 evidence、REPORT，列出结果、变更文件、启动命令和限制后停止。

接管事实

- 分支 feature/full-netdisk-task；已有 16 个 tracked 修改及上传/选择/验收等 untracked 文件，全部保留；索引不修改。
- 网页批量与自动分片已经实现，已有 Node 行为测试及历史真实浏览器证据；本轮仍须重新回归，不冒充新测试。
- 没有找到独立任务书/STATUS/HANDOFF/REPORT 文件；完整需求以 AGENTS、TASKS、DEVLOG、docs 和源码测试为依据。
- 普通 exec_command 的 Windows helper 启动失败；使用当前线程的 AgentDock 设备命令接口。Git 仅用本次命令 -c safe.directory，不修改全局配置。

基线：18 项 Node、go test ./...、go vet ./...、gofmt clean PASS。初次检查命令的 PowerShell/sh 引号错误已用项目内脚本修正；不属于源码测试失败。

MULTI_UPLOAD：真实浏览器三文件状态/下载内容 PASS；混合三文件的真实 400 和另外两项成功 PASS；无 JS errors。工具原生本地文件桥接返回网络错误；改用浏览器 DataTransfer 生成合成 File 后通过同一实际 UI/API，不使用网络 mock。截图 evidence/p1-multiupload-smoke.png。

自动备份客户端及验收完成；同项目无并发源码写入。

验收检查点（2026-10-06）

- MULTI_UPLOAD=PASS：最终浏览器原生文件输入选择三个绝对路径合成文件，状态/总数/下载内容一致；混合批次真实 400 + 另外两项成功，无 JS errors。
- AUTO_SYNC=PASS：Windows 独立进程 3 文件、新增/修改、重启去重、第二进程锁、删除保留、5 版本下载摘要。
- TESTS=PASS：8 项真实 HTTP/MinIO 同步测试 + race；gofmt、go test ./...（42 顶层 PASS）、go vet ./...、18 Node、Windows/Linux 构建。TCP NFS/P2P/MinIO/Range/ZIP 回归 PASS；可选 kernel NFS mount 本轮 NOT_RUN。
- 过程错误：PowerShell/sh 引号、初次测试 helper 返回类型/未用 import、浏览器相对路径及一次失败样本替换均已修正并真实复测，没有掩盖业务失败。
- 限制/恢复见 docs/automatic-backup.md；REPORT、DEVLOG、TASKS、p1 证据和 full-requirements-checks 增量已归档并审查。自己的两个 tmpfs 容器按创建 ID 验证后 stop/remove，三个浏览器 session 全部关闭；未删除任何既有卷。
- 最终结果 PASS，任务完成并停止，无 commit/push/remote/live 数据操作。
