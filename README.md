# 佳琛网盘

Go + net/http + SQLite 多用户网盘。

已实现网页文件/目录管理、勾选批量下载/分享/重命名/移动/删除、账户设置、去重、Range、断点上传、ZIP、S3/MinIO、逻辑目录 NFS 和客户端 P2P 直传；扩展包含本地邮箱验证码、密码修改、配额统计、分享分析与冷内容迁移策略。

普通上传支持多选、最多 3 个文件并发；超过 300MB 自动分片，默认单文件上限与账户配额均为 1GiB。

本地目录单向自动备份：构建项目内 `netdisk-sync` 后运行 `scripts/sync.ps1 -Username 用户名 -Root 明确目录`。支持新增/修改、续传和持久重试，保留云端版本，不传播本地删除；[启动与限制](docs/automatic-backup.md)。

支持服务器文件自动下载到移动硬盘、分段续传与哈希校验、校验后归档释放在线空间、历史版本及新服务器恢复；网页提供硬盘归档记录。[硬盘备份与恢复](docs/removable-backup.md)。

运行需要 Docker、Compose 和已有 `golang:1.27.0` 镜像。Windows：
```powershell
.\scripts\full-demo.ps1
```
Linux：
```sh
sh scripts/full-demo.sh
```
访问 <http://127.0.0.1:38125/>，独立演示卷为 `bingyan-netdisk-full-demo-data`；邮箱验证码在设置页显示，不发送真实邮件。本轮未部署到现有 live 数据卷。设计与真实验收见 [DEVLOG](DEVLOG.md)、[TASKS](TASKS.md)、[协议演示](docs/full-protocols.md) 和 [对象存储](docs/object-storage.md)。

限制：单实例；NFSv3 无传输加密/锁/ACL，同名逻辑条目需先改名；P2P 为 LAN/loopback TLS 演示，不含 NAT 穿透。对象直链最长 120 秒；本地运行，电脑关机时无法访问，未公网部署。

最终封版检查：BLOCKED，副本迁移清理了两份重复物理 blob，未通过严格 manifest 门槛；正式 38120 尚未切换。详见 [封版检查](evidence/final-closeout-checks.txt)。
