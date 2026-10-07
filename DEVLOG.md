# 开发记录

## 2026-10-03：目录返回按钮（未提交）

用户反馈进入文件夹后找不到返回入口。原“返回上级”采用普通文字样式，本轮改为目录标题下带左箭头、浅蓝背景、边框和至少 42px 高度的“返回上一级”按钮，移动端可见；现有路径中的祖先目录和根目录仍可点击。根目录禁用返回按钮，避免产生无意义请求。

同时修复导航状态提前提交：原 navigate 在目录请求成功前已修改 state.folder，失败后会留下旧列表/路径与新上传目标不一致。现在请求目录、目录树和统计均成功后才提交目录状态并清除上一目录的选择/批量结果；失败保留原目录、路径与选择。沿用 busy guard 防重复导航。

在真实 app.js 的 Node 行为测试中新增逐级返回、祖先/根路径与首页跳转、并发导航防重复、404 和后续目录树读取失败时状态保持两组；含现有上传/批量操作合计 18 项 PASS。已有 golang:1.27.0 tools 的 gofmt clean、go test -count=1 ./...、go vet ./... 和两个客户端构建 PASS。

全新 bingyan-netdisk-full-navigation-smoke 使用 /data 64MiB tmpfs、127.0.0.1:38129，仅注册合成账号和创建三级目录/合成文件。真实浏览器点击逐级返回“三级目录→二级目录→导航测试→根目录”、祖先路径跳转、根目录跳转和真实不存在目录 404 后选择/上传目标保持，均 PASS。390px 屏幕返回按钮可见、可点击且无页面溢出；JS errors 为空，截图仅合成数据并经目视检查。核对自己 demo 的精确容器 ID/独立数据挂载后，用 .tmp/full-navigation 新二进制更新 38125，保留 demo 数据与旧二进制；新合成账号再验证进入/返回，核对用户名后仅删除本轮检查账号。自己的 tmpfs 容器/测试浏览器已清理，旧 38120/live 服务、二进制、数据卷未请求或操作。没有提交、推送、暂存、reset/clean、后台 Codex、OpenCode、DeepSeek、子 Agent 或 goal。详见 evidence/folder-navigation-checks.txt。

## 2026-10-03：勾选与批量操作（未提交）

按用户百度网盘截图的操作方式增加文件/文件夹行勾选、当前目录全选/部分状态、已选数量、选中行高亮和批量工具栏，保留原上传与逐项操作。提供打包下载、逐项重命名、同目标移动、独立链接分享、确认删除；文件夹批量删除包括子树。操作前预览，期间禁用控件/重复提交，逐项成功/失败继续，结束刷新；失败项仍存在时保留选择，导航/退出清空私有状态。分享部分失败只重试失败项，前次成功链接保留在当前页内存供复制，导航/退出清除。窄屏可见勾选，顶部长用户名截断防溢出。

后端新增本人选中资源 ZIP 和显式递归目录删除，复用原 ZIP 写入、缓存、失效、Range、所有权/CSRF/参数化 SQL 与 dedupe GC。子树删除的文件引用、目录、上传会话及 durable 分片清理标记在事务内提交，其他用户共享 blob 保留；注入物理 GC 故障得到真实 503/tombstone，故障解除后清理恢复。ZIP 可覆盖任意本人选中条目，重复/父子重叠去重，包含非法/外人条目拒绝，最多 200 项，路径安全和同名避让沿用原实现。未新建数据库表。

Node 6 项勾选/批量行为测试加已有上传/自动续传 10 项最终全部 PASS；Go 3 项选中 ZIP/Range/隔离/失效、子树删除/引用/分片/分享、GC 故障恢复专项 PASS；原文件夹 ZIP/网页专项 PASS；gofmt clean、go test ./...（2.496s）、go vet ./... 和两个客户端构建 PASS。前端分享收尾后 Node 16 项复验/重新构建 PASS，Go 源码保持同一已验收版本。抽取 ZIP serve 函数时初次漏声明 err，定向编译捕获后修正；旧 DOM 测试夹具对所有选择器返回全元素，改成按 data 属性过滤后原上传测试通过；没有削弱业务断言。链接保留测试也捕获了补丁落到错误循环的位置，修正后才构建最终二进制。

真实 agent-browser 在新 tmpfs bingyan-netdisk-full-selection-smoke（38129、128MiB）注册合成账号、普通三文件上传，创建含子/空目录的测试目录。全选 5 项后取消、选 3 项显示高亮和部分状态；打包浏览器下载 1281 字节 ZIP，6 个合法条目，文件 SHA-256 校验；三分享匿名访问均 200。重命名一个前导空格名称产生真实 400，其余两项成功且只失败项留选，重复 submit 未新增请求。三项移动到目标目录，导航清除勾选；取消删除无变化；确认删除后目录空、三分享 404、其余未选文件保留，用量/预留正确。截图目视仅合成数据，JS errors 为空。自身 tmpfs 容器和浏览器已清理。

最终分享重试在隔离 closeout tmpfs 和新版 38125 各验证：仅第一次分享故意使用错误 CSRF header，服务器真实 403，第二项仍 201；重试只增加一次 POST，最终两条分享且两条链接仍可复制/匿名 200，没有重复成功项。无响应 mock。已按容器 ID/挂载核验，仅更新自己创建的 demo，保留 bingyan-netdisk-full-demo-data 和旧二进制；38125 更新后普通三文件、勾选/批量删除、390px 窄屏无页面横溢、分享部分失败/保留链接均 PASS。每个 demo 检查账号均核对本轮新建用户名后删除，其他账号/文件未操作。旧 38120/live 不请求、不重启、不换二进制、不读写卷；无 commit/push、索引修改、后台 Codex/OpenCode/DeepSeek/子 Agent。证据：evidence/selection-checks.txt、evidence/selection-smoke.png。

## 2026-10-03：普通上传自动分片（未提交）

根据用户最新要求移除分片/恢复面板、会话 ID 输入和取消按钮；普通多选入口统一决定传输方式。阈值严格为 >300,000,000 字节，每片 8MiB，每个文件依次发送各片，批次仍最多 3 个文件并发。默认单文件限制从 100MiB 调到 1GiB，继续执行现有账户配额、预留、所有权、CSRF 与 dedupe。小文件继续复用原 API；逐项状态、完成计数、防重复、部分失败继续和结束刷新保留。

新增后台重试与当前标签页自动恢复，校验服务端每片长度/摘要，阻止同名同大小但内容变化的错误续传。创建增加可选用户隔离幂等键，文件发布事务同时记录完成回执，响应丢失重试不会重复发布；取消、到期、账户/文件删除清理回执。浏览器拒绝 sessionStorage 时内存回退，成功/退出清除记录。新数据库表仅在临时测试夹具和独立 demo 中创建，旧 live 从未连接。

Node 行为测试 10 项、后端自动会话专项、既有分片/网页专项 PASS；最终 gofmt clean、go test ./...（2.672s）、go vet ./...、两客户端构建 PASS。新增存储兼容处理后重新执行最终检查。默认 Go 全量中的可选 MinIO/kernel NFS 集成本轮未启用，未把历史专项结果作为本轮重跑。

真实浏览器在新建 bingyan-netdisk-full-auto-smoke（38128、1.5GiB tmpfs）上传合成 300,000,001 字节文件。主动 abort 第 1 片产生 3 次网络失败，其他两个小文件正常成功；刷新后重新选择，自动复用会话、跳过已收到第 0 片，只补 35 片完成。随后普通入口三文件完整批次 PASS：大文件 36 片、小文件原 API、并发峰值 3、重复 submit 无额外请求、最终 3/3。真实 HTTP 下载流式 SHA-256 与源一致。浏览器内全文件摘要的 CLI 调用出现 EOF，因此下载最终以宿主流式真实请求校验，没有以该未返回的浏览器调用冒充 PASS。JS errors 为空；截图仅含合成文件。自己的 tmpfs 容器与测试浏览器已清理。

