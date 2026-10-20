# SiAgent

轻量、自托管的 AI Agent 平台。完整产品范围见 [plan.md](plan.md)。

当前已实现内部使用路径，并完成阶段三运行可靠性及阶段四 Skills / MCP、阶段五原生知识库的本地功能：**启动 → 使用首次令牌设置管理员密码 → 配置模型 → 默认助手对话**。支持 Web 流式 Markdown 回答、取消、按当前配置获取模型列表与下拉选择、模型连接测试、助手指令与启停、会话历史和重启后继续对话。

可按需上传 Skills 技能包或本机文件夹、连接 stdio / Streamable HTTP MCP，并逐项授权给指定助手。知识库支持独立管理、文档处理、中英文全文检索和可打开的回答来源，不依赖 MCP。渠道通过统一插件接口接入，随系统分发基于官方 SDK 的企微智能机器人长连接（BotID/Secret）与兼容自建应用两个插件，支持扩展进程插件及 Webhook 插件；通用消息底座统一管理会话、去重、任务和投递，并通过本地协议链路验证；真实企业收发、外部模型及第三方 MCP 兼容性仍待验收。实现进度见 [docs/progress.md](docs/progress.md)；企微配置及扩展见 [渠道说明](docs/channels.md)。

## 一键安装

**发布包自带 Node.js 和 Runtime 生产依赖，使用端无需安装 Node、npm 或 Go。** 安装脚本自动识别平台和 CPU 架构，下载 GitHub Release 并校验 SHA-256。以下下载地址需先发布至少一个包含安装脚本的正式版才可使用；`latest` 选择最近的正式版。

### Linux / macOS

支持 amd64、arm64，默认安装到用户目录，无需 sudo：

```sh
curl -fsSL https://github.com/vst93/SiAgent/releases/latest/download/install.sh | sh
```

需要 curl、tar，以及 sha256sum 或 shasum；Linux 另需 xz，Debian/Ubuntu 可执行 `sudo apt-get install curl xz-utils`。Linux 使用 glibc，建议 Ubuntu 22.04 / Debian 12 或更新版本，不支持 Alpine/musl。macOS 建议 15 或更新版本；当前发布包未签名或公证，受系统 Gatekeeper 策略约束。

指定版本安装：

```sh
curl -fsSL https://github.com/vst93/SiAgent/releases/latest/download/install.sh -o install-siagent.sh
sh install-siagent.sh --version v0.1.0
```

### Windows

**Windows 当前通过 WSL 运行 Linux 发布包，尚无原生 Windows 二进制。** 已初始化 Ubuntu WSL 的用户，在 PowerShell 中执行：

```powershell
Invoke-RestMethod https://github.com/vst93/SiAgent/releases/latest/download/install.ps1 | Invoke-Expression
```

首次使用 WSL 时，先在管理员 PowerShell 中执行：

```powershell
wsl --install -d Ubuntu
```

按提示重启并打开 Ubuntu，完成 Linux 用户创建，再执行安装脚本。Windows 安装脚本会配置 `siagent.cmd` 命令；运行参数中的文件路径使用 Linux 路径。

### 启动、数据与升级

安装完成后打开新终端运行：

```sh
siagent
```

安装器默认注册并启动当前用户的 systemd/launchd 服务；打开 <http://127.0.0.1:8080>，从 `journalctl --user -u siagent -n 50`（macOS 为 `~/Library/Logs/SiAgent/siagent.log`）复制首次令牌并设置管理员密码。使用 `--no-service` 可跳过服务安装；`siagent service status|restart|stop|start|uninstall` 管理服务。服务默认仅监听本机。

| 内容 | Linux / macOS 默认位置 | Windows（WSL）默认位置 |
| --- | --- | --- |
| 启动命令 | `~/.local/bin/siagent` | `%LOCALAPPDATA%\SiAgent\bin\siagent.cmd` |
| 程序及版本 | `~/.local/share/siagent/versions/` | WSL 内 `~/.local/share/siagent/versions/` |
| 持久数据 | `~/.local/share/siagent/data/` | WSL 内 `~/.local/share/siagent/data/` |

Linux/macOS 设置 `XDG_DATA_HOME` 时，程序与数据根目录改为 `$XDG_DATA_HOME/siagent`。通过 `siagent -data-dir /path/to/data` 可指定其他数据目录。

安装器部署可在“配置 → 运行与维护 → 应用更新”检查版本、后台下载校验，并确认停机备份后自动重启；新版本启动失败会恢复旧程序与完整数据。也可手动升级：停止完整服务并备份数据，然后重新执行安装脚本；默认服务会被重启，`--no-service` 部署需手动启动。脚本保留旧版本和独立数据目录；数据库迁移后的回退需恢复升级前完整备份。PDF 处理另需 Poppler，外部 MCP 命令和 Skill 脚本需要各自环境；Windows 下这些依赖安装在 WSL 内。

