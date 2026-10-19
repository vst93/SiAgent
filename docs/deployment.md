# 部署、停机备份与恢复

平台入口为一个 Go 进程，负责私有 Node/Pi Durable 子进程、独立渠道适配进程及其生命周期。一个实例只使用一个数据目录。当前 schema 为 23；容器业务与卷备份恢复已通过本地 Docker 29 验收，真实企业集成尚未验收，发布状态见 [release-checklist.md](release-checklist.md)。

## 本地构建与启动

支持 Linux/macOS 开发，需 Go 1.26+、Node 24、npm；处理 PDF 另需 Poppler 的 `pdfinfo` 和 `pdftotext`。Windows 使用 Linux 容器或 WSL，本版没有 Windows 二进制验证。

```sh
make install
make build
./bin/siagent -data-dir ./data -listen 127.0.0.1:8080
```

首次访问 `http://127.0.0.1:8080`，使用终端打印的一次性初始化令牌设置管理员密码。先配置模型并测试普通对话，再按需配置工具、知识库和企微。没有 MCP、知识库、企微凭据也可以使用基础对话。

数据目录默认 `./data`，可通过 `SIAGENT_DATA_DIR` 或 `-data-dir` 指定。不要单独运行 Runtime 操作同一数据目录；Go 入口持有目录排他锁。模型/MCP/企微秘密通过登录后的管理界面保存，不必放进环境变量或 Compose 文件。

## 迁移到普通服务器

`make build` 用于源码开发，不会将 Node.js 和 Runtime 的 npm 依赖打进 Go 二进制。跨服务器部署请下载 GitHub Release，或使用 `make package` 生成完整发布包，其中包含 Go 程序、私有 Node、Runtime JS 和 npm 生产依赖。目标机无需安装 Node、Go 或 npm，完整解压后直接启动包内的 `siagent`。Go 按自身位置定位运行文件，默认数据目录为安装目录的 `data/`；建议通过 `SIAGENT_DATA_DIR` 指定独立持久目录。

支持 Linux/macOS 的 amd64、arm64，构建与目标需使用相同操作系统和 CPU 架构。Linux 使用 glibc，不支持直接在 Alpine/musl 上运行；macOS 包暂未签名或公证。PDF 处理仍需 Poppler，外部 MCP 命令需要各自环境。GitHub 发版工作流在版本标签推送后完成四平台构建、迁移启动验证和附件发布；完整步骤及故障排查见 [server-package.md](server-package.md)。

## 单容器与公开访问

仓库提供 Dockerfile 和 `compose.yaml`：

```sh
docker compose build
docker compose up -d
docker compose logs -f siagent
```

默认只向宿主机 `127.0.0.1:8080` 发布端口，数据在 Compose 命名卷。容器以 `node` 用户运行，包含 Node Runtime、嵌入前端的 Go 程序和 Poppler；不需要外部数据库。停止宽限时间为 15 秒，停机等待 Go 及 Runtime 收尾。