核对自己此前创建的 demo 容器 ID、挂载与来源后，仅替换 bingyan-netdisk-full-demo 的容器和已测试二进制，保留原独立 demo 数据卷、账号、文件及旧二进制供恢复；新版本仍在 http://127.0.0.1:38125/。更新后新注册合成账号再跑普通三文件 smoke，页面无手动面板、策略值正确、最终 3/3、JS errors 为空；核对用户名后仅删除这个本轮检查账号与合成文件并关闭测试浏览器。旧 38120/live 服务没有请求、重启、替换二进制或访问数据。详细证据见 evidence/automatic-upload-checks.txt。继续保留未提交改动、空索引；无 commit/push、后台 Codex、OpenCode、DeepSeek、子 Agent 或 goal。

## 2026-10-03：普通批量上传

基于完整版提交 `3250d42` 的干净工作树直接修改，仅当前可见 Codex 线程，无后台 Codex/OpenCode/DeepSeek/子 Agent，不提交或推送。本轮不访问 live 数据卷，所有浏览器业务数据只在新建 `bingyan-netdisk-full-batch-smoke` 的 64 MiB tmpfs，端口仅 `127.0.0.1:38127`。

普通上传 input 增加 multiple，快照所选文件与当前目录，逐个复用原 POST /api/files 原始请求体 API 和 CSRF header。队列最多三个在途请求，单文件异常独立记录，继续处理其余文件；先检查所有大小，超限直接标失败/已跳过，不发请求。逐项显示等待、上传中、成功、失败及错误，总进度包括成功、失败和跳过的终态。沿用同步 busy guard 与禁用控件防重复提交，结束清空选择并刷新目录，保留结果供查看；退出登录清除私人文件名和结果。共用选择框的分片入口明确只允许单个文件。

补充 Node 内置行为测试（真实执行 queue 与 app.js）：七文件并发上限、失败后继续、状态与计数、大小等于上限/空文件、超限零请求、全部跳过、空选择/未知限制、重复 submit、原 API/body/CSRF/目录编码、仅全部结束后刷新、退出清理；五个顶层测试 PASS。Go 静态资源/CSP 测试与单文件核心上传针对性 PASS；最终 gofmt clean、go test ./...、go vet ./... 和构建 PASS。命令为 `node --test scripts/batch-upload.test.cjs`，以及已有 golang:1.27.0 tools；新增队列脚本不引入运行时依赖。

真实 agent-browser smoke：合成账号创建并进入 batch_target，一次选三个文件（4096/6144/12288 bytes），三次真实 POST 均 201，并发峰值 3，同一时刻第二次 submit 没有新请求；最终三个成功、3/3、当前目录刷新，三次真实下载 SHA-256 与源文件一致。另测五文件混合批次：前导空格名返回真实 400，其余三个仍 201，超限没有 POST，观察到等待状态，最终 5/5，目录增加三个文件。最初用 90 个 Unicode 字符的名称作为失败夹具，但服务器按 rune 计数合法接受；仅更正夹具为明确不合法的前导空格，没有修改服务端规则。JS errors 为空，截图目视检查无 token/验证码/真实数据。详情见 evidence/batch-upload-checks.txt；本轮结束移除自己的 tmpfs 容器与浏览器，保留源码和证据为未提交改动。

批量上传运行版本跟进：用户确认截图地址为 `http://127.0.0.1:38120/`。仅查看容器挂载元数据与项目二进制时间，确认旧服务仍运行 `/workspace/bin/netdisk`（2026-10-01 20:36:50 构建），没有请求旧 HTTP 服务、访问 live 卷、覆盖其二进制或重启。再次确认 `bingyan-netdisk-full-demo` 与 `bingyan-netdisk-full-demo-data` 均不存在后，执行已有 full-demo.ps1 构建当前版本并启动新独立实例 `http://127.0.0.1:38125/`；卷全新，账户/文件与旧实例分开。页面补充 Ctrl/Shift 多选提示；Node 五项复验与新运行实例三文件真实浏览器 smoke PASS，input.multiple=true，3/3 成功。仅移除本轮合成检查账号/文件并关闭测试浏览器，保持新 demo 运行供用户使用；无提交/推送。详细记录追加在 batch-upload-checks.txt。

## 2026-10-03：Full Requirements 可见主线程

TASK=`BINGYAN-NETDISK-FULL-REQUIREMENTS-VISIBLE-APP-001`，分支 `feature/full-netdisk-task`，HEAD/main/origin main 均为 `f04850f`。接管未提交的 Full A 改动，先读取 git status/diff 与隔离资源挂载，再重新执行针对性测试；没有 reset、重写既有高级功能、暂存、提交、推送或修改 remote。当前线程直接完成，没有后台 Codex CLI、OpenCode、DeepSeek、子 Agent 或持续 goal；精确模型 ID/API 数 UNKNOWN。

宿主 Go 不可用，使用已有 `golang:1.27.0` 和 tools。默认执行沙箱初始化失败，改为逐条审核的限定项目命令；Git ownership 检查以单次 `-c safe.directory=E:/冰岩实习/NetDisk` 解决，没有改全局配置。后续只读 Git 使用 `--no-optional-locks`；暂存 diff 始终为空。中途 VCS/stat cache 查询期间 index 指纹曾不同，未运行任何 add/reset/暂存操作；最终 index SHA-256 与接管时完全一致：`a4f628276d6df0adfd0eca0b6e7a61a3f6473b75ad046711a84d637edf3a3059`。

初始残留四个 Full A test 容器：baseline、MinIO、UI/CSP、UI；仅查看 mount metadata，均没有 live 数据卷，保持其状态。`bingyan-netdisk-data` 从未挂载、读取、写入、迁移、重启或删除。本轮所有业务夹具使用 `t.TempDir`、tmpfs 或新独立演示卷，旧版本迁移只用合成数据库夹具。

### 主线 A

重新验证了核心注册/登录/Session、文件 CRUD、文件夹与跨用户隔离、CSRF、大小限制/中断清理、重启恢复；分享创建/列表/匿名/撤销/源删除失效/动态子树边界；资料/账户删除和其他用户 dedupe 引用存活；并发相同内容单副本/最终引用回收；真实 HTTP 206、Content-Range 与分段重组；分片幂等、状态、重启继续、取消、过期、坏分片、IDOR 和最终去重；嵌套 ZIP/空目录/名称冲突/Zip Slip/缓存失效/过期/Range。

MinIO 使用已有隔离 tmpfs server。重新执行真实 Put/Get/HEAD/删除与重启、失败恢复、object ZIP、匿名/object 307。改进迁移校验：除本地上传流 hash 和远端 HEAD metadata，还回读远端真实字节核对长度/SHA-256，再事务切换 backend 和写入 durable local cleanup；普通下载仅短时 presigned redirect，不代理对象正文。

### 主线 B

NFS 使用固定版本 `willscott/go-nfs v0.0.4`、billy 逻辑适配器。导出 capability 只保存 hash，每次路径/handle/打开文件操作重验导出和 user_id；拒绝越界、Windows 路径、symlink、目录循环和跨用户资源。文件写入 scratch，原文件 ID 保持，在事务中更新内容/ref_count/配额；逻辑 mode/mtime 实际存储，client UID/GID 不改变 app 身份。独立 Linux TCP NFSv3 RPC 测试和有 SYS_ADMIN 的新容器内 kernel mount 均实际验证读写/目录/重命名/删除。NFSv3 锁、ACL、传输加密和重复名称歧义访问不提供；这些边界写明，没有空实现冒充。

P2P 增加 `p2p` 分享类型和 owner-only offer API，匿名链接只交换私有地址、hash、长度、短期 TLS fingerprint。`cmd/netdisk-p2p` 两个客户端直接连接，TLS 1.3/fingerprint pinning/token 校验、长度与 hash 校验、无覆盖发布；发送端发送前重新核对 signaling，旧 offer 不能绕过撤销。针对性测试实际传输 1 MiB，应用 signaling response 257 bytes；坏 token、hash、fingerprint、过期、跨用户 offer、源删除与撤销均测试。

