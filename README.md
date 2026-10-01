# 冰岩实习 NetDisk

用于本地演示的 Go 网盘。S2B 已通过验收，S3 冻结业务代码，仅整理交付文档、审查并验证源码包。网页：http://127.0.0.1:38120/。

技术栈：Go 1.27.0、net/http、database/sql、modernc.org/sqlite v1.60.1、golang.org/x/crypto v0.57.0；依赖锁定在 go.mod/go.sum。原生 HTML/CSS/JavaScript 内嵌于 Go 二进制，无前端构建链、框架或 CDN；数据在 Linux 命名卷。

## 功能范围

- 已实现：注册、登录、退出并撤销会话、当前用户；文件流式上传、全部列表、受保护下载、改名、删除；文件夹创建、改名、移动；文件移动；当前目录、面包屑、根目录/上级导航。
- 部分实现：仅删除空文件夹，非空明确拒绝；多用户只涵盖基础账号与数据隔离，不代表完成题目整个多用户章节。
- 未实现：分享、去重、分片上传、对象存储、NFS、P2P、递归删除、密码重置、账号删除、总配额、登录限速、列表分页、多实例部署。

安全、测试、目录和部署要求属于项目实施方案，不宣称全部来自题目原文。

## 运行前提与完整命令

需要运行中的 Docker Linux 引擎、Docker Compose v2；Windows 使用 PowerShell 7，GNU/Linux 使用 POSIX sh。宿主机不需要 Go 或 Node。首次需要取得 golang:1.27.0 镜像；Compose 的 pull_policy 为 never，不会自动拉取。首次编译需网络访问 Go 模块代理与校验服务取得锁定依赖，或已有对应缓存。项目目录需可写以输出 bin/netdisk。

Windows PowerShell 7（当前项目）：

```powershell
Set-Location 'E:\冰岩实习\NetDisk'
docker image inspect golang:1.27.0 *> $null
if ($LASTEXITCODE -ne 0) {
    docker pull golang:1.27.0
    if ($LASTEXITCODE -ne 0) { throw 'Go image unavailable' }
}
.\scripts\start.ps1
Invoke-RestMethod 'http://127.0.0.1:38120/healthz'
```

GNU/Linux（假设源码包解压在当前目录，产生 NetDisk 子目录）：

```sh
cd NetDisk
docker image inspect golang:1.27.0 >/dev/null 2>&1 || docker pull golang:1.27.0
sh scripts/start.sh
curl --fail http://127.0.0.1:38120/healthz
```

打开上述本地地址。新启动前检查 38120 绑定，冲突时报错，不终止占用者。已运行时脚本不重新编译：PowerShell 检查健康，Linux 返回已运行后由 curl 检查健康。

同一 Docker 引擎只运行这一套 Compose：容器名 bingyan-netdisk-app，数据卷 bingyan-netdisk-data 均写死，改变 Compose 项目名或切换解压目录不能隔离第二套实例。不要在现有服务运行时从另一个源码目录启动同名服务。缓存卷为 bingyan-netdisk-gomod、bingyan-netdisk-gocache。SQLite 不在 Windows 绑定目录中运行；不要删除数据卷。

## 配置与限制

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| NETDISK_MAX_UPLOAD_BYTES | 104857600 | 单次 100 MiB；Compose 支持宿主环境覆盖 |
| NETDISK_ADDR | 127.0.0.1:38120 | 直接运行；Compose 内为 :8080 |
| NETDISK_DATA_DIR | ./data | Compose 固定 /data 命名卷 |
| NETDISK_ORIGIN | http://127.0.0.1:38120 | CSRF 检查；无末尾斜线 |
| NETDISK_COOKIE_SECURE | false | 本地 HTTP 可用；HTTPS 要求 true |

密码 bcrypt cost 12；会话为 32 字节随机令牌，数据库保存 SHA-256 哈希及 24 小时过期时间；Cookie 为 HttpOnly、SameSite=Lax。网页使用安全文本展示，不在 Web Storage 保存身份。SQLite WAL、单连接、单实例；仅暴露 127.0.0.1:38120，未公网部署。

进程强杀/断电在内容落盘和元数据提交之间可能留下不可访问的临时/孤立文件，尚无回收器；未完成删除由持久化标记在重启重试。移动目标尚未隐藏自身/后代选项，由服务端拒绝。移动端仅有历史登录布局检查，未声称移动端文件全流程通过。