应用更新和安装脚本先尝试 GitHub 官方地址并重试，失败或超时后切换国内可用的镜像；可用 `SIAGENT_GITHUB_MIRRORS` 自定义或禁用回退。Docker 显示镜像拉取/重建指引。重启、备份与失败恢复详见 [应用内更新](docs/application-updates.md)。

自定义安装目录、指定 Windows 发行版与版本、WSL 引导参数等见 [完整安装说明](docs/installation.md)。

## 源码开发启动

需要 Go 1.26+、Node.js 24 LTS、npm。PDF 文档还需要 Poppler（macOS：`brew install poppler`；Debian/Ubuntu：`apt-get install poppler-utils`），TXT/Markdown 不需要。Go 平台 SQLite 使用 modernc.org/sqlite，密码使用 x/crypto 的 bcrypt；锁定的当前依赖要求 Go 1.26。以下命令在项目根目录执行。

```sh
make install
make run
```

1. 打开 <http://127.0.0.1:8080>，从启动终端复制首次启动令牌，设置管理员密码。密码须为 12–72 字节；汉字占多个字节。
2. 选择 OpenAI Chat Completions、OpenAI Responses 或 Anthropic Messages 协议，输入服务地址、模型名称和密钥，保存后自动创建默认助手。
3. 在左侧导航选择“Agent 对话”，选择助手、创建会话并开始对话。左侧导航的配置分组集中管理模型连接、Agent、知识库、插件、渠道及运行维护。
4. 在“配置 → 模型连接”增加或测试模型；在“配置 → Agent”编辑角色、选择模型、配置私聊/群聊与上下文策略、回复输出、关联知识库和逐项授权工具。见 [多模型与助手管理](docs/agents.md)。
5. 在“配置 → 知识库”创建、上传和测试检索；在“配置 → 插件接入”的独立页签上传 Skills 或连接 MCP。资源统一管理，访问范围分别在 Agent 中授权，见 [知识库说明](docs/knowledge.md) 和 [工具说明](docs/tools.md)。

左侧导航底部的 ☆ 可将当前页面固定为默认打开页面；偏好保存在当前浏览器。具体页面链接优先于默认页，浏览器前进/后退可切换工作区。按 `⌘K` / `Ctrl K` 打开命令面板，可跳转页面、新建对话、切换主题。导航和场景说明见 [工作区使用](docs/workspaces.md)。

首次设置统一要求令牌，避免本地反向代理使远程请求被误判为本机请求。令牌只在未初始化时输出到终端，不由 HTTP 返回，初始化后失效；未完成设置时重启会生成新令牌。不提供公开注册或密码找回。

服务地址应包含服务要求的路径前缀，例如 `https://api.example.com/v1`。本地无鉴权模型服务仍需填写该服务接受的占位密钥。连接测试按已保存的协议发送一条简短文本，不能证明工具调用或所有模型参数均兼容。上下文窗口、输出长度、推理和采样配置见 [模型配置](docs/models.md)。

普通对话默认没有任何工具权限。工具设置及信任边界见 [Skills 与 MCP 使用说明](docs/tools.md)。模型、助手指令、所选技能与工具、知识库绑定、会话/上下文/输出策略、凭据和时限在任务提交时保存独立快照，排队期间修改配置也不会改变已有任务；配置变更只影响新提交的任务，不向模型重复注入平台历史。停用助手不打断已有任务。

## 部署

**独立发布包自带固定版本 Node.js 和 Runtime 生产依赖，目标服务器无需安装 Node、npm 或 Go。** 下载 GitHub Release 对应 Linux/macOS、amd64/arm64 的压缩包，完整解压后直接运行包内的 `siagent`。Go 自动按自身位置查找私有 Node 与 Runtime，可从任意工作目录启动；默认数据目录为发布包内的 `data/`，建议通过 `SIAGENT_DATA_DIR` 指定独立持久目录。

本地生成发布包：在与目标相同的操作系统及 CPU 架构上执行 `make install && make package`，产物位于 `artifacts/`。`make build` 仍用于源码开发，不能仅复制 `bin/siagent` 部署。PDF 处理另需 Poppler。推送 `v0.1.0` 这类版本标签后，GitHub release workflow 会构建、验证并发布四个平台的 `.tar.xz` 压缩包、两份安装脚本及统一的 SHA-256 校验文件；手动触发默认生成草稿，版本号可留空，自动将最高版本标签的补丁号加 1（无标签时从 `v0.0.1` 开始）。详见 [独立发布包、发版流程与缺依赖排查](docs/server-package.md)。

