# NetDisk 当前任务规则

TASK = BINGYAN-NETDISK-FULL-REQUIREMENTS-VISIBLE-APP-001
PROJECT = BINGYAN-NETDISK
PROJECT_PATH = E:\冰岩实习\NetDisk

- 最新用户授权：继续完成 NetDisk 任务书全部功能要求。此前 S1/S2/S2B/S3 的“冻结业务/完成后停止”规则均为历史规则，不再限制当前任务。
- 当前 Codex Desktop App 可见线程是唯一写入执行线程；禁止后台 `codex exec`，禁止 OpenCode、DeepSeek、额外子 Agent。
- 先接管当前 `feature/full-netdisk-task` 分支上未提交的已有改动；不得 reset、clean、丢弃或从头覆盖。
- 必须先完成并真实验收：分享、用户资料/账户删除、内容去重、Range、分片断点续传、MinIO/S3 对象存储、文件夹 ZIP。
- 上述通过后继续完成：NFS 映射、P2P 直传演示客户端。
- 再完成任务书扩展：邮箱绑定/本地验证码与密码修改、用户统计和配额、分享访问日志/分析、可解释且可测试的存储策略。
- README 保持简短；详细接口、设计、过程和验收写入 DEVLOG.md、TASKS.md、evidence/full-requirements-checks.txt。
- 仅允许修改 `E:\冰岩实习\NetDisk`；不得扫描父目录、其他项目或整盘，不修改全局 Git/Codex/Docker 配置、中转站、n8n。
- 绝不挂载、读写、迁移、重启、清空或删除 live 数据卷 `bingyan-netdisk-data`。所有迁移/兼容/MinIO/NFS/P2P 测试只能使用 t.TempDir、fixture 或全新隔离测试容器/volume/tmpfs。
- 可以创建名称以 `bingyan-netdisk-full-` 开头的隔离测试资源；测试后只清理自己创建的资源。
- 不使用真实云账号、付费服务或真实邮件平台；对象存储用本地 MinIO，邮件验证码用本地开发适配器。
- 不 commit、不 push、不修改 Git remote；全部功能与验收完成后等待用户确认再提交。
- 不以 TODO、空接口、mock 或未运行测试冒充完成；不可验证项必须标记 NOT_RUN/BLOCKED 并继续其他可做部分。
- 保持已有注册/登录/Session、文件 CRUD、逻辑文件夹、用户隔离和网页兼容，不得回退。
- 安全要求：后端 user_id 所有权校验、CSRF、bcrypt、Session 原 token 不落库、分享/P2P token 高熵、SQL 参数化、路径/Zip Slip/NFS 越权防护、chunk IDOR 防护、并发 dedupe/ref_count 一致性、失败可恢复。
- 最终必须跑 gofmt、go test ./...、go vet ./...，以及各专项真实集成/浏览器验收；证据只使用合成数据且不得泄露凭据。
- 全部任务完成后停止并输出逐项 PASS/PARTIAL/BLOCKED 报告，不自动提交或推送。