### The end? 扩展（与主线分开）

显式本地开发邮箱适配器（6 位随机码、hash-only、10 分钟、单次使用、5 次错误、30 秒重发、邮箱唯一）；校验旧密码后 bcrypt 修改，并事务撤销全部 Session/NFS 导出/验证码。配额默认 1 GiB，计逻辑 bytes 和上传预留，普通/分片/NFS 统一执行，统计含本人内容去重大小。分享事件只记录 kind/time，无 IP/token/预签名链接，30 天清理；分析是请求数，未冒称完成下载字节或独立访客。存储策略选择 >=1 MiB、低访问、7 天冷 local blob，由维护密钥+所有者+CSRF 明确执行真实 MinIO 迁移，不声称云自动运维。

### 浏览器与最终验收

浏览器技能 `agent-browser` 用独立 localhost 会话。实际注册/登录、目录创建、普通上传、匿名分享/统计、普通下载/ZIP、分片上传、显示名称、邮箱绑定、NFS 导出创建/撤销；源/匿名/浏览器下载 SHA-256 `43c3af516f3489b61190ae5d4ef6fe04b3d2f0db3a137faac3a34fee00644f23`（212992 bytes）。辅助 download 命令取消；首次 launch 指定项目下载目录后普通 click 成功，没有更改附件 CSP。设置长弹窗需内部滚动才能点原底部关闭，改为顶部 sticky 关闭，并在弹窗内显示操作结果。

可复现独立演示启动脚本为 `scripts/full-demo.ps1/.sh`，新卷 `bingyan-netdisk-full-demo-data`，网页 38125、NFS 38126 均只发布 loopback；旧启动/验收脚本未用于本轮。接口/协议细节见 `docs/full-protocols.md`。最终统一回归、独立 CLI 和演示脚本结果追加在 `evidence/full-requirements-checks.txt`，不把未执行项写 PASS。

最终统一验收：gofmt clean、`go test ./...`（同时设置真实 MinIO 与 NFS kernel mount 两个 opt-in，无相应 skip）、`go vet ./...`、两命令构建全部 PASS。Windows 独立启动脚本实际运行、两个独立 P2P CLI 进程直接 TLS 传输 212992 bytes 且 cmp/hash 一致；Linux 启动脚本只做 `sh -n`，完整脚本运行 NOT_RUN（协议、Linux app 和 kernel mount 已真实验证）。最终编译版浏览器再次验证全部必需入口，并完成密码修改/重新登录/合成账号删除，普通下载和 ZIP entry hash 一致；JS errors 为空。截图已目视审查，不含验证码、token、cookie 或真实数据。

已关闭本轮浏览器，移除本轮 tmpfs browser 与 demo 容器及本轮全新合成 demo 卷；仅保留接管前四个 Full A 隔离容器。Go 临时验收容器均 `--rm`；无 live 操作。源码和证据留工作区，HEAD/main/origin main 仍 f04850f，暂存为空且 index 最终字节指纹与接管时相同。全部当轮功能/要求验收完成后停止，下一步仅审查工作区后由用户明确授权提交。

### 最新规则更新后的续接复验

再次读取最新 AGENTS.md、git status/diff、暂存 diff、index 指纹以及四个原有隔离容器的 mount metadata。最新规则明确授权全部阶段，当前工作区已包含完整 A/B/扩展实现。本次续接没有修改业务源码，没有发现需要修复的遗留失败，原暂存区和四个容器保持原状。

按顺序重新运行 Full A/核心的 19 个顶层专项测试（含真实隔离 MinIO）以及 B/扩展的 7 个顶层专项测试，均 PASS；NFS 使用真实 TCP RPC，P2P 直接传输 1 MiB 且 hash 一致。随后只读 gofmt 检查与 go vet ./... 再次 PASS。完整全量回归、Linux kernel mount、两个独立 CLI 进程和浏览器验收仍对应此前的同一业务代码；本次未重复这些已完成的检查，未把它们写成新执行结果。新输出追加到同一 evidence 文件。三个新工具容器均自动清理，没有创建新数据卷或触碰 live 数据。

### 完整版提交授权与审查

用户明确要求“提交完整版”，授权本地 Git 提交。核查分支仍为 `feature/full-netdisk-task`，原暂存为空，使用仓库已配置的真实 Git 身份，不修改任何身份或 remote 配置。提交白名单为 37 个完整实现、测试、脚本、文档与证据文件；凭据模式检查无匹配，浏览器证据再次目视审查，无真实用户数据或 capability。更新对象存储文档，使其准确描述远端完整 hash 回读验证和显式冷内容策略。业务源码未变，沿用已通过的全部验收结果；仅暂存明确白名单并创建本地提交，不 push、不部署、不操作 live 数据。授权前索引快照保留在忽略的 `.tmp/full-requirements`，不进入提交。

## 2026-10-01（Asia/Shanghai）

AI 辅助：Codex 完成任务协调和只读入口核查；未启动 OpenCode 模型任务，未使用额外子 Agent。精确协调模型 ID 无法可靠核实，记 UNKNOWN；底层 API 请求数 UNKNOWN。

实际证据：

- `Get-ChildItem -Force -LiteralPath 'E:\冰岩实习\NetDisk'`：起初无项目文件。
- Codex 聊天列表：当前 NetDisk 聊天 active，返回列表中无其他同目录活动聊天。Win32_Process 检查因拒绝访问未完成。
- `%APPDATA%\npm\opencode.cmd --help`：沙箱内 EEXIST；获准的沙箱外诊断退出码 0。配置目录元数据确认为目录，未修改。
- `opencode models`：退出码 0；包含实际输出的 deepseek/deepseek-v4-pro 等 ID，仅证明列表存在，未验证推理调用成功。
- `opencode providers list`：退出码 0；显示 DeepSeek api 与 OpenCode Zen api 两项凭据配置；未打印凭据值。
- `agentdock.exe --help`：显示服务、tunnel、nexus 等命令；未提供已验证的本任务 AI 调度契约。未启动或变更服务。
- 已知 OpenCode 配置提取的 model/instructions/provider/mcp 项均为空；已知 Codex 配置的节名和规则路径引用中未确认执行中枢。未读取其他项目、未广泛扫描目录。
- Git 配置查询：user.name 与 user.email 均未设置。

结论：BLOCKED。缺少既有执行中枢准确入口及必要调用规则，不能直接调用 OpenCode 绕过用户指定路由。只保存用户要求的最小项目规则、任务记录和阻塞说明，不接管业务施工。

所有业务测试、go test、go vet、gofmt、Linux 启动和重启验收均为 NOT_RUN。没有宣称实现完成或测试通过。

### 用户更新授权后：Codex 直接实现 S1

用户明确“不使用 opencode，只使用 codex”，更新规则后不再依赖中枢。上面的 BLOCKED 和 NOT_RUN 是调度前历史状态，当前结果以本节及 TASKS.md 为准。没有启动 OpenCode 模型任务、DeepSeek 或额外子 Agent。