## 3 分钟演示

1. 注册临时账号并登录，创建收件箱和归档两个目录。
2. 进入收件箱，上传自己的测试文件；点击移动并选择归档，再从根目录进入归档找到文件。
3. 点击下载，等待浏览器实际保存。用 Windows Get-FileHash -Algorithm SHA256 或 Linux sha256sum 比较源文件和下载文件。
4. 改名或移动文件夹；删除非空目录、自身/后代移动应明确拒绝。
5. 仅删除演示文件，再删除空目录并退出。没有递归删除。

自动化浏览器历史验收使用独立命名会话、明确项目 .tmp 下载目录和普通 click。agent-browser 0.33.1 的 download 辅助命令在独立空白页也失败；普通点击真实落盘已通过，具体 Windows 路径机制未证实。原 S2 PARTIAL 及失败原文归档 DEVLOG，原证据保留，没有改写历史。

## 测试与证据

项目根目录执行 Go 检查：

```sh
docker compose --profile tools run --rm --no-deps tools sh -c 'test -z "$(gofmt -l cmd internal)" && go test -count=1 -v ./... && go vet ./...'
```

原 S1 HTTP 验收入口（PowerShell 7）：

```powershell
Set-Location 'E:\冰岩实习\NetDisk'
.\scripts\verify.ps1
```

HTTP 脚本会生成测试账号/文件并实际重启本项目服务；成功后删除测试文件、退出会话，测试账号保留。不能接受短暂重启时不要运行。本轮 S3 未执行 HTTP 51 项脚本和浏览器业务流程，历史 PASS 不冒充当轮执行。

S2B 历史：10 组 Go 测试、vet、51 项 HTTP 回归、真实浏览器落盘哈希和目录重启持久化通过，见 evidence/s2b-checks.txt。下载独立补验见 evidence/s2-download-supplement-20261001.txt。S3 包内源码测试、vet、构建和隔离启动检查见 evidence/s3-delivery-checks.txt；使用已有镜像和项目 Go 缓存，不是空缓存、完全离线或另一台电脑实测。

## API

统一错误格式：`{"error":"..."}`。需要身份的接口使用 Cookie `netdisk_session`；下载链接仍需登录 Cookie。文件 ID 与磁盘存储键均由服务端随机生成。

所有写请求须带 `X-NetDisk-Request: 1`。若有 `Origin`，须等于 `NETDISK_ORIGIN`；拒绝 `Sec-Fetch-Site: cross-site`。不授予跨域 CORS 权限，自定义头使浏览器跨域写请求必须预检。命令行客户端可不发送 Origin，但仍须发送自定义头。

| 方法 | 路径 | 请求 | 结果 |
|---|---|---|---|
| GET | `/healthz` | 无 | 200，公开健康状态 |
| POST | `/api/register` | JSON `username,password` | 201，用户 ID 和名称 |
| POST | `/api/login` | JSON `username,password` | 200，用户信息；Set-Cookie 建立会话 |
| POST | `/api/logout` | 登录，无请求体 | 204，删除服务端会话并清除 Cookie |
| GET | `/api/me` | 登录 | 200，当前用户 |
| GET | `/api/config` | 登录 | 200，仅返回实际 `max_upload_bytes`，供网页显示 |
| POST | `/api/files?name=文件名` | 登录，原始二进制体，`application/octet-stream` | 201，文件元数据 |
| GET | `/api/files` | 登录 | 200，`{"files":[...]}` |
| GET | `/api/files/{id}` | 登录且属于本人 | 200，文件元数据 |
| GET | `/api/files/{id}/download` | 登录且属于本人 | 200，原始内容，附件下载 |
| PATCH | `/api/files/{id}` | 登录，JSON `{"name":"新名称"}` | 200，仅改变显示名称 |
| DELETE | `/api/files/{id}` | 登录 | 204，删除内容和有效记录 |

元数据包括 `id,name,size,created_at,download_url`，时间为 Unix 秒。用户名转小写后须为 3–32 位字母、数字或下划线；密码 10–72 字节。名称限 255 个 Unicode 字符，拒绝空名称、首尾空白、点目录、控制字符和两种路径分隔符。他人或未知文件返回 404，未登录 401，CSRF 拒绝 403，上传超限 413。