提供多阶段 [Dockerfile](Dockerfile) 和 [compose.yaml](compose.yaml)：一个容器运行 Go 与其监管的 Node Runtime，一个持久卷保存全部数据，以非 root 用户运行。Compose 开启 init 以回收异常退出的子进程；直接 docker run 时应使用 `--init`。

```sh
docker compose up --build -d
docker compose logs siagent
```

默认仅将宿主机 `127.0.0.1:8080` 映射到容器。启动日志中的令牌用于首次设置，完成后从 Web 管理模型。当前开发环境没有 Docker 引擎，容器定义尚未实际构建运行；本地等价构建、进程启动和持久化已验证。

配置公网 HTTPS 反向代理时，将 `SIAGENT_PUBLIC_URL` 设置为浏览器实际访问的源地址，例如 `https://agent.example.com`，并转发到本地端口。该配置同时用于来源校验和启用 Secure Cookie。仅信任显式配置，不依赖请求中的转发头判断 HTTPS。代理需允许 SSE 长连接，关闭 `/api/.../events` 的响应缓冲；例如 Nginx 使用 `proxy_buffering off` 和足够长的 `proxy_read_timeout`。更改公开地址后重新登录。

| 配置 | 默认值 | 作用 |
| --- | --- | --- |
| `SIAGENT_GITHUB_MIRRORS` | `https://ghproxy.net/,https://gh-proxy.com/` | 更新/安装下载的 HTTPS 镜像前缀，逗号分隔；空值关闭回退 |
| `SIAGENT_DEPLOYMENT` | 自动识别 | `docker` 显示容器更新指引；`managed` 交由外部服务管理器重启 |
| `SIAGENT_DATA_DIR` / `-data-dir` | 安装脚本：`~/.local/share/siagent/data`（可由 XDG 调整）；手动解压：`<安装目录>/data`；源码：`data` | 唯一持久化目录 |
| `SIAGENT_LISTEN` / `-listen` | `127.0.0.1:8080` | Go HTTP 监听地址 |
| `SIAGENT_PUBLIC_URL` / `-public-url` | 空 | 浏览器源地址；HTTPS 时启用 Secure Cookie |
| `-runtime` | `runtime/dist/index.js` | Runtime 入口；发布包按安装目录定位 |
| `SIAGENT_NODE` / `-node` | 源码：`node`；发布包：`<安装目录>/libexec/node` | Node.js 可执行文件 |
| `SIAGENT_CHANNEL_WORKER` / `-channel-worker` | `runtime/dist/channel-wecom.js` | 内置渠道入口；发布包按安装目录定位 |

健康检查：`/health/live` 表示 Go 存活，`/health/ready` 表示 Runtime 就绪，均不检查外部模型。模型可用性在设置中单独测试。Runtime 异常退出最多退避重启 3 次，耗尽后整个平台退出；运行结果不明时标记中断，不自动重试。

## 数据与备份

```text
<data>/
  platform.sqlite               Go 独占：管理员、会话、加密模型/MCP 配置、授权、消息及工具记录
  master.key                    模型/MCP 与任务凭据加密主密钥，权限 0600
  runtime/execution.sqlite      包装层内部执行状态及序号
  runtime/conversations/*.sqlite Pi Durable 独占：模型上下文及任务状态
  files/                        文件目录，当前不向 Agent 开放
  skills/<id>/                  系统管理的 Skill 目录：指令、脚本与参考文件
  knowledge/<document>/<version>/ Go 管理的原文件、解析文本；索引及元数据在平台数据库
  knowledge/staging/            临时上传，未提交残留在启动时清理
  channel-adapters/<package>/    可选可信渠道适配包，启动时发现，包含清单和依赖
  workspaces/<agent_id>/        各助手独立的本地 MCP 工作目录；不是操作系统沙箱
  workspaces/connection-tests/   MCP 连接测试工作目录
```

平台数据库当前版本为 **26**。从版本 1–25 升级时自动迁移，已验证管理员、模型密钥、消息、既有工具历史、Skills/MCP 授权、知识库及来源保留，历史会话归属默认助手，版本 6 增加企微渠道与永久会话来源，版本 7 增加持久化会话清理，版本 8 增加模型协议与生成参数，版本 9 迁移为通用渠道表并保留旧企微记录，版本 10 增加 Agent 会话/上下文/输出策略及思考展示记录，版本 23 增加兜底模型与对话附件，版本 24 记录渠道会话身份，版本 25 增加知识库上传限制，版本 26 增加加密的 Agent 系统环境变量；升级前先停机备份完整数据目录。迁移后旧版程序不能直接打开该数据库，回退需要恢复升级前的完整备份。

