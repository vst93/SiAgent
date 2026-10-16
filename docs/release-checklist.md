# 发布检查

当前可以作为本地验证过的内部候选版本，**尚未满足正式发布验收**。真实企业微信、真实模型/第三方 MCP 兼容性及 Docker 实际运行仍缺环境证据；不能用协议测试服务或 CI 配置文件代替实际运行结果。

## 已有本地证据

- [x] `make install` / `make build`，固定 Runtime SDK 及 lockfile 依赖，前端由 Go 嵌入。
- [x] `make test`：19 项 Runtime 测试及全部 Go 竞态检查，包含真实 Node/Pi Durable 的企微本地完整链路。
- [x] `go vet ./...` 及 Linux amd64 `CGO_ENABLED=0` 交叉编译。
- [x] `python3 tests/e2e.py`：首次配置、认证、真实 Runtime、排队/取消/超时、中断不重放、MCP 清理、知识库及多助手。
- [x] 同一进程测试完成整个 Go/Node 停机、完整归档、逐文件 SHA-256 核对、新目录恢复、原账户新登录、原密钥/文件/来源恢复和原 Runtime 上下文续聊。
- [x] Chromium 桌面/390px 流程、CSP、安全文本、配置与密钥脱敏、知识库和助手隔离、独立加密企微 URL 验证。
- [x] schema 1–9 → 10 保留原配置、密钥、认证、历史及知识数据；主密钥缺失/不匹配及未来数据库版本拒绝启动。
- [x] [部署与恢复](deployment.md)、[企微接入](wecom.md)、[Runtime 边界](runtime-validation.md) 已提供操作与回退说明。

## 本轮维护补齐

- [x] 管理员明确确认的会话/历史清理，资源释放保护、Runtime 删除确认、重启重试、失败公平轮转及企微去重墓碑。
- [x] 鉴权磁盘用量/可用量/部分统计/低空间提示和待清理计数，不跟随符号链接。
- [x] Compose 默认日志轮转配置，回调代理避免记录加密 URL 查询。
- [x] Runtime 实际上下文文件与执行记录删除、Go 权限/状态/恢复/去重测试、完整进程和 Chromium 删除/磁盘验证。

全部功能与保留边界见 [feature-status.md](feature-status.md)。

## 自动化检查配置

[.github/workflows/check.yml](../.github/workflows/check.yml) 已配置 Ubuntu 上的 Go 1.26、Node 24、Python 3.12、Poppler 和 Chromium，执行构建、竞态、进程 E2E、浏览器和 vet，再构建容器，通过 `tests/container.py` 验证镜像 HEALTHCHECK、UID 1000、数据根目录 0700、Poppler、真实 Runtime 对话、跨容器重建及跨命名卷完整备份恢复。

共享验收流程已通过 `python3 tests/container.py --local` 在真实 Go/Node 进程上的配置、对话、完整归档、逐文件校验、新目录恢复和上下文续聊检查。此模式不执行 Docker，也不验证容器权限或网络。

Docker 环境可运行 `make test-container`，或在已有 `siagent:check` 镜像时运行 `python3 tests/container.py --image siagent:check`。脚本使用独立内部网络、本地模型协议服务及两份临时命名卷，结束后清理本次创建的资源，不访问现有业务数据卷。

该工作流尚未提交或在云端运行；本机没有 Docker，容器检查仍待实际执行。待 CI 有运行记录后，将链接、构建摘要及失败修复记录填入本清单。

## 正式发布前仍需补齐

| 检查 | 当前状态 | 需要的证据 |
| --- | --- | --- |
| 干净 Linux 构建/运行 | 已交叉编译，CI 待运行 | 新环境安装、启动、登录与基本对话 |
| Docker 构建及真实持久化卷 | 未执行 | 构建成功、健康检查、非 root、重启后数据保留、卷备份/恢复 |
| 真实模型服务 | 未执行 | 指定供应商和版本下文本、工具、取消、错误处理及脱敏记录 |
| 第三方 MCP | 未执行 | 指定 stdio/HTTP 产品版本、工具授权和结果不明处理 |
| 企业微信实际收发 | 未执行 | [wecom-validation.md](wecom-validation.md) 的官方回调、成员实际收到回复等证据 |
| 发行物和版本 | 尚未发布 | 可复现源码/二进制/镜像标识、依赖摘要、校验和及迁移说明 |

本地恢复证明同版本、同宿主环境的新目录可恢复；不能据此声明不同操作系统、外部 MCP 路径或真实企业网络可直接迁移。

## 操作顺序

1. 在待发布版本运行 `make build`、`make test`、`python3 tests/e2e.py`、`npm --prefix web run test:browser`、`go vet ./...`。
2. 在 Docker 环境执行工作流等价检查，并实际验证命名卷备份/恢复。
3. 对准备对外宣称兼容的模型、MCP 和企业微信场景补齐真实验收，保留脱敏证据。
4. 停机保存完整数据备份和原版本，验证隔离副本升级；发布说明列明 schema 10 和保守恢复语义。
5. 记录应用及依赖版本、镜像摘要和归档 SHA-256，再制作发行物。正式切换后检查就绪、登录、原历史和一条真实业务链路。

回退始终停止新版本，再使用旧版本与升级前完整备份恢复至空目录；不允许只回退单个 SQLite 文件或带升级后的数据直接运行旧二进制。
