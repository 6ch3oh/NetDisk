# NetDisk 当前任务规则

TASK = BINGYAN-NETDISK-FINAL-CLOSEOUT-001
PROJECT = BINGYAN-NETDISK
PROJECT_PATH = E:\冰岩实习\NetDisk

## 当前用户明确授权

用户已明确授权执行 NetDisk 最终封版流程：
1. 审查当前未提交改动；
2. 做一次最终全量回归；
3. 创建最终 Git 提交；
4. 将完整版本合并/快进到 main；
5. push 到现有 GitHub origin/main；
6. 在严格备份和副本迁移验证通过后，把正式 38120 服务切换到完整版本；
7. 验证原有账号、文件、目录和内容在迁移后保持完整。

本授权覆盖本任务所需的 commit、main 更新、push、正式服务重建/启动和 live 数据库 schema 迁移；不得扩大到其他项目、其他仓库或其他服务。

## 执行方式

- 当前 Codex Desktop App 可见线程是唯一写入执行线程。
- 禁止后台 codex exec。
- OpenCode=0，DeepSeek=0，不启用额外子 Agent。
- 仅操作 E:\冰岩实习\NetDisk 及该项目自己现有/新建的 bingyan-netdisk* Docker 资源。
- 不扫描父目录、其他项目或整盘，不修改全局 Git/Codex/Docker 配置。

## 安全封版顺序（不得跳步）

### A. 现状审查
- 核对当前分支、HEAD、main、origin/main、工作树、暂存区、remote。
- 审查所有 modified/untracked 文件，排除 .tmp、bin、dist、数据库、数据卷备份、真实凭据、Cookie、Session token、分享/P2P capability、预签名 URL、真实用户内容等不应提交内容。
- 识别哪些是已验收源码/测试/文档/脱敏 evidence，哪些只是临时产物。
- 不 reset --hard，不 clean，不丢弃未知改动。

### B. 最终回归
必须在隔离数据上重新运行当前最终源码的最低完整回归：
- Node 前端行为测试：批量上传、自动分片/续传、勾选批量操作、sync 客户端相关测试。
- gofmt -l 必须为空。
- go test ./...
- go vet ./...
- build cmd/netdisk、cmd/netdisk-p2p、cmd/netdisk-sync（如存在）。
- 真实 MinIO 集成。
- NFS 实际协议/可用环境下 kernel mount 验证。
- P2P 两独立客户端直传及 hash。
- 浏览器 smoke：注册/登录、目录、普通多文件上传、自动分片入口、批量勾选、分享、ZIP、账户设置；使用隔离服务。
- 所有测试不得挂载 bingyan-netdisk-data。

任何真实失败必须先修复并重新针对性测试；不得用旧证据代替当前源码的关键回归。

### C. live 数据备份与副本迁移演练
在触碰 live schema 前：
- 精确确认正式容器和 live volume：bingyan-netdisk-app / bingyan-netdisk-data。
- 确认没有其他容器写入 live volume；必要时只停止正式 app。
- 对 bingyan-netdisk-data 做一致性的完整卷备份到项目 .tmp 私有目录或新只读备份介质，备份不得进 Git。
- 保存备份 SHA-256/manifest、SQLite integrity_check/foreign_key_check、用户/文件/文件夹/会话等数量摘要；不得输出密码 hash、session 原 token 或真实秘密。
- 对所有现有 blob 做内容哈希/大小 manifest，保存在 .tmp 私有目录，不提交。
- 把备份恢复到一个全新的隔离测试 volume，然后用“当前最终版本”在副本上执行实际 schema 迁移/启动。
- 副本迁移后验证：SQLite integrity/foreign keys、账号/文件/目录计数、原文件元数据和 blob 内容 hash/size 与迁移前 manifest 一致；原有下载可读。
- 再在副本上做一次重启验证。
- 副本迁移不通过：禁止继续 live 部署，报告 BLOCKED。