- 初始化本项目 Git；沙箱身份曾触发 Git 所有权检查，仅对当前命令使用 safe.directory 配置，不修改全局 Git 配置。身份仍缺失，未提交或推送。
- 核实 Docker 29.8.1；已有 golang:1.27.0 镜像为 linux/amd64，容器内 go version 为 go1.27.0。临时绑定检查确认 127.0.0.1:38120 可用。
- 在独立容器中查询实际依赖版本，固定 modernc.org/sqlite v1.60.1 和 golang.org/x/crypto v0.57.0，go mod tidy 生成完整锁定文件。
- 实现 net/http 后端、SQLite 用户/会话/文件表、bcrypt、会话哈希与过期、CSRF 验证、所有权过滤、固定缓冲上传、原始下载、改名和带恢复标记的删除。
- 实现 6 组必要测试，覆盖账号、会话实际失效与过期、密码/会话哈希、用户隔离、路径与 CSRF、下载安全、流式过程、超限和中断清理、重启与未完成删除恢复。
- 16:18 左右启动的检查因聊天中断丢失输出；恢复时没有遗留运行中的工具容器。未假定其通过，16:39 重新收取完整格式检查、go test、go vet、构建结果，退出码 0。
- 16:42 使用项目脚本启动实际 Linux 服务；真实 HTTP 验收 51 项全部通过，包含约 1 MiB 随机数据哈希、双用户隔离、退出后重放旧 Cookie、TCP 上传中断和本项目容器实际重启。
- SHA-256：628b49c532e5d2e6dcbec617746275ae0dfc0a1757ce58e32d95f6b8e546b8f3。上传、下载、改名后和重启后内容相同。
- 实际检查端口只发布 127.0.0.1:38120；/data 为 bingyan-netdisk-data Linux 命名卷，/workspace/bin 为本项目只读绑定。测试后临时文件 0、blob 文件 0；运行日志仅有监听事件，未输出密码或会话。
- 当前结果 PASS；Git commit 唯一等待项为真实 user.name/user.email。仅使用同一 Codex 任务续接，精确模型 ID 和底层 API 请求数均 UNKNOWN。

已记录的限制：无前端或 S2；原始请求体上传；无用户总配额或登录限速；强杀/断电可能留下不可访问的孤立文件，未实现回收。本轮完成即停止。

交付收尾：启动脚本增加已运行服务检查，重复执行不会因本项目占用端口报错；PowerShell 重复启动路径实测通过，Linux shell 脚本语法检查通过。添加仓库内 .gitattributes 固定 LF，避免 Windows 换行破坏 Linux 脚本。验收文本仅移除行末空白；源码、测试、文档及证据已暂存，二进制、数据库及临时内容仍被忽略。未修改全局配置。

### S2 · 最小网页与条件文件夹（19:49 开始）

- 用户以附件授权 BINGYAN-NETDISK-S2-DEMO-AND-FOLDERS-001：A 网页优先，A 完整验证后且剩余至少 45 分钟才允许 B。仅 Codex 当前主任务，未调用 OpenCode、DeepSeek 或子 Agent。
- 读取指定文档、S1 证据及相关源码，未扫描上级或其他项目。初始服务退出码 255、非 OOM，无法据此确认退出原因；通过原启动脚本恢复健康，没有提前重复整套验收。
- 修改前将既有暂存树 2488cd5094d1ff819c8e470bf3ba5c325f6b6a11 存入 .tmp/checkpoints/s1-20261001-1949.zip；不含数据卷、上传内容或运行凭据。原 18 个暂存项保留。
- 实现内嵌同源 HTML/CSS/JS 页面，新增受鉴权保护的上传上限配置接口。仅页面静态资源使用同源脚本样式 CSP，原 API 和附件安全策略最终不变；没有框架、CDN、Node 构建依赖。
- 19:56 相关测试发现 Go 路径规范化返回 307，而测试预期 301；一轮修正为验证重定向目标及最终源码路径 404，复测通过。
- 19:58 起使用已有 agent-browser 独立会话实际注册本轮测试账号、登录、上传、改名。HTML 样式文件名按文本显示，DOM 中 img 数为 0。未打印密码或保存 Cookie，Web Storage 检查为空。
- 浏览器工具对 HTML 样式名称和普通名称下载都返回 Download was canceled。限定尝试在附件 CSP 中允许 allow-downloads，一轮修正后仍取消，未取得落盘文件；根因未证明。按用户限制停止该修复尝试，恢复 S1 下载 CSP，没有继续试错或开始 B。
- 随后完成删除取消/确认、空列表、退出及另一标签页刷新触发登录过期的浏览器验证。仅删除本轮上传的测试文件；没有删除真实数据。保存真实截图并查看桌面和手机登录布局。
- 20:03 最终统一运行 gofmt 检查、7 组 Go 测试、go vet、构建，成功退出；部署回退后的最终版本后，原 S1 HTTP 验收 51 项全部通过，包括真实重启、双用户隔离和下载哈希。哈希为 7bf67b820b6dcfbd65f0f0af4ba0d20c5568abbc8e295cb21d30c16555ce2e15。
- 结果 PARTIAL：A 浏览器下载未通过；B NOT_STARTED。未做数据库迁移，原数据卷继续使用。更新现有文档并保存可恢复的部分成果检查点，不清空暂存区，不提交、不推送。本轮停止。

### 下载独立补验与 S2B（20:21:39 开始，35 分钟）

- 接收 ChatGPT 经 AgentDock 于 20:14:37 完成的补验证据。agent-browser 0.33.1 的 download 辅助命令在无 NetDisk 的空白页也取消；明确下载目录和普通 click 成功。重新读取两个 1 MiB 文件并重算 SHA-256，均为 8251010387eac967bb534ee30b87b204ef282d70dba78f7bd97e2ec0183c4b12。不改原失败历史，不将具体路径前缀当作已证明根因，不再修下载。
- 20:23 保存源码检查点 .tmp/checkpoints/s2b-before-20261001.zip；停止本项目，确认无运行容器使用数据卷后只读 tar 备份整个卷，保存 .tmp/s2b-backup/data-before.tar，再启动原服务。备份和运行数据不加入 Git。恢复流程写入 README，本轮未执行恢复。
- 新增 folders 表和 files.folder_id 的最小事务迁移，重复启动安全。所有移动操作在同一事务中检查拥有者、目标与祖先链，单连接串行化并发修改；删除只支持空目录。文件同名独立保留，同级目录重名拒绝，未重命名或清理旧文件。
- 网页沿用现有样式和下载函数，增加新建、目录进入、面包屑、返回上级/根目录、目录重命名、移动目标选择和删除空目录。原全部文件 API 不改语义；使用新目录 API 展示当前目录。
- 20:35 新增的目录安全、并发循环及旧库兼容测试通过；20:36 统一执行格式检查、10 组 go test、go vet 和构建，退出 0，随后部署迁移。
- 首个带额外 allowed-domains 拦截选项的浏览器会话，1 MiB 上传等待且没有产生服务端临时文件，服务健康。限定一轮工具会话复测，使用新的独立会话、明确下载目录、去掉额外拦截后成功；无业务代码修补，不断言低层原因。
- 真实网页完成注册登录、新建收件箱和归档、目录内上传、移动文件夹、非空删除拒绝、文件移动、目录改名。运行原 S1 回归 51 项断言全部通过，其真实重启期间保留本轮测试目录和文件。
- 20:45 重启后刷新确认归档/待整理结构及文件关联仍在；普通点击原下载按钮，浏览器落盘 1048576 字节，源和下载 SHA-256 均为 26ce77f878913905475c2b6fc613ed576191a150aa1a4b23324b0125ea823b81。确认哈希后才清理本轮文件、空目录，退出并关闭浏览器。没有调用 download 辅助命令。
- 20:47 只读核验备份与运行库完整性均 ok、外键无错误、版本 1；6 个旧账号字段保持。备份有 0 个有效会话和 0 个文件记录，非空旧文件和会话兼容由人工构造的旧版测试数据库实测，不把空库检查夸大成真实旧文件哈希证据。
- 当前结果 PASS。仅 Codex 主任务，未使用 OpenCode、DeepSeek 或子 Agent；实际模型 ID 和底层 API 请求数 UNKNOWN。没有修改全局配置或其他项目，保留既有暂存树，不提交、不推送，等待真实 Git 身份。停止于文件夹，不继续分享。

## S3 整理归档：S2 README 历史原文（非当前状态）

以下保留 S2 当时的 README 历史段落，包含当时 PARTIAL、下载失败、B 未启动及旧暂存状态；其后补验与 S2B 通过见上文。这里的本轮、当前、下一步均指旧阶段，不覆盖 S3 当前交付说明。
## 以下保留此前 S2 的历史状态与原接口文档

