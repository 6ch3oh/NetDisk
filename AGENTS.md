# NetDisk 项目规则

当前任务：BINGYAN-NETDISK-S3-DELIVERY-CLOSEOUT-001；项目：BINGYAN-NETDISK。

- 仅在 E:\冰岩实习\NetDisk 工作，不扫描上级、其他项目或用户目录，不复制其他项目内容。
- S1/S2/S2B 业务已冻结。S3 只允许必要文档、.tmp 私有检查点/解压验证、dist 包/校验和 evidence/s3-delivery-checks.txt；不改业务源码、下载、CSP、数据库结构或现有数据。
- 仅 Codex 当前主任务；不使用 OpenCode、DeepSeek、其他模型或子 Agent，不使用持续 goal。模型精确 ID/API 请求数不能核实则 UNKNOWN。
- 不修改全局配置、其他项目、其他容器或卷、Docker Desktop，不公网发布、登录外部账号、付款或删除真实数据。
- Docker 名称以 bingyan-netdisk 开头；复用 golang:1.27.0。现有服务只绑定 127.0.0.1:38120。源码包验证使用独立 docker run、新临时数据，不运行解压目录 Compose，不接入现有数据卷、不重启原服务。
- Git 只按审查白名单暂存；原暂存树/索引先做可恢复检查点。不 git add .，不清空暂存区，不 reset --hard 或 clean。S3 不 commit/push，不伪造身份、历史或日期。
- 数据库、上传内容、凭据、备份、缓存、.tmp、bin、dist 不进入 Git 或源码包。只用合成测试数据；截图和文本均需审查。
- 开始 2026-10-01 21:05:31 UTC+8，截止 21:25:31。业务缺陷、数据风险、敏感内容未解决、构建失败或超过时间预算时停止，据实记录；未运行检查写 NOT_RUN。
- 完成 S3 后停止，不进入新功能。安全与部署规则是用户实施方案，不冒称题目原文。