### D. Git 封版
只有 B、C 都 PASS 后：
- 按明确白名单 git add，不使用 git add .。
- 创建一个规范的最终提交，包含本轮已验收的批量上传、自动分片/续传、勾选批量操作、netdisk-sync 自动备份、相关测试、简短 README 和脱敏 evidence。
- 不提交 .tmp、bin、dist、数据库、卷备份、真实用户内容、凭据、缓存。
- 复核提交内容和敏感信息扫描。
- main 若只是祖先关系，优先 fast-forward 到 feature/full-netdisk-task；如有分叉，禁止粗暴覆盖，先分析。
- push 现有 origin main；禁止 force push，禁止修改 remote。
- push 后核对 origin/main 与本地 main commit 一致。

### E. 正式 38120 切换
只有 B、C、D 均 PASS 后：
- 使用 main 当前最终源码构建新的正式 netdisk 二进制到临时路径，构建 PASS 后再原子替换 bin/netdisk；原旧二进制先备份到 .tmp 私有目录。
- 正式 compose 继续使用原 bingyan-netdisk-data；不得删除、重建或清空该 volume。
- 启动/重建正式 bingyan-netdisk-app，让应用对 live 数据执行已经在副本验证过的兼容迁移。
- 启动失败或迁移验证失败：立即停止正式 app，不重复试错；使用预先保存的完整备份和旧二进制回滚到部署前状态，并验证旧版可用。保留失败证据。
- 启动成功后验证 /healthz 和首页。
- 用迁移前私有 manifest 核对原用户/文件/目录计数与内容哈希/大小不变。
- 不需要也不得读取用户密码原文。
- 允许创建一个随机合成临时账号做正式环境 smoke：多文件上传/下载、目录、分享等；测试完成必须删除该合成账号并确认原用户数据计数回到迁移后基线。
- 不删除或改名任何原有用户文件。

### F. 最终状态
最终要求：
- main = origin/main = 最终封版 commit；
- 工作树干净，或仅保留明确说明的 ignored 私有备份；
- 38120 正式服务健康；
- 原有用户数据迁移完整；
- 38125 demo 可停止，不作为正式入口；
- GitHub 仓库展示最终 README 和源码；
- 不触碰其他项目。

## README 与文档

- README 保持简短，不恢复成长手册。
- 详细最终结果追加到 DEVLOG.md、TASKS.md、evidence/final-closeout-checks.txt。
- evidence 只写合成/脱敏信息，不写真实凭据、Cookie、完整 capability token、预签名 URL 或真实用户文件内容。

## 停止条件

- 副本迁移不通过 -> 停止，不触碰 live schema。
- 最终回归存在未解决失败 -> 停止，不 push、不部署。
- Git main 出现意外分叉/远端新提交 -> 停止分析，不 force push。
- live 正式切换失败 -> 自动回滚到本任务开始前的完整卷备份 + 旧二进制，并验证恢复；不得继续试错。
- 除上述 NetDisk 封版授权外，不执行任何付款、外部账号安全设置、删除其他项目文件等动作。

## 最终报告必须给出

TASK
RESULT
FINAL_COMMIT
MAIN_EQUALS_ORIGIN_MAIN
FULL_REGRESSION
NODE_TESTS
GOFMT
GO_TEST
GO_VET
BUILD
MINIO
NFS
P2P
BROWSER_SMOKE
LIVE_BACKUP
COPY_MIGRATION
LIVE_MIGRATION
ORIGINAL_DATA_INTEGRITY
FORMAL_URL
FORMAL_HEALTH
SYNTHETIC_LIVE_SMOKE
GITHUB_PUSH
WORKTREE
LIVE_DATA_DELETED=NO
ORIGINAL_USER_FILES_MODIFIED=NO
OPEN_CODE_CALLS=0
BACKGROUND_CODEX_EXEC=0
BLOCKERS