可运行的 Go 后端和同源原生 HTML/CSS/JavaScript 网页：注册、登录、退出，以及本人文件的上传、列表、改名和删除已通过真实浏览器交互。后端下载哈希和 S1 回归通过，但自动化浏览器下载返回 `Download was canceled`，未取得保存文件证据，原因未定位。因此本轮 S2 结果为 **PARTIAL，A 未完成全部验收，B 文件夹未启动**。

网页地址：<http://127.0.0.1:38120/>。页面有上传等待、成功/错误、空列表和会话过期提示；删除前确认；不显示虚假百分比，不将用户内容作为 HTML 插入，不使用 localStorage/sessionStorage 保存身份。没有外部 CDN 或新增前端构建链。

已实现：同源页面、账号操作、文件列表/上传/改名/确认删除；受保护下载入口已实现但浏览器落盘验证未通过。未实现：文件夹、目录导航、移动、分享、去重、分片、对象存储、NFS、P2P。基础多用户登录和隔离不代表题目整个多用户章节或整份任务书完成。

技术：Go 1.27.0、net/http、database/sql、modernc.org/sqlite v1.60.1、golang.org/x/crypto v0.57.0。完整依赖锁定在 go.mod/go.sum。项目实施中的安全、测试和部署约束不等同于题目原文。

## 启动

需要 Docker 正常运行、Docker Compose，以及已有的 `golang:1.27.0` Linux 镜像。首次编译需联网获取锁定依赖，不需要宿主机安装 Go。

Windows PowerShell 7：

```powershell
Set-Location 'E:\冰岩实习\NetDisk'
.\scripts\start.ps1
Invoke-RestMethod 'http://127.0.0.1:38120/healthz'
```

GNU/Linux，在本仓库根目录：

```sh
sh scripts/start.sh
curl --fail http://127.0.0.1:38120/healthz
```

脚本先检查本项目是否已运行；首次启动前检查端口，冲突则报错，不停止占用者。Go 容器构建出的 Linux 二进制在被 Git 忽略的 `bin/netdisk`。运行容器 `bingyan-netdisk-app` 只发布 `127.0.0.1:38120`；SQLite、临时上传和文件均位于 Linux 命名卷 `bingyan-netdisk-data`，没有将运行中的 SQLite 放入 Windows 绑定目录。

```powershell
# 只重启本项目，保留数据
docker compose restart app
# 修改源码后，停止本项目再重新构建和启动
docker compose stop app
.\scripts\start.ps1
```

不要删除数据卷。依赖缓存为 `bingyan-netdisk-gomod`、`bingyan-netdisk-gocache`；本机缓存卷最初由依赖核查容器创建，Compose 可能提示非 Compose 创建，功能不受影响。

## 最小验收

```powershell
Set-Location 'E:\冰岩实习\NetDisk'
.\scripts\start.ps1
docker compose --profile tools run --rm --no-deps tools sh -c 'test -z "$(gofmt -l cmd internal)" && go test -count=1 -v ./... && go vet ./...'
if ($LASTEXITCODE -ne 0) { throw 'Go checks failed' }
.\scripts\verify.ps1
```

Go 检查命令也可用于 GNU/Linux。`verify.ps1` 需要 PowerShell 7，从宿主机访问真实服务并仅重启本项目容器。生成随机临时账号和约 1 MiB 二进制内容，密码和会话仅留在内存，验证隔离、匿名拒绝、CSRF、路径、下载 SHA-256、改名、上传中断、重启、删除及退出后 Cookie 重放。成功后删除本次文件并退出两个测试会话；测试账号保留，本轮无账号删除接口。

本轮实际结果：7 个 Go 测试组、go vet、原 S1 的 51 项 HTTP 断言均通过。新证据为 `evidence/s2-go-checks.txt`、`evidence/s2-http-regression.txt` 和 `evidence/s2-browser-checks.txt`；S1 旧证据原样保留。流式测试生成 3 MiB 数据，最大读取缓冲 32768 字节，在 EOF 前确认临时文件已有数据且数据库无半成品记录；另测已知和未知长度超限、读取中断后的清理。

## 网页 3 分钟演示与待人工验证项

1. 打开网页，点击“注册账号”，使用临时用户名和 10–72 字节密码注册；切到登录并输入新密码登录。
2. 选择一个自己的测试文件，点击“开始上传”，观察真实等待状态和文件列表中的名称、大小、上传时间。
3. 点击“下载”，检查浏览器下载列表及本地落盘文件，再比较源文件与下载文件 SHA-256。**这一步自动化浏览器未通过，需人工核实，不可宣称演示成功。** 可在 PowerShell 使用 `Get-FileHash` 比较两个测试文件。
4. 点击“重命名”，修改名称；点击“删除”，先取消以确认文件保留，再点击“确认删除”。仅操作自己的临时文件。
5. 打开第二个同源标签页，在其中退出登录，回到第一个标签点击“刷新列表”，应提示登录已过期。

浏览器交互已实测上述步骤，唯独下载未获得落盘文件。保留了桌面登录、文件列表、删除确认、空列表、会话过期和手机宽度截图。上传等待状态的 DOM 及禁用逻辑已实现，但没有额外抓取慢速上传截图。下载的单轮 CSP 修正未解决问题，已恢复 S1 策略，未继续试错。

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

## 最小演示（PowerShell 7）

只生成测试账号和本地 `.tmp` 文件，不打印或保存密码、Cookie：

```powershell
Set-Location 'E:\冰岩实习\NetDisk'
$base = 'http://127.0.0.1:38120'
$headers = @{'X-NetDisk-Request'='1'}
$credentials = @{username=('demo_'+[guid]::NewGuid().ToString('N').Substring(0,12)); password=([guid]::NewGuid().ToString('N')+'!9')} | ConvertTo-Json
Invoke-RestMethod "$base/api/register" -Method Post -Headers $headers -ContentType 'application/json' -Body $credentials
Invoke-RestMethod "$base/api/login" -Method Post -Headers $headers -ContentType 'application/json' -Body $credentials -SessionVariable session
Invoke-RestMethod "$base/api/me" -WebSession $session
New-Item -ItemType Directory -Force .tmp | Out-Null
[IO.File]::WriteAllText((Join-Path $PWD '.tmp/demo.txt'), 'NetDisk streaming demo')
$file = Invoke-RestMethod "$base/api/files?name=demo.txt" -Method Post -Headers $headers -WebSession $session -ContentType 'application/octet-stream' -InFile '.tmp/demo.txt'
Invoke-RestMethod "$base/api/files" -WebSession $session
Invoke-WebRequest ($base+$file.download_url) -WebSession $session -OutFile '.tmp/download.bin'
Get-FileHash '.tmp/demo.txt', '.tmp/download.bin' -Algorithm SHA256
Invoke-RestMethod "$base/api/files/$($file.id)" -Method Patch -Headers $headers -WebSession $session -ContentType 'application/json' -Body '{"name":"renamed.txt"}'
Invoke-RestMethod "$base/api/files/$($file.id)" -Method Delete -Headers $headers -WebSession $session
Invoke-RestMethod "$base/api/logout" -Method Post -Headers $headers -WebSession $session
```

## 配置与限制

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `NETDISK_MAX_UPLOAD_BYTES` | `104857600` | 每次最多 100 MiB；Compose 支持宿主环境覆盖 |
| `NETDISK_ADDR` | `127.0.0.1:38120` | 直接运行地址；Compose 容器内为 `:8080` |
| `NETDISK_DATA_DIR` | `./data` | Compose 固定为 `/data` 命名卷 |
| `NETDISK_ORIGIN` | `http://127.0.0.1:38120` | 无末尾斜线；用于 CSRF 校验 |
| `NETDISK_COOKIE_SECURE` | `false` | 本地 HTTP 可用；仅允许 loopback HTTP，HTTPS 必须为 true |