上传使用原始请求体，**不使用 multipart/form-data**。通过 32 KiB 缓冲流式写入临时文件，成功后才提交元数据；正常中断或超限清理临时文件。下载固定 `application/octet-stream`、`Content-Disposition: attachment` 和 `nosniff`，不将上传 HTML 作为网页执行。


### 文件夹 API、兼容性与同名策略

新增接口均需登录，写请求沿用 `X-NetDisk-Request: 1` 和 Origin 校验。根目录使用空 ID，是虚拟目录，不能改名或删除。

| 方法 | 路径 | 请求或语义 |
|---|---|---|
| GET | `/api/directory?folder_id=ID` | 当前目录的 folders/files/breadcrumbs；空 ID 为根目录 |
| GET | `/api/folders` | 当前用户全部逻辑文件夹，供移动目标选择 |
| GET | `/api/folders/{id}` | 本人文件夹元数据 |
| POST | `/api/folders` | JSON `name,parent_id`；空 parent_id 为根目录 |
| PATCH | `/api/folders/{id}` | JSON `name` |
| DELETE | `/api/folders/{id}` | 仅空目录；非空 409 |
| POST | `/api/folders/{id}/move` | JSON `parent_id`；自身/后代 409 |
| POST | `/api/files/{id}/move` | JSON `folder_id`；空值移到根目录 |
| POST | `/api/files?name=名称&folder_id=ID` | 仍为原始流式上传，仅新增可选目录参数 |

`GET /api/files` 仍返回本人**全部文件**，不会悄悄改成当前目录列表；上传省略 folder_id 仍在根目录。旧文件 ID、内容、链接和账号不变，旧文件迁移到逻辑根目录。移动/改名仅更新元数据，不搬运文件内容。

文件允许同名，每次仍有独立随机 ID 和存储键，不覆盖旧文件；同级文件夹名称按 SQLite 二进制规则精确比较，重名返回 409。文件与文件夹可以同名，网页以类型区分。没有为建立唯一约束清理或改名旧文件。事务和单连接将移动的所有权、目标、循环检查与写入串行化；数据库外键阻止悬空目录关联。


### 备份、恢复与限制

修改前源码检查点：`.tmp/checkpoints/s2b-before-20261001.zip`。数据库修改前停止本项目 app，确认没有运行容器挂载数据卷，再以只读挂载将整个卷备份为 `.tmp/s2b-backup/data-before.tar`，随后恢复旧服务。因此没有把活跃 SQLite 主文件单独拷走。备份 SHA-256 为 `79CC8D2B11AEBE66726D2CF0C24A72E37018CD9880C20F54942121462C56A9E2`，备份库和当前库 integrity_check 均为 ok。

恢复方式：先停止本项目，保留当前卷的另一份完整快照，确认备份之后没有需要保留的其他写入；将恢复源解压到私有检查目录核验，恢复数据库主文件及配套 WAL/SHM 的完整集合，不能与现场旧 sidecar 混用；卷内 blob 内容按同一快照匹配，同时恢复源码检查点并重建。原有现场文件应移入另一个私有恢复备份位置保留，不能直接清空卷或覆盖未知改动。本轮迁移已成功且无需恢复，未执行恢复或删除真实数据。

迁移在事务内建 folders 表、为 files 增加可空 folder_id、建立索引并记录版本 1；失败回滚，重复启动安全。备份中的 6 个旧账号字段逐项保持；备份当时没有有效旧会话和文件记录，非空旧文件/同名文件、旧会话及链接兼容通过专用旧版数据库夹具测试验证，没有把空库核对冒充真实旧文件哈希验收。


## 交付与 Git

源码包：dist/NetDisk-source-20261001.zip；校验：dist/NetDisk-source-20261001.sha256。唯一 NetDisk 根目录，仅包含审查白名单；无 .git、.tmp、运行数据库、上传内容、会话资料、私有备份、bin、dist 或编译缓存。

S3 保存原 S1 暂存树和索引检查点后按明确清单更新最终暂存版本。Git 真实署名/邮箱仍缺失，没有 commit、remote、push 或 GitHub 发布；下一步是用户提供真实身份，复核暂存内容后另行授权提交。远程仓库和 push 另需明确授权。

Codex 辅助实现、测试及整理；精确模型 ID 和底层 API 请求数 UNKNOWN。未使用 OpenCode/DeepSeek 模型任务或额外子 Agent。DEVLOG 保留各阶段真实过程与失败历史。本轮到交付收尾为止。