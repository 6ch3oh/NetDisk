# 可选 S3 兼容存储 / MinIO

默认不设置 S3 环境变量，只使用本地文件，不需要云凭据。客户端为锁定的 minio-go/v7 v7.0.95。新上传先进入本地统一去重路径，再由维护接口迁移共享 blob。冷内容候选策略由维护者显式执行，接口与阈值见 [协议演示](full-protocols.md)。

环境变量：`NETDISK_S3_ENDPOINT`（host:port）、`NETDISK_S3_BUCKET`、`NETDISK_S3_ACCESS_KEY`、`NETDISK_S3_SECRET_KEY`、`NETDISK_S3_SECURE`（默认 false）、`NETDISK_MAINTENANCE_KEY`。`NETDISK_S3_DOWNLOAD_ENDPOINT` 可指定浏览器能访问的地址；内外地址使用相同 TLS 选项。桶须事先存在；迁移后必须保留同一桶和有效配置。默认 Compose app 保持纯本地；可在新的独立实例向进程传入这些变量，不要将凭据写入源码或日志。

维护操作：当前用户 Cookie + `X-NetDisk-Request: 1` + `X-NetDisk-Maintenance` 调用 `POST /api/files/{id}/migrate-s3`。同时校验文件所有权；维护密钥不授予访问其他用户文件的权限。顺序为记录待清理对象、流式 PUT/校验本地 SHA-256、StatObject 校验大小/哈希元数据、回读远端完整字节核对长度和 SHA-256、事务切换 backend 并记录本地清理、最后删本地副本。失败保留本地可读路径；未切换的远端副本进入持久清理队列，可重试迁移或在清理周期回收。backend 已为 s3 时调用为幂等清理操作。

对象 GET 返回空响应体的 307，Location 是有效期 120 秒的签名 GET；服务器不获取对象正文。客户端直接访问该地址并自行发 Range。HEAD 是鉴权探测（200，无对象读取），用于现有页面下载预检。ZIP 生成会按需要流式读取对象。分享撤销/资源删除会立即拒绝新的应用请求；此前已签发的对象 URL 在其 120 秒期限内仍可能有效。

## 隔离集成

```powershell
.\scripts\minio-test.ps1
```

脚本使用现有 tools 服务构建官方 `github.com/minio/minio@RELEASE.2025-04-22T22-12-26Z`，二进制在 Git 忽略的 `.tmp/full-a/minio-bin`。官方镜像仓库拉取失败，官方二进制端点返回 410，所以本轮真实集成使用该源码构建，不以 mock 替代 MinIO。解析的模块版本为 `v0.0.0-20250422221226-0d7408fc9969`；MinIO 依赖不加入应用 go.mod。

`compose.minio-test.yaml` 是独立项目：服务器用 golang:1.27.0 运行此二进制，数据仅在 256 MiB tmpfs，仅绑定 127.0.0.1:38121；没有应用服务或真实数据卷。明示测试凭据 `netdisk-test-only` / `netdisk-test-only-password` 仅供一次性本地测试。每次测试创建随机桶并清理合成对象，脚本最后停止和移除本次 MinIO 容器。可手动分步运行上述脚本里的命令；不要把该文件与主 Compose 合并。

集成覆盖：迁移成功与上传/验证/事务失败、失败远端副本回收、本地保留与成功后删除、307 空正文、签名直链 SHA-256/Range、对象 ZIP、重启、账户删除保留另一用户引用、最后引用删除对象。测试不打印凭据、Cookie、分享 token 或签名 URL。