密码使用 bcrypt cost 12；会话为 32 字节安全随机令牌，数据库仅存 SHA-256 哈希及 24 小时过期时间。Cookie 设置 HttpOnly、SameSite=Lax；JSON 和日志不返回密码或会话凭据。退出撤销服务端会话。

本轮没有文件夹、分享、去重、分片、对象存储、NFS、P2P、密码重置、用户总配额或登录限速。不用于公网部署。SQLite 采用单连接和 WAL；列表尚未分页；仅运行一个实例访问数据卷。网页下载取消问题尚未定位；移动端仅检查了登录页面布局。

正常上传失败会清理；进程强杀或断电发生于文件落盘与元数据提交之间时，可能留下不可下载的临时/孤立文件，本轮未实现回收。删除使用持久化标记，重启重试未完成删除。

Git 已初始化；姓名、邮箱缺失，所以尚未 commit/push。用户设置真实仓库身份后可创建首个规范提交。开发过程及 AI 辅助情况见 DEVLOG.md。

S1 的 18 个暂存文件保持原样；S2 修改留在工作区，新增文件未暂存。未创建远程仓库或公开发布。GitHub 待办为：用户提供真实 Git 身份、审查改动并提交、用户明确授权创建远程仓库及 push。精确模型 ID 无法可靠获取，记 UNKNOWN；实际由 Codex 辅助实现，未伪称个人手写。

修改前的 S1 源码检查点：`.tmp/checkpoints/s1-20261001-1949.zip`；本輪可恢复的网页部分成果：`.tmp/checkpoints/s2-a-partial-20261001.zip`。均由 Git 忽略且不含运行数据库或上传文件。恢复源码时应先将 ZIP 解压到新的临时目录审查，再选择性恢复，不覆盖未知修改。B 未启动，无数据库迁移或数据库恢复步骤，原命名卷保持使用。


## S3 交付收尾（2026-10-01 21:05:31 UTC+8 开始）

仅当前 Codex 主任务 1 个，模型精确 ID/API 请求数 UNKNOWN；OpenCode、DeepSeek、子 Agent 均为 0。业务冻结，不提交、不推送。

- 保存原 S1 树 2488cd5094d1ff819c8e470bf3ba5c325f6b6a11 的 ZIP 和 .git/index 副本到私有 .tmp/s3-20261001-2105。记录全部 Go/网页/脚本/配置/锁定文件哈希及现有容器 ID、启动时间和重启次数。
- 将 README 整理为当前状态、运行前提、完整启动命令、API、功能边界和 3 分钟演示。S2 历史 README 原文完整归档上节，失败和补验均保留；仅将历史命令中的个人 Windows profile 前缀替换为 APPDATA 占位，不改变事件。
- 逐项建立 38 文件白名单，审查凭据模式与账号/会话来源，测试密码和旧库会话均为测试夹具；7 张截图可见内容仅为合成账号、测试文件与空登录框，未发现真实敏感内容。没有把 .gitignore 当成审查替代品。
- 初次包内 Go 测试已编译，但默认 noexec tmpfs 阻止执行，记为验证环境失败；明确临时 /tmp、/verify 允许执行后复测通过，没有改业务文件或安全检查。
- 21:18 包内源码 10 组测试、格式、vet、构建通过。用 /verify/netdisk 新产物启动，数据仅在该容器全新 tmpfs，健康响应 ok，首页、JS、CSS 与解压源码逐字节一致。启动轮询首个请求在监听就绪前拒绝连接，随后成功；没有当作业务错误或隐藏失败。
- 容器使用 --rm 自动清理，未发布主机端口，未挂载 bingyan-netdisk-data，未运行第二套 Compose；原网盘保持运行。复用既有镜像和项目 Go 缓存，不是空缓存/异机/完全离线实测。
- 按审查白名单准备最终暂存与源码包，Git 规范化文本换行为 LF。原工作树业务文件原始字节保持，包内版本逐项与 Git 暂存对象核对；最终证据为 evidence/s3-delivery-checks.txt。不在证据内部记录其自身或 ZIP 的循环哈希，ZIP 哈希单独保存 .sha256。
- 本轮未跑会重启主服务的 S1 HTTP 验收，也未重跑浏览器业务；历史结果不当成本轮执行。Git 身份缺失，未 commit/push，未创建远程仓库。任务到此停止。


## Full A 实现：2026-10-03（BINGYAN-NETDISK-FULL-A-CORE-ADVANCED-001）

本轮用户授权覆盖已完成 S3 的源码冻结/旧时间窗。基线 f04850f，分支 feature/full-netdisk-task。Codex 当前主任务 1，精确模型 ID/API 请求数 UNKNOWN；OpenCode、DeepSeek、子 Agent 和持续 goal 均为 0。不提交、不推送、不改远程；原索引检查点在忽略的 `.tmp/full-a/index.before`，索引未修改。既有 live app 不重启、不部署本轮产物，`bingyan-netdisk-data` 不挂载、不读写、不迁移。

### 存储与兼容性

schema v2 增加 blobs / shares / upload_sessions / upload_parts / zip_cache 和持久本地、临时、远端清理队列，users 增加 display_name，files 增加 blob_id。保留文件、账号、会话 ID 和现有 API，保留 files.storage_key 的旧唯一元数据字段；实际下载通过 blob_id 读取 blobs.storage_key。每项迁移事务化、按列/表存在性幂等执行。旧文件逐个流式 SHA-256，大小一致后引用统一 blob，事务成功后再删除重复物理副本。缺失/大小异常的可见旧内容拒绝迁移并回滚高级 schema；可安全回收已删除且无物理内容的旧 tombstone。仅临时旧库夹具参与验证。

普通上传与分片完成共用 publish：32 KiB 缓冲、增量 SHA-256、随机服务端存储键、哈希唯一约束、原子文件引用/ref_count 写入。进程内存储锁和 SQLite 单连接事务串行化文件、目录及物理清理；只支持单个实例操作同一数据目录，长上传/下载会占用该锁。删除元数据与减少引用在同一事务，ref_count=0 作为持久回收状态；重启重试。pending blob 不作为新的上传引用，防止未完成清理的内容被误复用。账户删除复用 releaseFile，显式删除会话、分享、分片及目录引用，其他用户的 blob 不受影响。

每分钟及启动清理到期会话（24 小时）、ZIP（15 分钟）、持久清理队列；私有 tmp/blobs 中超过 24 小时、具有服务端生成名称且无有效引用的孤立文件才会回收。临时文件 0700 目录 / 0600 文件，成功、取消和失败路径及时清理。对象故障后的 GC 错误保留队列以便重试；已为 s3 的数据需要同一对象存储配置。

### 分享、安全和下载

分享 token 为 crypto/rand 的 32 字节随机值，数据库只存 SHA-256。创建返回一次性链接，列表只列分享 ID/资源 ID，撤销/源删除即时失效。文件夹按所属用户和递归子树过滤目录和文件，不返回上级 breadcrumbs 或私有 download_url；文件/目录移动后重新校验当前子树。写操作沿用 Cookie + X-NetDisk-Request / Origin / Fetch Metadata 保护。参数化 SQL，无客户端文件系统路径；资源 ID 不授予跨用户权限。增加 no-referrer，应用不记录全 token。

本地和匿名文件继续用 ServeContent，标准 Range / 206 / Content-Range / 416 不重新实现。对象文件返回空正文 307 和 120 秒 presigned GET，应用没有对象 GET/正文读取；对象 HEAD 仅作 200 鉴权预检。已经签发的对象 URL 在过期前可能仍有效，撤销拒绝新的应用请求。迁移顺序与独立真实 MinIO 测试详见 docs/object-storage.md。

ZIP 从逻辑子树元数据构建，保留嵌套目录和空目录，逐个 io.CopyBuffer（32 KiB）写入私有临时 ZIP，不 ReadAll 内容。entry 拒绝绝对路径、反斜线、冒号、点目录和控制字符。目录先保留原名；重名文件按稳定 ID 顺序保留第一个原名，其余加 [id-counter] 后缀并避让已有项；文件与目录同名时文件加后缀。entry 时间来自逻辑资源创建时间。缓存 fingerprint 包含 owner/root/文件与目录元数据和内容哈希；文件/目录写操作通过事务内触发器使该用户的缓存失效，并记录物理删除。缓存 ZIP 用 ServeContent 支持真实 Range 重组；到期/重启/账户删除均清理。

