# NetDisk

Go + net/http + SQLite 多用户网盘。

已实现网页文件/目录管理、分享、账户设置、去重、Range、断点上传、ZIP、S3/MinIO、逻辑目录 NFS 和客户端 P2P 直传；扩展包含本地邮箱验证码、密码修改、配额统计、分享分析与冷内容迁移策略。

运行需要 Docker、Compose 和已有 `golang:1.27.0` 镜像。Windows：
```powershell
.\scripts\full-demo.ps1
```
Linux：
```sh
sh scripts/full-demo.sh
```
访问 <http://127.0.0.1:38125/>，独立演示卷为 `bingyan-netdisk-full-demo-data`；邮箱验证码在设置页显示，不发送真实邮件。本轮未部署到现有 live 数据卷。设计与真实验收见 [DEVLOG](DEVLOG.md)、[TASKS](TASKS.md)、[协议演示](docs/full-protocols.md) 和 [对象存储](docs/object-storage.md)。

限制：单实例；NFSv3 无传输加密/锁/ACL，同名逻辑条目需先改名；P2P 为 LAN/loopback TLS 演示，不含 NAT 穿透。对象直链最长 120 秒；未公网部署。