两个模块的 SQLite 均启用 WAL。数据目录创建权限为 0700，进程 umask 为 0077。主密钥独立保存，模型密钥、MCP 环境变量/请求头、Agent 系统环境变量、企微 Secret/Token/AES Key 及任务凭据快照使用 AES-256-GCM 加密；完整备份被读取时，这种加密不能保护其中的密钥。

**备份和恢复必须停止整个服务后操作。** Docker 部署先执行 `docker compose stop`，再用挂载同一卷的临时容器完整归档；本地部署停止 Go 与 Runtime 后归档完整数据目录。恢复时保持停机，包含数据库、WAL 文件、Runtime 数据、文件和 `master.key`，先用同版本验证。不要运行中直接复制数据库，不要只备份 `platform.sqlite`。完整命令、恢复验收及回退步骤见 [部署与恢复](docs/deployment.md)。进程 E2E 已验证完整停机归档、逐文件校验、新目录同版本恢复与真实 Runtime 上下文续聊。

缺失主密钥、主密钥与已有凭据不匹配、或平台数据库版本高于当前支持版本时，启动会失败；不会静默生成新密钥或继续服务。不要同时直接启动多个 Runtime 包装进程操作同一目录；正常入口是 Go，Go 通过目录锁限制单实例。

## 检查与开发

容器业务验收：Docker 可用时执行 `make test-container`，覆盖真实 Runtime 对话、知识文件、跨容器重建和跨数据卷完整恢复；本机无 Docker 时可先执行 `python3 tests/container.py --local` 验证共享业务流程，此模式不代表容器通过。


```sh
make test         # Go 竞态检测、平台 API/安全测试和 Runtime 集成测试
make test-e2e     # 先构建，再运行完整 Go + Node + 本地模型协议服务的验收；需要 Python 3
```

端到端测试覆盖首次设置、登录会话、密钥加密、错误密钥提示、默认助手、真实 Runtime 问答、SSE、重启后的历史与上下文、多模型/助手隔离及归档历史、退出登录，以及 FIFO 排队、跨会话并发、队列溢出、排队取消、实际超时和 Runtime SIGKILL 后不自动重放；还覆盖 Skills 导入、真实本地 MCP 配置/测试/关联/调用、凭据脱敏、工具超时及 Runtime 崩溃时清理 MCP 子进程；知识库验证上传、实际索引及 Agent 检索、来源记录、整机进程重启和删除。它使用本地模型和 MCP 协议测试服务，不代表真实外部模型已验证。

可选浏览器验收：先 `make build`，再执行 `npm --prefix web exec -- playwright install chromium` 和 `npm --prefix web run test:browser`。测试自动启动临时实例，覆盖首次使用、双工作区切换与草稿保留、具体栏目默认页/深链接/前进后退、连接测试、刷新历史、安全文本显示、移动布局、运行中继续排队、取消排队、限制设置、技能导入和资源预览、MCP 连接与授权、调用详情及密钥脱敏；还覆盖知识库工作台、原生检索、来源安全显示、失败/成功文档更新和历史/删除引用。可通过 `SIAGENT_CHROMIUM` 指定已有 Chromium 可执行文件；截图默认保存到系统临时目录下的 `siagent-browser-artifacts`。

界面默认简体中文，可在顶栏、移动端抽屉或登录页切换英文；选择按浏览器保存。对话支持图片/文件附件，模型连接可声明文本/图片输入，Agent 可配置有序 fallback 模型；详见 [对话附件](docs/chat-attachments.md)。系统级启动参数、附件限制和运行默认值可放在系统 config 目录的 `SiAgent/config.json`，格式、路径和优先级见 [外置系统配置](docs/configuration.md)。开发 Web 时先启动 Go，然后另开终端执行 `npm --prefix web run dev`。Vite 代理 `/api`、`/health` 和 `/callbacks` 到 Go；开发时不配置 `SIAGENT_PUBLIC_URL`，浏览器源地址随 Vite 请求校验。生产构建静态资源由 Go 嵌入提供，不依赖外部字体或 CDN。前端系统图标统一使用 Phosphor Icons，后续新增图标通过公共组件引用；布局、尺寸与可访问性约定见 [界面与图标规范](docs/ui-guidelines.md)。

