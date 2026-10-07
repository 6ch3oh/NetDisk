# 服务器下载备份、硬盘归档与恢复

`netdisk-sync` 保留原上传模式，新增 `download`、`status`、`archive`、`restore`、`restore-all`。网页右上角的“硬盘归档”显示归档位置及恢复记录。自动备份由本机客户端运行，浏览器页面关闭不影响正在运行的客户端。

## 准备

服务端与客户端均须使用含本功能的版本。构建 Windows 客户端：

```powershell
docker compose --profile tools run --rm --no-deps -e GOOS=windows -e GOARCH=amd64 tools go build -o bin/netdisk-sync.exe ./cmd/netdisk-sync
```

在移动硬盘中手动创建一个空文件夹，例如 `X:\佳琛备份`。绑定状态保存在电脑上、硬盘备份文件夹之外。每个账号/备份目录使用不同的 `-State`；下面的用户名、服务器地址和盘符均需替换为自己的实际值。PowerShell 启动器交互式隐藏输入密码，密码不进参数、日志、绑定状态或备份清单。

## 首次下载与持续备份

```powershell
.\scripts\sync.ps1 -Mode download -Username your_user -Server https://disk.example.com -Root 'X:\佳琛备份' -State '.tmp\my-disk\binding.json' -InitDisk -DiskLabel '我的移动硬盘' -Once
```

首次初始化只接受空目录，写入随机硬盘标识、绑定清单和版本清单。之后使用相同目录及 State，去掉 `-InitDisk`、`-DiskLabel` 和 `-Once`，默认每分钟检查一次：

```powershell
.\scripts\sync.ps1 -Mode download -Username your_user -Server https://disk.example.com -Root 'X:\佳琛备份' -State '.tmp\my-disk\binding.json'
```

新增/修改的文件按 Range 分段下载。完成的分段写盘、同步后才保存校验信息；重启会重新校验已保存分段，再继续下载。整个文件 SHA-256/大小一致后才发布本地版本。内容、名称或逻辑路径变更会保留对应历史记录；服务器删除文件不删除本地备份。

硬盘不在、盘符指向其他硬盘、标识不符、空间不足或清单损坏时暂停写入，服务器文件继续保留。默认保留 16 MiB 空闲空间，可用 `-ReserveBytes` 调整。网络失败默认最多尝试 5 次并持久保存退避时间，解决问题后加 `-RetryFailed` 可重置未完成项。Ctrl+C 结束；重新运行可续传。

“未变化”的完整文件会在本地重新核对内容，不重复下载；完整本地扫描有磁盘 I/O 成本，大目录可增大 `-Interval`，例如 `10m`。

## 查看备份并归档一个文件

```powershell
.\scripts\sync.ps1 -Mode status -Username your_user -Server https://disk.example.com -Root 'X:\佳琛备份' -State '.tmp\my-disk\binding.json'
```

状态列出文件 ID、名称及是否已备份/归档/恢复。选择需要释放在线空间的一个文件 ID：

```powershell
.\scripts\sync.ps1 -Mode archive -FileId 文件ID -Username your_user -Server https://disk.example.com -Root 'X:\佳琛备份' -State '.tmp\my-disk\binding.json'
```

归档是单独、显式的操作，持续下载不会自动清理服务器。客户端先重新校验完整本地文件；服务端在同一事务中核对内容摘要、大小、名称和完整目录链，保存归档记录后移除在线文件引用。过期备份、目录移动或内容修改会拒绝归档，须重新备份后再操作。原文件分享随引用移除失效；其他账号或其他文件仍引用的相同内容继续保留，所以释放的物理空间可能小于逻辑文件大小。存储清理失败会显示 pending，服务端保留可重试的清理记录。

归档请求丢失响应可用相同命令重试，持久回执避免重复执行。网页“硬盘归档”显示原目录、文件名、硬盘标签、硬盘内位置和恢复状态。

## 恢复

恢复一个文件（可直接使用归档前的文件 ID）：

```powershell
.\scripts\sync.ps1 -Mode restore -FileId 文件ID -Username your_user -Server https://disk.example.com -Root 'X:\佳琛备份' -State '.tmp\my-disk\binding.json'
```

恢复所有已记录文件的最新备份及保存的空目录：

```powershell
.\scripts\sync.ps1 -Mode restore-all -Username your_user -Server https://disk.example.com -Root 'X:\佳琛备份' -State '.tmp\my-disk\binding.json'
```

客户端重建逻辑目录，使用分片上传及请求回执恢复文件；新发布文件有新的 ID，原分享链接不恢复。原文件存在且内容/名称一致时跳过；已有的新内容继续保留，恢复历史版本会新增一个文件。重复恢复和恢复确认丢失可重试。指定历史版本时，增加 `-Revision`，其值为硬盘 `.netdisk-manifest.json` 中 `versions` 的键。若最新一次下载尚未完成，批量恢复会报告该项未完成；可以明确选择之前已校验的历史版本恢复。

服务器损坏后，可在新服务器建立一个新账号，再明确指定恢复目标：

```powershell
.\scripts\sync.ps1 -Mode restore-all -RestoreToOtherServer -Username recovery_user -Server https://new-disk.example.com -Root 'X:\佳琛备份' -State '.tmp\my-disk\binding.json'
```

该选项只允许读取已有备份并恢复，禁止在新目标执行下载或归档。旧电脑的 binding.json 丢失时，可用新的 `-State` 加 `-AdoptDisk` 显式读取硬盘清单恢复绑定；原硬盘标识与清单必须完整。盘符变化时指定实际 Root，绑定通过硬盘标识核对。

## 保存形式与范围

- `.netdisk-disk.json`：硬盘标识与原服务器/账号绑定。
- `.netdisk-manifest.json`：目录元数据、文件原名、版本摘要、下载进度与归档/恢复记录；应与文件一起保留。
- `versions/文件ID/SHA256/file-安全文件名`：可直接打开的原始文件字节。Windows 不支持的文件名字符会转换，原名称和目录在清单中保存，恢复时使用原逻辑名称。
- `parts/`：未完成的下载；完成后原子移入版本目录。

本功能备份当前账号的文件内容和逻辑目录，恢复受目标账号配额和单文件限制约束。账号密码、会话、分享链接、其他账号及服务端 SQLite 数据库不包含在这个客户端备份中；完整服务器灾难恢复仍使用一致性的整卷备份。硬盘只留一份的归档文件也需要独立副本保护。

历史本地版本不会自动清理；当前没有桌面 GUI、开机自启服务或自动按容量归档策略。运行中的客户端在重连后继续备份，退出后需重新运行。下载采用固定分段续传，暂未实现不同内容版本之间的分块差分。

## 隔离验收

`go test ./...` 覆盖断网续传、分段损坏、Range/哈希拒绝、换盘/空间/路径边界、删除保留、归档竞争、回执丢失、重启、历史版本和新服务器恢复。真实 MinIO 测试包含对象 Range、凭据隔离、最后引用删除及恢复。`node scripts/pull-smoke.cjs http://127.0.0.1:隔离端口` 使用项目内合成目录和原生客户端；服务端必须为专用隔离实例。