容器业务已在本机 Docker 29 通过 `tests/container.py`：HEALTHCHECK、非 root/0700 权限、Poppler、真实 Runtime 对话、跨容器重建及跨数据卷备份恢复均通过。本地因 Go Bookworm 镜像下载停滞，使用缓存的 Go 1.26 Alpine 编译阶段生成同样的 CGO_ENABLED=0 静态程序，最终运行镜像仍为 Node 24.22.0 Bookworm；[GitHub CI](https://github.com/vst93/SiAgent/actions/runs/38946294294) 已通过仓库正式 Dockerfile 的完整构建及容器业务/卷恢复验收。测试服务直连同一网络的模型协议服务，不依赖宿主 Docker 客户端注入的代理。

公网使用 HTTPS 反向代理，并在 Compose 环境中设置浏览器实际源地址：

```sh
SIAGENT_PUBLIC_URL=https://agent.example.com docker compose up -d
```

`SIAGENT_PUBLIC_URL` 只填写浏览器看到的协议和主机，不包含路径；HTTPS 会启用 Secure Cookie。修改地址后重新登录。

网页资源、API 和知识来源链接均按当前目录解析，因此可以挂在 `/siagent/` 这类子路径。二选一：

- 反向代理保留前缀并转发到根路径（Nginx `proxy_pass http://127.0.0.1:8080/;`），或
- 启动时设置 `SIAGENT_BASE_PATH=/siagent`（对应 `-base-path /siagent`），由 SiAgent 自己挂载到前缀。此时健康检查仍保留在根路径，供自动更新探测。

代理需把 `/siagent/` 转到实例，并把不带斜杠的 `/siagent` 重定向到 `/siagent/`。Nginx 示例：

```nginx
client_max_body_size 21m;
location = /siagent {
    return 301 /siagent/;
}
location ^~ /siagent/callbacks/ {
    access_log off;
    proxy_pass http://127.0.0.1:8080/callbacks/;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_buffering off;
}
location /siagent/ {
    proxy_pass http://127.0.0.1:8080/;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_buffering off;
    proxy_read_timeout 1800s;
}
```

如果实例自身已用 `-base-path /siagent` 挂载，代理直接把 `/siagent/` 转发到 `http://127.0.0.1:8080/siagent/`，不要再剥掉前缀。证书和 TLS server 配置由部署环境提供。SSE 依赖关闭缓冲和持续连接。文档文件上限为 20 MiB，代理需允许 multipart 的额外大小。企微回调路径不加管理员登录或 CSRF 层，其自身验签解密；企业还需配置应用可见范围和可信 IP/域名，见 [wecom.md](wecom.md)。

`/health/live` 表示 Go 存活，`/health/ready` 要求平台和 Runtime 就绪。异常重复退出应检查服务退出码、普通日志和认证诊断；不要持续无上限重放失败任务。日志不应被配置为记录请求正文、Cookie、模型密钥或回调密文/明文。

## 本地停机备份

先停止整个服务并等待退出，例如终端 Ctrl-C，或停止管理 Go 入口的服务管理器。确认 Go 管理的 Runtime/MCP 子进程已经退出。**运行中不得复制 SQLite 或直接打包数据目录。**

从项目根目录执行，归档置于数据目录之外：

```sh
mkdir -p backups
chmod 700 backups
umask 077
tar -C ./data -czf "$PWD/backups/siagent.tar.gz" .
shasum -a 256 backups/siagent.tar.gz
shasum -a 256 bin/siagent runtime/package-lock.json web/package-lock.json
```

记录归档校验和、应用二进制/源码或镜像版本、Runtime 依赖版本及部署配置。保留完整数据目录，包括 `platform.sqlite` 及 WAL/SHM（存在时）、`master.key`、全部 Runtime 数据库、知识库原件、Skill 副本、文件和工作目录。不能只备份平台数据库。

归档包含管理员数据、业务消息和主密钥，须使用受限存储或额外加密；AES-GCM 密钥加密不能保护被完整读取的备份。

## 本地恢复到另一个目录

使用自己生成、校验完整性的可信归档。原实例保持停机；先安装同版本的 Go 程序、Runtime 和依赖。**目标目录必须不存在或为空，不向已有实例目录叠加解包。**

```sh
(
  set -eu
  mkdir -m 700 restored-data
  tar -C ./restored-data -xzf ./backups/siagent.tar.gz
  chmod 600 ./restored-data/master.key
  exec ./bin/siagent -data-dir ./restored-data -listen 127.0.0.1:8080
)
```

管理员账户随备份恢复，应使用原密码登录。不要重新初始化、删除主密钥或只替换数据库。更换机器时重新安装外部 MCP 命令及其依赖，检查配置中的命令绝对路径；这些系统依赖不属于平台数据目录。源目录和恢复目录不必相同，平台的 Skill/知识库/工作目录按新根目录定位。

验收至少包括：原密码登录；模型连接测试成功；历史和来源可读取；知识库检索与原文件存在；Skills/MCP 关联保留；继续原会话时使用原模型上下文；中断任务仍有明确状态；企微配置及历史保留。相同企业应用不能同时启动原实例和恢复副本进行收发，验证副本需先隔离外部收发。恢复公网实例时重新确认域名、可信 IP 和回调路由。

`python3 tests/e2e.py` 已实际停止完整 Go/Node 服务、归档所有文件、逐文件 SHA-256 比较、解包到新目录，再启动同一二进制。已验证新登录、模型/MCP 密钥、加密企微回调密钥、Skill 文件、工作目录、原生知识索引/原件/历史墓碑、助手归档状态和真实 Runtime 原上下文继续对话；恢复启动不会自动重放旧模型任务。

## Compose 命名卷备份和恢复

以下命令保留相同服务挂载规则，并通过替代 entrypoint 避免启动 Go/Runtime。**命令是待 Docker 环境执行的部署方案，本机没有容器验收证据。**

停机并备份：

```sh
docker compose stop
mkdir -p backups
chmod 700 backups
docker compose run --rm --no-deps --user root --entrypoint sh \
  -v "$PWD/backups:/backups" siagent \
  -c 'umask 077; tar -C /data -czf /backups/siagent.tar.gz .'
shasum -a 256 backups/siagent.tar.gz
```

恢复到新的 Compose 项目及新卷，保持原项目停机：

```sh
docker compose -p siagent-restored run --rm --no-deps --user root --entrypoint sh \
  -v "$PWD/backups:/backups:ro" siagent \
  -c 'set -eu; test -z "$(ls -A /data)"; tar -C /data -xzf /backups/siagent.tar.gz; chown -R node:node /data; chmod 700 /data; chmod 600 /data/master.key'
SIAGENT_PUBLIC_URL=https://agent.example.com docker compose -p siagent-restored up -d
```

不要删除原卷来测试恢复，不同时运行两份同一业务实例。备份文件已移出命名卷，应另行保存。原系统使用不同镜像版本时先准备对应同版本镜像，再执行恢复。

## 升级与回退

1. 停止完整服务，按上述步骤备份数据，并保留可运行的旧二进制/依赖或镜像版本。
2. 在隔离副本中用目标版本验证迁移和业务读取；涉及外部收发时使用独立测试应用或断开网络。
3. 确认后升级正式实例。平台 schema 1–22 会迁移至 23；更高版本数据库拒绝打开，主密钥缺失或密文不能解密时拒绝启动。
4. 升级失败时停止新版本，保留故障目录供诊断，切回旧版本并恢复**升级前完整备份**至空目录。不要用旧程序直接打开升级后的数据，也不要只回退 SQLite 单文件。

异常退出后的未完成任务会标为中断；结果不明的 MCP 调用和企微发送不自动重放。平台只对确定性的本地知识处理进行安全恢复，不承诺透明恢复外部副作用。


## 运行容器业务验收

在可用 Docker 环境执行：

```sh
make test-container
# 已构建相同镜像时，也可单独执行：
python3 tests/container.py --image siagent:check
```

脚本使用随机名称的独立测试网络、无外部供应商依赖的模型协议服务和两份临时命名卷，应用 API 仅发布到宿主 127.0.0.1，检查实际 HEALTHCHECK、非 root 用户、数据根目录 0700 和 Poppler。随后经管理 API 完成初始化、密钥配置、真实 Runtime 对话、知识上传/检索；销毁并重建原容器验证持久化，再停机完整归档并恢复至新卷，比较所有文件 SHA-256，验证登录、模型密钥、知识原件/索引/绑定和原会话上下文续聊。所有测试容器、网络、卷和临时归档在退出时清理；不使用现有业务卷。

当前无 Docker 时，可在 `make build` 后运行 `python3 tests/container.py --local`，使用真实本地 Go/Node 验证同一业务流程。该模式已通过，但不验证 Docker 命令、镜像内容、容器权限或网络，不能代替容器验收。

镜像将新数据根目录创建为 0700；已有宿主机绑定目录的所有者和权限仍由部署者设置。`.dockerignore` 排除默认备份、恢复目录和 `.env` 文件，避免这些文件进入构建上下文。自定义数据目录和归档也应放在构建目录之外或加入排除规则。


## 会话清理、磁盘提示与日志

点击会话标题栏的删除图标打开“删除此对话？”确认弹窗。先核对已产生内容和工具操作，再勾选确认；任务排队/执行/取消中、资源未释放或仍有待发送回复时返回拒绝。需要时先取消任务或等待结束，再提交删除。请求持久化后旧会话隐藏，后台经 Runtime 确认删除模型上下文后清理平台历史；服务重启继续清理，失败会安全重试，不自动重跑业务任务。

企微保留最小去重墓碑，删除正文与当前成员会话映射后，新消息开启新会话。清理不删除模型/助手/知识文档，不撤销外部操作，不清理旧备份，也不保证 SQLite 空闲页安全擦除或即时缩小。待清理数量可在“配置 → 运行与维护”点击“刷新状态”查看；持续未完成时检查 Runtime 就绪、数据目录挂载和文件权限。

同一诊断入口显示数据用量、文件数、所在磁盘可用量和低空间提示。统计不跟随目录内符号链接，超出时间/条目限制时标为部分结果；不会自动删业务数据。

默认 Compose 使用 json-file 日志驱动，每份最大 10m，最多 3 份。裸进程只输出到标准错误，应由宿主的服务管理器配置保留与轮转；不在数据目录另建无界日志文件。反向代理的回调路径示例关闭访问日志，避免 URL 验证的 echostr 密文落入请求 URL 日志。

`DELETE /api/conversations/{id}` 要求登录、CSRF 及 `{"confirm":true}`，接受后返回 202；`GET /api/storage` 同样要求登录。schema 7 保存清理请求；升级前备份完整目录，回退仍须使用升级前备份。

## 聊天渠道适配器

企微智能机器人使用出站 WebSocket 长连接，不要求公网回调或设置 `SIAGENT_PUBLIC_URL`。镜像已安装固定版本官方 SDK，正常构建即可使用 BotID 与 Secret；旧自建应用仍需公网 HTTPS 回调。参见 [channels.md](channels.md)。

额外可信包安装在 `<data>/channel-adapters/<package>/`，重启后发现。格式错误的包会跳过并记录安装错误；连接或适配进程异常不停止基础 Web 对话。平台不自动下载包或执行依赖安装脚本。包含安装代码和依赖的整个数据目录仍应停机备份；重建容器后会重新加载这些包。协议及限制见 [channel-adapters.md](channel-adapters.md)。

`-channel-worker` / `SIAGENT_CHANNEL_WORKER` 可独立指定内置机器人传输入口，默认 `runtime/dist/channel-wecom.js`；修改模型 `-runtime` 不改变渠道入口。Node 路径复用 `-node`。每个启用的进程适配渠道使用独立 Node 进程，应按实际渠道数量准备内存。停用后关闭对应连接和进程。

schema 8 → 9 迁移保留旧渠道、会话及出站状态。升级前停止 Go 及其所有子进程并完整备份；旧二进制不能直接打开新数据库，回退必须恢复旧完整备份。

## 应用更新与容器升级

官方安装器部署提供应用内下载、SHA-256 校验、完整停机备份、程序切换与重启；新版本未就绪时恢复旧程序及数据。Docker 和外部服务管理器不在进程内替换程序，改为显示拉取/重建或服务重启指引。下载默认 GitHub 优先、失败后镜像回退，可配置 `SIAGENT_GITHUB_MIRRORS`。备份位置、工作流程和人工恢复限制见 [application-updates.md](application-updates.md)。