### 新 API（详细使用约定）

认证写操作统一需 `X-NetDisk-Request: 1`；JSON 需 application/json，正文文件或分片需 application/octet-stream。Cookie 为现有 netdisk_session。现有注册/登录/登出、files、folders、directory 和 move 接口继续有效。

| 方法/路径 | 输入与结果 |
|---|---|
| PATCH /api/me | JSON {display_name}，最长 80 个 Unicode 字符，不修改登录 username；200 User |
| DELETE /api/me | 当前身份，204；删除当前账号全部私有资源，页面二次确认；不能传入他人用户 ID |
| POST /api/shares | JSON {resource_type: file或folder, resource_id}；201 {share,url}，只在此时能复制 token 链接 |
| GET /api/shares | 200 {shares:[{id,file_id或folder_id,created_at}]}，不返回 token |
| DELETE /api/shares/{id} | 当前分享所有者，204；他人/未知返回 404 |
| GET /s/{token} | 匿名；文件分享直接下载，文件夹分享返回 {folder_id,folders,files} |
| GET /s/{token}?folder_id={descendant} | 仅共享根或后代的直接子项；空参数为共享根，不返回上级导航 |
| GET /s/{token}/files/{id}/download | 匿名下载共享文件或共享子树内文件，支持 Range/对象 307 |
| POST /api/uploads | JSON {name,folder_id,expected_size,part_size}；201 上传会话；大小受现有上传限制，part_size 为 1..8388608，分片数有约 10000 的上限 |
| PUT /api/uploads/{id}/parts/{index} | 零起始索引，offset=index*part_size；末片必须恰为剩余大小，其余恰为 part_size。首传 201，同哈希重传 200，不同内容 409，不覆盖旧片 |
| GET /api/uploads/{id} | 200 {session,parts:[{index,size,sha256}]}，过期/他人/未知 404 |
| POST /api/uploads/{id}/complete | 所有连续分片/大小/每片哈希通过后流式合并和哈希；201 File，事务中发布并删除会话。缺片/破损 409，重复 complete 已无会话返回 404 |
| DELETE /api/uploads/{id} | 取消并清理私有分片，204 |
| GET /api/folders/{id}/download | ZIP，支持 HEAD/Range；他人 404，危险持久名称拒绝生成 |
| POST /api/files/{id}/migrate-s3 | 认证且当前用户所有 + X-NetDisk-Maintenance；需配置对象客户端；成功 200，未授权 403，失败 503且保留安全读取路径 |

Native HTML/CSS/JS 无新增构建链：文件/目录分享按钮，分享列表撤销与一次性链接复制；资料设置与账号删除确认；目录 ZIP 按钮；分片上传 demo 共用现有选文件框。浏览器刷新后可手动贴回会话 ID 并选回同名同大小文件继续；demo 会重新发送全部分片核对幂等哈希，避免悄悄混合两个不同源文件，不把 Cookie/token 放入 Web Storage。

NFS/P2P 未实现、未开始。邮箱绑定、配额统计、分享分析和自动存储策略没有纳入本轮。


## P1 多文件上传与单向备份（2026-10-06，未提交）

TASK=BINGYAN-NETDISK-P1-MULTIUPLOAD-SYNC-V1。先接管 feature/full-netdisk-task 的全部现有未提交改动，独立回归再顺序收尾。MULTI_UPLOAD=PASS（浏览器原生 3 文件/下载、真实单项 400 + 2 成功，无 JS errors）；AUTO_SYNC=PASS（新增项目内 Go CLI/PowerShell 启动器，Windows 独立进程新增/修改/重启/锁/删除保留/5 版本摘要）。

真实 HTTP/MinIO 同步新增 8 项和 race PASS；gofmt、42 项顶层 go test ./...、go vet ./...、Node 18 项、Windows/Linux 构建 PASS。已有 CRUD/隔离/分享/dedupe/Range/分片/MinIO/ZIP/TCP NFS/P2P 回归通过，可选 kernel NFS mount 本轮 NOT_RUN。

默认单向保留版本、10 秒轮询、2 并发、8 MiB 分片和最多 5 次持久重试；只读取明确目录，无文件删除传播。详见 docs/automatic-backup.md、任务 TASK、REPORT 及 evidence/p1-checks.txt。业务后端/schema/前端本增量没有改动；当前源码及接管改动保留。无 commit/push/remote/live 卷操作，OpenCode/DeepSeek/子 Agent/后台 Codex=0。仅清理自己的本轮隔离资源，完成后停止。

## 最终封版检查（2026-10-06，BLOCKED）

TASK=BINGYAN-NETDISK-FINAL-CLOSEOUT-001。当前源码的最终隔离回归 PASS：Node 18 项、真实 Windows sync 独立进程、gofmt 空输出、go test ./...、go vet ./...、三个 Linux 程序及 Windows sync 构建；真实 MinIO、TCP NFSv3、kernel mount 读写/目录操作、两独立 P2P CLI 直传及 SHA-256、浏览器注册/登录/目录/原生三文件上传下载/勾选/ZIP/批量分享/账户设置 PASS。普通入口实际上传 300,000,001 bytes（36 片），下载内容一致；JS errors 为空。

正式 app 与 38125 demo 在接管时已停止。正式卷唯一既有挂载者为 bingyan-netdisk-app；只读挂载完成一致性完整 tar 备份、SHA-256 校验和私有全卷/元数据/blob manifest。原基线：12 个账号、8 个文件、1 个目录、2 个会话、8 个物理 blob。备份和旧正式二进制保留在受限 ACL 的 ignored .tmp/final-closeout-20261006，不提交真实数据、秘密或私有 manifest。

恢复到全新 bingyan-netdisk-final-copy-20261006 的迁移前完整性/外键/manifest PASS。当前版本启动并迁移后，账号认证记录 SQL 内部对比、账号/目录/逻辑文件元数据及内容 hash/size、原 8 文件 HTTP 下载均 PASS；物理 blob 从 8 变为 6，旧迁移去重流程清理两份内容相同的旧 storage_key。因此未满足 AGENTS.md 要求的全部原物理 blob manifest 完全一致，COPY_MIGRATION=FAIL，按停止条件判定 BLOCKED；未继续副本重启门槛或正式切换。

停止后再次只读核对正式卷，原计数/元数据/全部 8 个 blob hash/size 仍与基线一致。未 stage/commit/main 更新/push、未替换正式 bin/netdisk、未触碰 live schema、未创建正式合成账号。原工作树/索引完整保留。远端新增 README-only 73251fe 与 feature 分叉已获取分析，后续只能保留该历史正常合并，禁止 force push。

本轮 tmpfs smoke 与 MinIO 已停止并删除；失败副本容器已停止，副本卷、完整备份和私有诊断保留。38120 与 38125 保持接管时的停止状态。OpenCode/DeepSeek/子 Agent/后台 Codex=0。完整脱敏结果见 evidence/final-closeout-checks.txt。下一步需先解决物理副本保留与严格迁移验收合同，再从备份做全新副本迁移及重启验收；不得以逻辑文件一致替代本任务的物理 manifest 门槛。

## 佳琛网盘名称与存储位置核对（2026-10-07）

按用户要求将 internal/netdisk/web/index.html 的页面标题、首页可访问名称、登录欢迎语与两处品牌文字共 5 处改为“佳琛网盘”，README 标题同步更新。保留历史验收记录的原始名称。既有 TestBundledWebAndPolicyIsolation 与 TestBatchUploadWebAssets 均 PASS，当前源码 Linux netdisk 构建 PASS；全新 tmpfs 隔离服务实测 /healthz 和首页均 200，新名称出现 5 次、旧名称 0 次，标题正确。测试容器已停止并删除，构建保留在 ignored .tmp/brand-storage-20261007。

