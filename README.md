# NetDisk

冰岩实习 NetDisk 项目，使用 Go + SQLite 实现的多用户网盘。支持注册/登录、文件上传/下载/重命名/删除、逻辑文件夹管理、文件移动与用户隔离。

文件采用流式上传，真实文件内容存放在磁盘中，用户、Session、文件元数据和目录关系存放在 SQLite 中。

## 运行

Windows PowerShell：

```powershell
.\scripts\start.ps1
```

Linux：

```bash
sh scripts/start.sh
```

启动后访问：<http://127.0.0.1:38120/>

已知限制：暂未实现分享、断点续传、对象存储、NFS、P2P 等进阶功能。
