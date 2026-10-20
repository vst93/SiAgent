# 应用内更新与下载回退

“配置 → 运行与维护 → 应用更新”显示当前版本、最新版本、部署方式和更新状态。点击“检查新版本”读取 GitHub 最新公开正式 Release 的 `VERSION` 附件；草稿和预发布版本不会作为普通自动更新目标。检查不自动安装。发现新版本后同时读取该 Release 的说明，显示在“更新内容”区域；说明过长时在面板内滚动，读取失败时显示无说明提示，不影响检查和更新。

## 官方安装脚本部署

使用 `scripts/install.sh` 安装的 Linux/macOS，以及 Windows 下的 WSL 安装，可在应用内更新。安装脚本默认安装当前用户的 systemd/launchd 服务；服务模式下更新会切换 `current` 后调用服务管理器重启，不再自行启动第二个进程。安装脚本记录仓库到 `.siagent-repository`，fork 部署检查并下载原仓库的 Release。安装目录必须保留 `.siagent-install`、`versions/` 和指向当前版本的 `current`。手动解压、源码部署以及非 SiAgent 服务管理的实例仍显示手动操作提示。

1. 点击“检查新版本”查看可用版本，再点击“更新并自动重启”。这一次操作授权后续下载、停机备份和重启，下载完成后无需再次确认。关闭页面不会中断更新。
2. 下载到安装目录的独立暂存区，验证 SHA-256、私有 Node 和程序可执行性；下载期间当前应用继续服务。暂存安装固定使用 `--no-service`，不会注册或重启用户服务，也不会把服务入口和数据目录指向临时目录。
3. 校验完成后暂停新的管理写入和渠道消息，等待已接收的任务、排队、渠道入站/出站、知识处理、模型测试和会话清理结束。状态显示“等待现有任务结束”；现有任务继续执行，可取消运行中的任务，也可取消本次更新并恢复接收。持续失败或长时间重试的任务/投递需要管理员处理，不会强行中断或重放外部操作。
4. 空闲后自动关闭 Go、私有 Runtime、渠道进程和数据库。独立更新进程等待旧服务关闭存储并释放独占数据锁，将**整个数据目录**复制到 `<安装目录>/backups/update-时间-版本/`。备份按私有权限保存，包含密钥、数据库、原件与工作区。
5. 将已验证的版本移入 `versions/`，原子切换 `current`，按原启动参数、工作目录和环境启动新服务。最多等待 90 秒，并用本次启动的唯一标识验证 `/health/ready`，防止误把其他实例当作更新成功。新进程验证就绪前仍阻止业务写入和渠道接收。
6. 成功后恢复正常服务，页面自动重连并刷新。失败则停止新进程，将失败数据保留到备份旁的 `-failed-data` 目录，同时恢复旧程序与完整停机备份，再验证旧版本就绪。若恢复或旧版启动也失败，保留状态与备份，提示人工恢复，不继续循环重试。

界面只展示检查、下载校验、等待空闲、重启和结果等阶段，并用不定进度条表示操作进行中，不估算下载百分比。下载器原始输出仅用于失败诊断。

`update.log` 位于安装目录，记录更新进程及更新后服务输出，权限为 0600。数据备份可能占用较大空间，失败的备份也可能留下部分文件；确认结果后按需离线保存或手动清理，平台不自动删除备份。应用更新与手动安装脚本共用安装锁，避免同时切换版本。已授权的自动更新意图与准备状态一起保存，应用正常重启后可继续等待空闲并自动安装；旧版手动准备的更新仍可选择继续或取消。过期或已经安装的准备包会清理，不会据此降级；异常中断的下载在下次启动时清理。

建议使用独立数据目录，默认 `<安装目录>/data/`。数据目录不能位于版本或备份目录内，也不能包含安装目录，否则拒绝应用内更新。数据目录中的符号链接会按链接备份，不复制其指向的外部文件；外部 MCP 程序和系统依赖仍由管理员维护。更新不包含 Poppler 等系统软件。