正式 bingyan-netdisk-app 当前仍停止，NETDISK_DATA_DIR=/data，未配置 NETDISK_S3_ENDPOINT。正式上传内容存于本机 Docker Desktop Linux 命名卷 bingyan-netdisk-data：引擎路径 /var/lib/docker/volumes/bingyan-netdisk-data/_data，容器内 /data/blobs 为文件字节，/data/netdisk.db（含 WAL/SHM）为账号、文件/逻辑目录与会话元数据，/data/tmp 为临时分片/缓存。本次只读核对：SQLite integrity/foreign keys PASS，12 账号、8 文件、1 目录、2 会话、8 物理 blob；原元数据与全部 blob hash/size 仍与封版前基线一致。du apparent bytes：卷 18,420,736；blobs 18,227,592；tmp 0；数据库主文件 57,344、WAL 103,032、SHM 32,768。没有读取或输出真实账号密码、密码 hash、会话或 capability 原文。

其他副本：38125 演示卷为 bingyan-netdisk-full-demo-data；失败的迁移演练卷为 bingyan-netdisk-final-copy-20261006，容器均停止。正式完整备份文件为 E:\冰岩实习\NetDisk\.tmp\final-closeout-20261006\live-volume-pre-closeout.tar（18,432,000 bytes），私有 SQLite 副本/manifest 同目录。项目范围文件名分类还发现 .tmp/s2b-backup 的历史归档/SQLite 副本、.tmp/full-a 的隔离测试 SQLite 文件，以及 .tmp/p1-cli-state-*、.tmp/p1-launch-state-* 下的合成 sync state.json；这些不是当前正式卷。项目根 data/、uploads/ 不存在。gomod/gocache 为构建缓存卷。Windows 宿主 Docker 虚拟磁盘的具体 VHDX 位置未跨项目范围检查，不推断其所在盘。

本次未替换正式 bin/netdisk，未启动正式 app、未迁移 live schema，正式二进制 SHA-256 仍等于封版前备份。封版的副本迁移门槛仍 BLOCKED，故新名称目前已落实在源码和验证构建，正式服务尚未切换。未 stage/commit/main 更新/push。OpenCode/DeepSeek/子 Agent/后台 Codex=0。

## 移动硬盘下载备份、归档与恢复（2026-10-07）

用户要求先实现软件功能，服务器购买、流量方案与原封版流程暂缓。本轮新增 internal/backup/pull*.go、平台空间检查、internal/netdisk/archive.go 及对应测试，扩展 netdisk-sync 和 PowerShell 启动器，保留原上传模式。服务端新增账号所有权保护的备份清单与归档 API；disk_archives 为独立附加表，归档记录不含存储凭据或能力令牌。

下载模式将原始字节保存为 versions/文件ID/SHA256/file-安全文件名，原名称、目录链、历史版本及空目录存入硬盘清单。每段 fsync 后才持久化进度，重启重新核对已下载段，完整 SHA-256/大小一致后发布；未变化文件本地校验后跳过网络下载，服务器删除不传播到硬盘。外置绑定状态、硬盘随机标识、根目录身份核对、进程锁、os.Root 路径约束及空间预留用于拒绝缺盘、误换盘和非法路径；本轮使用项目内合成目录验证，无实际移动硬盘挂载操作。

释放在线空间必须显式选择一个文件归档。客户端先重读完整本地备份，服务端在同一事务中核对内容、名称及目录链，再保存回执并释放文件引用；改变的文件拒绝归档，其他引用仍使用的 blob 保留，删除最后引用后的清理由已有持久 GC 处理。归档响应丢失可用相同回执重试。网页“硬盘归档”用文本节点展示合成测试中的硬盘标签、路径与已恢复状态，退出清空记录。

恢复通过原分片上传和请求回执重建文件，先保存新文件 ID，再确认归档恢复状态；重试不会重复发布。支持指定历史版本、全部最新记录及空目录恢复；已存在的新内容保留。服务器损坏后的新目标需显式 RestoreToOtherServer，且该模式禁止下载和归档。最新记录未完成时明确报错，可选之前已验证的历史版本。文件级客户端备份不替代 SQLite、账号和会话的一致性整卷备份；未实现桌面 GUI、自启动、自动容量归档或不同版本之间的分块差分。

当前源码验收：go test -count=1 ./...、go vet ./...、gofmt -l cmd internal 空输出；pull/archive 两组 race 测试；Node 原有批量上传/自动分片/勾选 18 项；Linux netdisk/netdisk-p2p/netdisk-sync 与 Windows sync 构建全部 PASS。新增八个顶层 pull/archive Go 用例及子例覆盖断网/损坏续传、Range/整文件哈希拒绝、换盘/空间/路径边界、远端删除保留、所有权、归档竞争、回执丢失、服务端重启、历史版本与新目标恢复。

真实依赖验收：MinIO 三个集成用例 PASS（含新增下载 Range、凭据隔离、归档最后引用对象删除与恢复）；kernel NFS mount/read/write/mkdir/rename/remove/rmdir PASS；两个独立 P2P CLI 直传 1,048,613 bytes 且 SHA-256 一致。Windows 原有上传 watcher smoke 和新增下载/归档/恢复 smoke PASS，后者验证零字节文件、1,048,613 bytes 文件、重启跳过、远端删除保留与恢复幂等。浏览器实际登录、归档列表、原生三文件上传、全选、账户显示名称保存、退出清空记录 PASS；首次脚本多点了一次已自动关闭的账户弹窗，修正测试步骤后通过，未因此修改产品行为。

构建和私有测试记录保存在 ignored .tmp/disk-backup-20261007，新 Windows 客户端经校验后原子替换 bin/netdisk-sync.exe，旧客户端保留在同一私有目录。正式 bin/netdisk 保持封版前 SHA-256；本轮测试使用独立 tmpfs 或临时目录，从未挂载正式卷。原封版的严格物理 manifest 阻塞仍存在，本轮未 stage/commit/main 更新/push、未正式迁移。详细脱敏结果及合成浏览器截图见 evidence/removable-backup-checks.txt 和 evidence/removable-backup-smoke.png；OpenCode/DeepSeek/子 Agent/后台 Codex 均为 0。

收尾核对：用安装后的默认 bin 客户端重跑公开下载 smoke PASS；PowerShell 启动器 status 模式以合成隐藏输入替身验证 PASS。62 个候选文本文件的高置信度秘密模式扫描无命中，git diff --check PASS，索引仍为空，客户端/完整备份均为 ignored。当前创建的隔离浏览器服务、MinIO 与 NFS 容器已停止并移除；仅保留原本停止的正式、demo 和迁移副本容器。

## GitHub 源码更新（2026-10-07）

在功能实现与验收报告后，用户再次明确要求“更新GitHub”。本次范围为发布已验收的软件源码、测试、简短 README 和脱敏证据；正式 38120 部署及严格副本迁移封版仍暂缓，不把源码发布等同于正式迁移验收通过。README 保留迁移阻塞说明。原有主线程执行、白名单暂存、秘密扫描、保留远端历史、禁止 force push 和禁止修改 remote 等边界继续适用。

更新前再次核对：feature/full-netdisk-task 基线 3250d42，本地 main f04850f，远端 main 73251fe 未出现额外新提交；远端与 feature 的既有分叉仅包含一个 README 修改提交。按普通合并保留其历史，并保留“本地运行、电脑关机时不可访问”的使用说明；旧“尚无分享/续传”等描述已被当前真实实现取代。此前对当前源码的 Go、Node、Windows 客户端、真实 MinIO/NFS/P2P 和浏览器验收结果见 removable-backup-checks.txt；本次未修改已验收业务代码。候选集再次秘密扫描无命中，私有完整备份、数据库、真实用户内容、二进制和临时产物不进入提交。
