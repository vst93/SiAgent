# 独立服务器发布包与 GitHub 发版

SiAgent 发布包包含 Go 程序、固定版本的私有 Node.js、Runtime JS 和 npm 生产依赖。目标服务器无需安装 Node、npm、Go 或前端构建工具。Go 程序直接监管私有 Node 子进程，无需启动脚本。

`make build` 仍用于源码开发，只生成 Go 程序与编译资源；跨服务器部署使用 `make package` 或下载 GitHub Release 的完整压缩包。

## 安装脚本

推荐通过 Release 提供的 `install.sh`（Linux/macOS）或 `install.ps1`（Windows，经 WSL）安装。脚本负责平台选择、下载校验、命令入口和独立数据目录，支持指定版本与升级；使用方法见 [installation.md](installation.md)。原生 Windows 二进制仍未支持。

## 下载与运行

选择与目标机器匹配的 `siagent-<os>-<arch>.tar.xz`，支持 Linux/macOS 的 amd64、arm64。GitHub Release 同时提供 `SHA256SUMS`；将压缩包和校验文件放到同一目录，可在 Linux 执行：

```sh
sha256sum --ignore-missing -c SHA256SUMS
tar -xf siagent-linux-amd64.tar.xz
./siagent-linux-amd64/siagent -listen 127.0.0.1:8080
```

解压工具需支持 XZ；最小化 Linux 系统可安装 `xz-utils`，macOS 自带的 tar 支持该格式。macOS 可执行 `shasum -a 256 siagent-darwin-arm64.tar.xz` 并与 `SHA256SUMS` 中对应条目比较。macOS 发布包目前未做 Developer ID 签名和公证，受系统 Gatekeeper 策略约束。

发布包结构：

```text
siagent-linux-amd64/
  siagent
  libexec/node
  runtime/
    dist/
    node_modules/
    package.json
    package-lock.json
  licenses/node-LICENSE
  NODE_VERSION
  LICENSE
  README.md
```

完整解压，不单独移动 Go 二进制。Go 根据自身可执行文件的位置查找 Node、模型 Runtime 和渠道入口，支持从任意工作目录启动，也支持通过符号链接调用。默认数据目录为解压目录内的 `data/`；升级时建议指定独立持久目录：

```sh
SIAGENT_DATA_DIR=/var/lib/siagent /opt/siagent/siagent -listen 127.0.0.1:8080
```

运行用户需有权创建和读写数据目录。首次启动使用日志中的一次性令牌设置管理员密码。停止 Go 主进程时，会一并关闭其监管的 Runtime 和渠道进程。

参数优先级：命令行参数 > 环境变量 > 默认值。`SIAGENT_NODE` / `-node` 可覆盖私有 Node，`-runtime` 可覆盖模型入口，`SIAGENT_CHANNEL_WORKER` / `-channel-worker` 可覆盖内置渠道入口。源码开发仍默认使用 PATH 中的 `node` 和项目相对路径。

发布包不是静态链接的单文件程序，仍受所带 Node 二进制的系统兼容性限制。Linux 使用 glibc，建议 Ubuntu 22.04 / Debian 12 或更新版本；不支持直接在 Alpine/musl 上运行。macOS 使用 GitHub 对应原生 runner 构建，建议 macOS 15 或更新版本，旧系统尚未验证。PDF 处理另需安装 Poppler 的 `pdfinfo` 和 `pdftotext`；用户配置的外部 MCP 命令与 Skill 脚本需要各自运行环境，发布包不提供 npm/npx 命令。

## 本地生成

构建机需要 Go 1.26+、Python 3.12+、npm，Node 版本见项目根目录 `.node-version`。在与目标相同的操作系统及 CPU 架构上执行：

```sh
make install
make package
python3 tests/package.py artifacts/siagent-linux-amd64.tar.xz
```

发布包位于 `artifacts/`。发布构建使用 `-trimpath -ldflags='-s -w'` 去掉 Go 调试符号，源码开发的 `make build` 保留默认构建方式。打包脚本从 Node 官网下载 `.node-version` 固定的版本，并使用仓库内 `scripts/node-checksums.json` 的 SHA-256 校验后，提取可执行文件和许可证。随后使用私有 Node 在临时目录安装锁定的 npm 生产依赖，移除用于开发的类型声明和源码映射，再以 XZ 压缩。保留执行所需的 JS、资源文件和许可证；不修改开发依赖，不包含数据和凭据。Python 标准库负责 XZ 压缩，构建机无需另装 xz 命令。