更新进程独立于启动终端。服务模式下，更新进程在旧服务停止后取得数据锁，完成备份与切换，再通过 `systemctl --user restart siagent.service` 或 `launchctl kickstart -k` 拉起新版本；不再手动启动第二个进程。服务管理器无法传递环境变量，因此启动标识写入安装目录的 `.update-boot`，由新进程读取、删除并在 `/health/ready` 回传；新版本未就绪时先停止失败的服务，再恢复数据并用 `start` 拉起旧版本，避免失败进程继续占用数据锁。请勿在重连期间再次手动启动同一数据目录。停止实例请使用 `siagent service stop`。

首次发布包含更新功能的版本之前，旧程序仍需通过安装脚本升级一次，之后才能使用此入口。

## 命令行

命令本身不需要横杠；保留原有无命令启动和配置选项的兼容性，包括安装器在命令前加入的 `-data-dir`。

```sh
siagent help                 # 同样支持 -h、--help、-help
siagent version
siagent serve                # 与直接运行 siagent 相同
siagent serve -listen 127.0.0.1:8080
siagent update
siagent update -data-dir /path/to/data
siagent service install|uninstall|start|stop|restart|status
```

正在运行的实例：`update` 通过当前系统账户专用、权限 0600 的本地 Unix socket 发起同一自动更新流程，并打印阶段、等待重连及最终结果。控制目录权限为 0700，按规范化数据目录区分实例，避免长安装路径超出 Unix socket 长度限制；不使用公开 TCP 接口，不需要输入 Web 管理员密码。命令连接的是 `-data-dir` 对应的本机实例，更新沿用该实例的启动参数。关闭命令行不取消服务端更新。

未运行的安装器部署：命令取得数据锁，完成下载校验后将备份、切换与服务启动交给独立更新进程，终端返回后进程继续执行；结果见运行与维护页面或安装目录 `update.log`。自定义监听地址须在命令中指定，例如 `siagent update -listen 127.0.0.1:9090`。不能取得数据锁时不会接管正在运行的旧版实例；旧版没有本地控制入口时，使用网页更新，或先停止服务再执行命令。

Windows 继续使用安装器提供的 WSL 命令入口，`help` / `update` 参数会传入 Linux 中的程序；不新增原生 Windows 服务或二进制更新方式。Docker、源码、手动解压以及外部服务管理器部署，命令会返回对应的手动更新指引。

## Docker 与服务管理器

Docker 中不替换程序文件：替换会在容器重建时丢失，且无法更新镜像、Node 或系统依赖。界面识别 `/.dockerenv` 或 `SIAGENT_DEPLOYMENT=docker` 并显示提示，仅提供版本检查，不提供应用内下载安装。

使用镜像仓库的 Compose 部署：

```sh
docker compose pull
docker compose up -d
```

仓库默认 Compose 使用本地构建时，先更新源码，再执行：

```sh
git pull
docker compose up -d --build
```

更新前停止或等待任务，并停机备份原数据卷；始终使用原 Compose 项目及数据卷。新镜像失败时，若数据库已迁移，必须同时恢复旧镜像与升级前完整备份。

由 `siagent service install` 创建的服务会设置 `SIAGENT_SERVICE=systemd` 或 `SIAGENT_SERVICE=launchd`，这类实例支持应用内更新并重启服务。只有未声明 `SIAGENT_SERVICE`、但检测到 systemd `INVOCATION_ID` / `NOTIFY_SOCKET`、Supervisor `SUPERVISOR_ENABLED` 或 `SIAGENT_DEPLOYMENT=managed` 时，才显示由外部服务管理器停止、更新和启动的指引。其他进程管理器也应显式设置 `SIAGENT_DEPLOYMENT=managed`，避免应用内启动与管理器自动拉起冲突。

## GitHub 官方优先与镜像回退