保留阶段一的命令行探针：停止其他服务实例后，通过环境变量 `SIAGENT_MODEL_BASE_URL`、`SIAGENT_MODEL`、`SIAGENT_MODEL_API_KEY` 临时传入模型连接信息，运行 `./bin/siagent -probe '你好'`。使用 `-probe-tool` 可显式启用无副作用的 `add` 和 `wait` 验证工具。探针输出包含对话及工具详情，仅用于本地诊断。

## 运行行为与边界

- 默认同时执行 2 个任务，同一会话按提交顺序串行执行；每个会话最多等待 10 个、实例最多等待 100 个。容量满时明确提示，不插入任务或静默丢弃消息。
- 可以在回答过程中继续发送消息，各自排队，不合并或打断上一轮。排队任务可单独取消；停用助手只拒绝新消息，已有队列继续处理。
- 默认每个运行最长 5 分钟、单次工具 60 秒、最多 10 轮工具调用。“配置 → 运行与维护”可调整。并发/队列上限立即生效，时限与工具轮数只影响之后新提交的任务。
- 超时或取消先进入“取消中”，确认停止后才结束；超过 10 秒仍无法确认停止时，强制重启 Runtime，结果保守标为中断，不把未知操作当作已撤销。
- 收到运行终态后还需 Runtime 确认会话和存储已释放，才派发下一条同会话任务。管道写入有 2 秒期限，避免失去响应的进程阻塞取消检查。
- 平台幂等保存事件序号、消息、状态及诊断记录；Runtime 查询用于核对资源释放，不据状态查询推断丢失的成功结果。发现协议序号缺口或 SDK 事件溢出时，不宣布运行成功。
- 每条回答下可展开运行详情，查看模型配置版本、工具名称及状态、最近 200 条诊断事件。诊断记录不复制消息正文、密钥、工具参数或原始结果。取消/中断后的工具记录会明确标识需要核对的操作。参数和结果作为独立业务记录保存，逐项展开后经鉴权获取，已知凭据脱敏；不复制到普通诊断事件。
- SSE 重连读取平台持久化会话快照；诊断事件日志不是模型或工具结果的重放接口。
- 服务或 Runtime 重启后，所有未完成任务（包括排队任务）标为中断；保留已产生内容，不自动重试或继续旧队列。管理员核对后显式发起新消息。
- Agent 可限制仅私聊、仅群聊或两者都允许，群内可按成员隔离或全群共享；可配置渠道空闲后新建会话和用户 `/new` 权限。Web 对话属于私聊。
- 上下文默认保留最近 20 轮，也可选择近期时间窗口、自动摘要压缩或完整历史。裁剪按完整轮次保留工具配对，历史记录不删除；摘要使用当前模型，可能增加用量并遗漏细节。模型窗口、最大输出预留与独立的 8 MiB 保护继续生效。
- 回复可选简洁、均衡或完整预设，分别控制模型返回的思考摘要、工具调用详情、检索来源和渠道处理中提示；可剔除模型回答中的 think 标签并按上限返回思考摘要。渠道支持原地更新时会立即确认收到并持续输出已生成内容。展示开关不改变工具权限，管理员运行审计保留。配置与边界见 [Agent 策略说明](docs/agent-policy.md)。
- 每个助手可配置加密的系统环境变量，供 Skills 与本地工作区命令读取；值不回显，只注入该助手的工作区命令进程并对输出脱敏。
- Skills 和 MCP 默认关闭，启用时确认可信来源及写入/删除权限；不提供通用 Shell，不自动执行 Skill 脚本或安装依赖。MCP 工具执行结果不明时停止后续调用，不自动重试。知识库通过 Go 原生全文检索接入，不依赖 MCP；绑定在提交时固定，文档查询使用当时已发布版本，删除立即排除。支持多个模型连接及助手，分别关联能力；会话固定助手，归档保留历史。企微内部应用经签名 AES 回调入站，按应用/成员隔离会话并异步回复；发送结果不明不自动重发，真实企业验收仍待补齐。

完整功能对照与未验收项见 [首版功能对照](docs/feature-status.md)。

资料：[模型配置](docs/models.md) · [工作区使用](docs/workspaces.md) · [多模型与助手](docs/agents.md) · [原生知识库](docs/knowledge.md) · [知识库设计](docs/knowledge-design.md) · [Skills 与 MCP](docs/tools.md) · [Runtime 验证](docs/runtime-validation.md) · [通信协议](docs/runtime-protocol.md) · [自动更新与命令行](docs/application-updates.md) · [部署与恢复](docs/deployment.md) · [发布检查](docs/release-checklist.md) · [企微接入指南](docs/wecom.md) · [企业微信验证](docs/wecom-validation.md) · [开发进度](docs/progress.md)。