更新 Node 时需同时更新 `.node-version`、`scripts/node-checksums.json` 及 Dockerfile 中的 Node 镜像版本；校验值应取自该版本官网的 `SHASUMS256.txt`。CI 和 Release workflow 通过 `.node-version` 选择构建 Node。

迁移验证会清空 PATH，在独立临时目录解压，检查私有 Node 版本、内置渠道 SDK 加载、Runtime 就绪、内嵌前端访问、符号链接定位、环境变量/参数覆盖及正常退出。私有 Node 缺失时必须明确失败，不静默改用服务器上的 Node。

## GitHub 自动发版

工作流位于 `.github/workflows/release.yml`。推送 `v主版本.次版本.补丁版本` 标签会自动发版，例如：

```sh
git tag v0.1.0
git push origin v0.1.0
```

带预发布后缀的标签（如 `v0.1.0-rc.1`）会标为 prerelease。也可在 GitHub Actions 的 release 工作流中手动发版，版本号非必填，默认保留为草稿。留空时，读取仓库中最高的 `v主版本.次版本.补丁版本` 标签（含预发布标签的基础版本），将补丁号加 1，例如 `v1.2.3 → v1.2.4`；没有版本标签时从 `v0.0.1` 开始。填写已有标签时构建该标签的提交；填写新版本时为手动选择的分支/提交创建标签。自动标签由工作流令牌推送，不会重复触发另一轮标签发版。

四个原生构建任务分别在 Linux amd64/arm64、macOS amd64/arm64 runner 上安装锁定依赖，运行 Runtime 测试、Go 测试、go vet，构建并执行无系统 Node 的迁移启动验证。全部成功且 Linux/macOS/Windows 安装脚本测试通过后，发布任务汇总四个平台压缩包与 `install.sh`、`install.ps1`、记录发布版本的 `VERSION`，生成覆盖这些附件的 `SHA256SUMS` 和 GitHub 自动发布说明。

发布任务先创建包含全部附件的草稿，再按触发方式决定是否公开；任何构建或验证失败都不会进入发布任务。版本准备任务和发布任务拥有 `contents: write` 权限，构建与测试仅需读取。所有发版任务串行执行，避免自动版本冲突；构建与测试固定使用版本准备任务解析的提交。构建失败时已创建标签保留，重试时可明确填写该标签。已有同名 Release 时创建会失败，不覆盖已发布附件。

这些 runner 需要仓库具备相应 GitHub Actions 使用权限。工作流在推送后才会实际运行；本地测试不等同于四个平台已通过云端验收。

## 缺文件与环境排查

| 现象 | 处理 |
| --- | --- |
| `cannot use Node.js runtime` | 私有 Node 缺失、不可执行或系统不兼容；完整解压对应平台发布包。检查是否设置了 `SIAGENT_NODE` / `-node` 覆盖。 |
| `ERR_UNKNOWN_BUILTIN_MODULE`，涉及 `node:sqlite` | 检查是否覆盖到旧版 Node；该模块不能通过 npm 补装。 |
| `ERR_MODULE_NOT_FOUND` / `Cannot find package ...` | npm 生产依赖未完整部署；重新解压完整发布包，不要只复制 `dist/`。 |
| `runtime entrypoint ... is missing` | Runtime 文件缺失，或 `-runtime` 指向了无效路径。 |
| PDF 处理失败 | 安装 Poppler；其不包含在独立发布包中。 |

升级、备份和恢复前停止完整服务并备份整个数据目录，详见项目的 `docs/deployment.md`。已有 Docker / Compose 部署继续可用，镜像额外提供 Poppler。

## 应用内更新

发布包内的 `VERSION` 与 Go 构建版本由 `RELEASE_TAG` 注入（本地未设置时为 `dev`）。安装器部署可通过“配置 → 运行与维护 → 应用更新”下载、校验、停机备份及重启；新版本失败时恢复旧程序与完整数据。Docker 和外部服务管理器显示相应操作提示。网络默认官方优先、失败后镜像回退，详见 [application-updates.md](application-updates.md)。