应用版本检查、发布包下载，以及 Linux/macOS/Windows 安装脚本均先请求 GitHub 官方地址。官方地址最多尝试 3 次，失败后按顺序尝试镜像，每个镜像最多 2 次。版本/校验信息单次请求约 12–15 秒，安装包单次传输最多 120 秒，连接建立最多 5 秒；应用版本检查总期限 2 分钟，发布包准备总期限 15 分钟。某个来源耗尽重试后才转入下一个来源，不并行下载多个来源。

默认镜像前缀为：

- `https://ghproxy.net/`
- `https://gh-proxy.com/`

它们以“镜像前缀 + 完整官方 URL”的格式访问 GitHub Release 附件。可在启动服务或运行安装脚本前设置 `SIAGENT_GITHUB_MIRRORS`，使用逗号分隔的 HTTPS 前缀替换列表；设置为空字符串关闭镜像回退：

```sh
export SIAGENT_GITHUB_MIRRORS='https://your-mirror.example/'
# 或仅使用 GitHub
export SIAGENT_GITHUB_MIRRORS=''
```

Windows PowerShell：

```powershell
$env:SIAGENT_GITHUB_MIRRORS = 'https://your-mirror.example/'
# 安装器会将此设置传入 WSL 中的安装脚本
```

应用在下载状态中显示来源或错误，安装脚本在终端显示来源。镜像可用性取决于第三方服务；管理员可随时替换或禁用，不承诺这些地址永久可用。SHA-256 可以发现下载损坏，但如果安装包和校验文件都来自同一第三方镜像，它不等于发布者签名验证；有供应链来源要求的环境应使用自己信任的镜像或关闭回退。这里的回退仅用于公开更新资源，不转发模型请求、管理 Cookie、业务消息或模型密钥。

最初下载安装脚本的 `curl` / PowerShell 命令仍由当前终端执行；脚本未下载到本机时，它自身的回退逻辑尚未运行。此时可从可信镜像取得脚本，或先配置终端网络再安装。

## API 与验证

管理 API 均要求登录、CSRF 和来源校验：

- `GET /api/updates`：部署类型、版本、阶段、提示及备份路径。
- `POST /api/updates/check`：后台检查最新公开版本。
- `POST /api/updates/start`：传入 `{"confirm":true}`，服务端自动完成检查、下载、等待空闲、备份和重启。
- `POST /api/updates/prepare`：兼容旧客户端，仅后台下载并校验更新。
- `POST /api/updates/cancel`：取消已准备好或等待空闲的更新，恢复新任务接收。
- `POST /api/updates/apply`：传入 `{"confirm":true}`，检查空闲状态后停机更新。

`python3 tests/update.py artifacts/siagent-linux-amd64.tar.xz` 通过本地 HTTPS 代理模拟 GitHub Release，使用真实程序、完整私有 Runtime 和两个构建版本验证下载、停机、备份、新版启动及登录/模型密钥/会话/数据保留；不会创建或修改公开 Release。此测试已接入 CI。测试分别覆盖网页发起后不再轮询仍自动完成、命令行更新运行中实例、未运行实例更新后自动启动。浏览器验证单次操作、下载/等待/重启阶段、重复提交保护、取消入口与 320px 布局。

Go 测试同时覆盖父进程尚未回收退出记录时依然能在数据锁释放后更新，避免将僵尸进程误判为运行中的服务。Go 测试使用真实子进程验证成功重启和模拟迁移失败后的完整数据/旧程序恢复；覆盖下载超时、重试顺序、镜像回退、大小限制、准备/取消/重启恢复、认证与更新期间写入屏障。安装脚本测试通过本地 Release 和网络包装器验证官方失败后切镜像，校验失败不替换旧程序；Windows fixture 在 GitHub CI 验证 PowerShell 5.1 与 WSL 参数、重试及校验流程。真实第三方镜像的持续可用性不属于本地测试保证。

本轮 Linux、macOS、Windows PowerShell 安装器云端测试已通过：[installer CI](https://github.com/vst93/SiAgent/actions/runs/38987073602)。此结果不等同于真实 Windows 机器上的 WSL 安装和应用更新已验收。
