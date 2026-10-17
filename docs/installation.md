# 一键安装

安装脚本从 GitHub Release 下载对应平台的独立发布包，并校验 SHA-256。先尝试官方地址（最多 3 次），失败或超时后依次切换镜像（每个最多 2 次）；可通过 `SIAGENT_GITHUB_MIRRORS` 配置或禁用镜像，详见 [网络回退](application-updates.md#github-官方优先与镜像回退)。发布包自带 Node.js，用户无需安装 Node、npm 或 Go。

需先将项目变更提交并推送、发布至少一个包含安装脚本的版本，下面的 `latest/download` 地址才可使用。`latest` 选择最近的正式版；预发布版通过版本参数指定。

## Linux / macOS

```sh
curl -fsSL https://github.com/vst93/SiAgent/releases/latest/download/install.sh | sh
```

安装完成后打开新终端运行：

```sh
siagent
```

打开 <http://127.0.0.1:8080>，从启动日志复制首次设置令牌。也可直接使用安装脚本最后输出的完整命令路径，无需打开新终端。

支持 Linux/macOS 的 amd64 和 arm64。需要 curl、tar 以及 sha256sum 或 shasum；Linux 另需 xz，Debian/Ubuntu 可通过 `sudo apt-get install curl xz-utils` 安装。Linux 需 glibc，建议 Ubuntu 22.04 / Debian 12 或更新版本；macOS 建议 15 或更新版本，发布包目前未签名或公证。

默认路径：

| 内容 | 位置 |
| --- | --- |
| 命令 | `~/.local/bin/siagent` |
| 程序版本 | `${XDG_DATA_HOME:-~/.local/share}/siagent/versions/` |
| 当前版本链接 | `${XDG_DATA_HOME:-~/.local/share}/siagent/current` |
| 持久数据 | `${XDG_DATA_HOME:-~/.local/share}/siagent/data/` |

默认不需要 sudo。脚本按当前 Shell 更新 PATH 配置，新终端生效。指定版本或自定义目录：

```sh
curl -fsSL https://github.com/vst93/SiAgent/releases/latest/download/install.sh \
  -o install-siagent.sh
sh install-siagent.sh --version v0.1.0

# 自定义安装目录与命令目录；运行用户需要目录读写权限。
sh install-siagent.sh --prefix "$HOME/Applications/SiAgent" --bin-dir "$HOME/bin"

# 仅安装命令链接，不修改 Shell 配置。
sh install-siagent.sh --no-modify-path
```

`--repo owner/repo` 可指定发布包所在的 GitHub 仓库，适用于 fork。安装锁避免并发修改；校验失败不会切换当前版本或修改已有数据。重复安装相同版本会复用已安装的版本目录。

## Windows

**当前通过 WSL 运行 Linux 发布包，没有原生 Windows 二进制。** PowerShell 安装脚本在 WSL 内安装应用，并创建 Windows 的 `siagent.cmd` 命令。需要支持 WSL 的 Windows 10/11，默认使用名为 `Ubuntu` 的发行版。

已有初始化完成的 Ubuntu WSL 时，在 PowerShell 执行：

```powershell
Invoke-RestMethod https://github.com/vst93/SiAgent/releases/latest/download/install.ps1 | Invoke-Expression
```

新终端运行 `siagent`，通过 Windows 浏览器打开 <http://127.0.0.1:8080>。命令入口位于 `%LOCALAPPDATA%\SiAgent\bin\siagent.cmd`，数据在 WSL 用户的 Linux 主目录中保存。

首次使用 WSL：在管理员 PowerShell 中执行 `wsl --install -d Ubuntu`，按提示重启后打开 Ubuntu，完成 Linux 用户创建，再执行上面的一键安装命令。也可下载脚本并使用引导参数：

```powershell
Invoke-WebRequest -UseBasicParsing https://github.com/vst93/SiAgent/releases/latest/download/install.ps1 -OutFile install-siagent.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File .\install-siagent.ps1 -InstallWSL
```

WSL 安装可能需要重启，完成初始化后重新运行脚本，不带 `-InstallWSL`。该参数只引导安装 WSL，不会跳过发行版首次用户设置。

指定版本和发行版：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install-siagent.ps1 -Version v0.1.0 -Distribution Ubuntu
```

还支持 `-Repository owner/repo` 与 `-NoModifyPath`。Windows 命令将参数传递给 WSL，因此 `-data-dir` 等路径参数应使用 Linux 路径，例如 `/home/alice/siagent-data`。

## 启动、数据与升级

安装完成后手动运行 `siagent`。它默认仅监听本机，不注册系统服务。需要开机启动时，可由 systemd、launchd 或 WSL 内的服务管理器管理安装后的命令。

运行参数与原程序相同，例如：

```sh
siagent -listen 127.0.0.1:8080
siagent -data-dir /srv/siagent-data
```

Linux/macOS 还可用 `SIAGENT_DATA_DIR=/srv/siagent-data siagent` 指定数据目录。安装命令提供的默认数据目录位于版本目录之外，升级不会换到新的空数据目录。

优先使用“配置 → 运行与维护 → 应用更新”：后台下载校验后，确认停机备份，自动切换并重启；新版本失败会恢复旧程序与完整数据。Docker 与外部服务管理器显示相应操作指引，详见 [应用更新、网络回退和恢复](application-updates.md)。

手动升级时，停止完整服务并备份数据，然后重新执行安装脚本，最后手动启动。旧版本目录会保留，已运行的进程不会被安装脚本重启。数据库迁移后的回退仍需恢复升级前完整备份，不能仅切换旧程序。备份与恢复见 [deployment.md](deployment.md)。

PDF 处理另需 Poppler；外部 MCP 命令和 Skill 脚本仍需各自环境。Windows 通过 WSL 使用这些依赖时，应安装在对应的 Linux 发行版内。

## 安装脚本验证

`python3 tests/install.py` 使用本地 Release 服务和隔离的 HOME，验证平台/版本选择、最新版本重定向、下载校验、重复安装、升级保留数据、包含空格/单引号的路径与失败安装保护。

GitHub 的 `installers` workflow 在 Linux/macOS 上运行上述测试，并在 Windows PowerShell 5.1 下使用 WSL 测试替身验证脚本、下载校验、LF 输入流及 Windows 命令入口。替身测试不代表真实 WSL 安装或重启过程已验收；真实 Windows/WSL 和 macOS 的云端结果需在推送后确认。Release workflow 会等待安装脚本测试成功，再发布压缩包、两份安装脚本及统一的 `SHA256SUMS`。
