# SiAgent 扩展体系与对外集成 API 计划

> 状态：M1–M5 已实施并验证，见 `docs/progress.md`。
>
> 交付范围说明：M1 的“Pi 供应商生态”以可选供应商预设包实现，复用 Pi AI 的 OpenAI/Anthropic 协议适配器，不内置各厂商 SDK；Bedrock/Vertex 单独成包并要求自备兼容网关。M3 的渠道插件保留既有进程宿主，在扩展页登记为随系统分发，不重复迁移到解析扩展宿主。真实厂商 SDK 目录、渠道宿主统一和插件签名/升级通道列为后续增强。  
> 范围：扩展包体系、模型生态复用、高级工具开关、对外集成 API。  
> 不包含：Skills/MCP 改造、公开 Web 问答页、外部文档系统连接器、Pi extension 兼容。  
> 关联计划：知识效果专项（另行编写；本计划中的 embedder/reranker/parser 阶段契约以其为准）。

## 0. 决策摘要

| 编号 | 决策 | 结论 |
| --- | --- | --- |
| D1 | 复用 Pi（`@earendil-works/pi-ai`）的模型生态 | 采用；配置界面必须保持简洁 |
| D2 | 能力加载模式 | 采用；基础 3 种模型协议内置，其余能力按扩展加载 |
| D3 | Skills/MCP | 本阶段不改动 |
| D4 | 高级工具 | 做成管理员可配置的开启项 |
| D5 | 高级工具默认值 | 默认关闭；开启时给出明确风险提醒 |
| D6 | 高级工具配置粒度 | Agent 级一键开关，不提供逐工具配置 |
| D7 | 对外集成 API | 采用此前方向：API Key、异步运行、SSE、幂等、知识推送/检索 |
| D8 | 公共插件 ABI | 不直接暴露 `pi-durable` 的实验性扩展 API |
| D9 | Pi 现有插件兼容 | 暂缓；本阶段只复用 `pi-ai` provider 与 `pi-durable` 官方 tools；不引入 OpenCode |
| D10 | Pi 模型供应商打包 | 一个官方总包 + 重依赖 provider 单独包 |
| D11 | 基础 3 协议 | 长期保留，作为兼容底座 |
| D12 | 高级工具管控层级 | Agent 级单开关 + 部署级 kill switch |
| D13 | `codemode` / `tool-search` | v1 不纳入高级工具包；后续分别处理 |
| D14 | 第三方扩展签名 | v1 不强制；SHA-256 + 管理员可信来源确认；签名可选 |
| D15 | Integration API 认证 | v1 只做 API Key；认证只查 SHA-256 哈希；为管理员回看另存主密钥加密副本，默认隐藏；OIDC/JWT 后置 |
| D16 | Agent revision | 轻量版现在做：自增 revision，API 可选传入，运行快照记录 |
| D17 | 未来 Pi extension 路线 | 暂不选；若未来需要，先 A，再 B；C 仅用于 coding/automation sidecar |
| D18 | 知识效果优先级 | v1 轻量优先：不做 embedding/向量索引；查询扩展 + 多查询 BM25 + 启发式/可选 LLM 重排；embedder/reranker 降为可选扩展 |

## 1. 背景与定位

SiAgent 的定位是：

> 面向企业的、可配置的知识问答服务端；知识效果是核心；开发者可以通过扩展包增强能力；其他系统可以通过 API 接入 Agent；个人桌面版是后续分发形态。

因此本计划的优先级不是“多接渠道、多做格式”，而是：

1. 让能力可以按需加载，而不是默认堆满界面；
2. 让模型与知识能力可以复用成熟生态；
3. 让第三方系统能稳定接入 Agent 和知识库；
4. 不在现阶段改造 Skills/MCP，也不做公开 Web 访问页。

## 2. 设计原则

1. **核心稳定，能力按需加载。** 基础问答、知识库、管理员后台不依赖扩展；扩展缺失时明确提示，不静默降级。
2. **不把 Pi Durable 的实验性 API 暴露给插件作者。** 对外提供 SiAgent 自己的稳定 ABI，内部用适配层接 `pi-durable`。
3. **一个能力一个开关。** 高级工具不提供逐工具勾选，避免配置复杂化。
4. **默认安全。** 高风险能力默认关闭；开启需要确认并记录审计。
5. **插件是可信代码，不是安全沙箱。** 进程隔离、超时、资源限制、日志和启停是工程手段，不承诺隔离恶意代码。
6. **先内置、再官方扩展、最后第三方。** 不在没有真实插件需求前过度抽象。
7. **API 与管理后台分离。** 管理后台使用 Cookie/CSRF；集成 API 使用 Bearer API Key，互不复用。
8. **用标准换生态。** Skills、MCP、OpenAI-compatible 模型是跨生态标准；优先承接标准，而不是复制某个宿主的插件 API。

## 3. 扩展体系

### 3.1 三层架构

```text
SiAgent
├── L1 内置核心能力
│   ├── 基础模型协议（OpenAI Chat Completions / Responses / Anthropic）
│   ├── 基础知识库（TXT/Markdown/文本 PDF + FTS5/BM25）
│   ├── Agent、会话、队列、管理员后台
│   └── Skills / MCP（本阶段不改动）
│
├── L2 官方扩展包
│   ├── Pi 模型供应商扩展（一个总包 + 重依赖单独包）
│   ├── Embedding / Reranker
│   ├── Parser
│   ├── 高级本地工具（read/write/edit/bash）
│   └── codemode / tool-search（后续，独立处理）
│
└── L3 第三方扩展包
    ├── 独立进程 + 版本化协议
    ├── manifest / 配置 / 密钥 / 健康 / 日志
    └── 管理端安装、启停、升级、回滚
```

### 3.2 能力加载模式

采用“显式加载”，而不是“默认全量内置”：

| 状态 | 行为 |
| --- | --- |
| 未安装 | 能力不出现在界面；相关入口显示为空或引导安装 |
| 已安装未启用 | 显示为“未启用”，不可用于新任务 |
| 已启用 | 出现在配置界面，可用于新任务 |
| 加载失败 | 显示固定错误和日志入口，不静默隐藏 |

具体规则：

- **模型供应商**：基础 3 种协议始终可用；Pi 供应商总包加载后，才出现在模型连接的 provider 列表中。
- **Embedding / Reranker**：未加载时知识库只使用 BM25；界面显示“未启用语义检索”，不视为故障。
- **Parser**：未加载时只支持基础格式；其他格式给出明确提示。
- **高级工具**：未启用时 Runtime 不注册这些工具，模型上下文中也没有它们。
- **已有配置引用未加载能力**：任务在派发前明确失败，错误码为“能力未加载”，不自动切换到其他 provider/工具。

加载时机：

- 扩展在服务启动或管理员启用时加载；
- 不在每次请求时动态加载，保证行为可预测；
- 启用/停用扩展不要求重启整个服务，但可以要求重启对应扩展进程。

命名约定：

- 现有“插件接入”（Skills/MCP）本阶段不改名、不改行为；
- 新增页面统一使用“扩展”，避免与 Skills/MCP 混淆。

### 3.3 扩展类型与优先级

| 类型 | 作用 | 优先级 | 备注 |
| --- | --- | --- | --- |
| `model` | 模型供应商/网关 | P0 | 复用 `pi-ai` |
| `parser` | 文档解析 | P1 | 结构化资料；v1 优先 DOCX/Markdown/PDF |
| `embedder` | 向量化 | P2 可选 | v1 不做；由知识评测触发 |
| `reranker` | 重排 | P2 可选 | v1 用启发式 + 可选 LLM；不做 cross-encoder 服务 |
| `channel` | 聊天渠道 | P1 | 现有渠道插件正式化 |
| `tool` | 原生工具 | P2 | MCP 已覆盖大部分 |
| `retriever` / `index` | 替换检索引擎/向量库 | P2 | 谨慎开放 |
| `integration` | 自定义回调/事件 | P3 | 先由平台 API 统一处理 |

本阶段不开放“替换整条知识流水线”。知识库采用“核心编排 + 阶段扩展”：

```text
上传 → 解析(parser) → 切分(core) → 向量化(embedder)
     → 索引(core) → 检索(core: BM25 + vector)
     → 重排(reranker) → 上下文组装(core) → 回答(core)
```

阶段契约以“知识效果专项计划”为准；本计划只定义扩展类型和宿主边界。

### 3.4 扩展包格式与清单

统一使用 `plugin.json`，兼容现有渠道的 `adapter.json`。

```json
{
  "schema": "siagent.plugin/v1",
  "id": "acme.reranker",
  "name": "ACME Reranker",
  "version": "1.2.0",
  "type": "reranker",
  "api_version": "1",
  "min_host": "0.6.0",
  "max_host": "0.7.x",
  "entry": "dist/worker.mjs",
  "runtime": "node",
  "capabilities": ["rerank"],
  "config_schema": {
    "fields": [
      { "key": "endpoint", "label": "服务地址", "required": true },
      { "key": "api_key", "label": "密钥", "secret": true, "required": true }
    ]
  },
  "limits": { "timeout_ms": 30000, "max_inflight": 8 },
  "permissions": ["network:https://api.acme.com"]
}
```

原则：

- 包自包含，安装时不执行 `npm install` 或安装脚本；
- 入口必须在包内，拒绝绝对路径、`..`、符号链接；
- 配置字段先使用简单 `fields` 描述，后续再支持完整 JSON Schema；
- 密钥字段单独声明，加密保存、不回显、只走 stdin；
- `api_version` + `min_host/max_host` 做兼容性检查；
- 校验 SHA-256；签名可选，见 3.8。

### 3.5 进程模型与协议

沿用现有 `internal/channels/process.go` 的思路，抽出通用插件宿主：

- 独立进程，stdin/stdout 走版本化 NDJSON/JSON-RPC；
- 环境变量白名单，密钥只走 stdin；
- 进程组管理，异常退出清理子进程；
- 请求 ID、超时、取消、最大帧、最大 in-flight；
- stderr 保留有界环形日志，管理端可查看，日志脱敏；
- 健康检查与有限重启退避；
- 安装目录只读，插件数据目录可写，插件不能直接访问平台数据库。

协议帧：

```text
{v:1, id, method, params}
{v:1, id, result}
{v:1, id, error}
{v:1, type:"event", ...}
```

统一方法：`initialize`、`configure`、`health`、`shutdown`。  
类型方法：

- `model.chat`（流式）
- `embedder.embed`（批量）
- `reranker.rerank`
- `parser.parse`
- `channel.open/send/close`

进程隔离策略：

- 官方/内置扩展：允许进程内或共享宿主；
- 第三方扩展：默认独立进程；
- 同一插件崩溃不应导致整个 Runtime 崩溃；
- 单个插件设置并发上限、响应大小上限和超时。

### 3.6 安装、启停、升级、回滚

管理端新增独立“扩展”页面：

- 上传 `.zip` / `.tar.gz`；
- 展示 manifest、权限、配置字段、版本兼容性；
- 管理员确认可信来源；
- 安装到 `<data>/plugins/<id>/<version>/`；
- 配置、启用；
- 查看健康状态、日志、最近错误；
- 升级、回滚、卸载（可选择保留数据）。

生命周期：

```text
uploaded → validated → installed → configured → enabled → healthy
                                              ↘ degraded / failed
```

升级流程：

1. 安装新版本到独立目录；
2. 启动新实例并健康检查；
3. 切换流量/绑定；
4. 排空旧实例；
5. 失败回滚到旧版本。

参考 `internal/selfupdate` 的校验、备份、切换、失败恢复思路，不另造一套。

### 3.7 配置界面

目标：能力变多，但界面不乱。

- 模型连接采用“provider → model → 连接参数”的分组/分步布局；
- provider 列表分组：基础协议、官方扩展、自定义；
- 只展示当前 provider 需要的字段，高级参数折叠；
- 扩展未加载时，不显示空分组，只在“扩展”页提供安装入口；
- 扩展配置表单由 `config_schema` 生成，不暴露原始 JSON；
- 高风险开关集中在 Agent 的“工具授权”中，不与普通工具混排；
- 任何“未加载/未启用”状态都有明确文字说明，不使用静默隐藏。

### 3.8 安全、信任与签名

- 插件是可信代码，不是安全沙箱；
- 第三方插件默认独立进程，降低崩溃和权限影响；
- 官方/内置扩展可以在进程内，但仍需白名单和显式启用；
- 危险能力默认关闭；
- 密钥加密保存，日志脱敏；
- 管理员安装时确认来源；
- v1 校验 SHA-256，不强制签名；
- 记录安装来源、哈希、安装时间、操作者；
- 企业版提供“仅允许签名扩展”策略：
  - ed25519 签名；
  - 发布者信任列表；
  - 策略开启后，未签名包拒绝安装；
- 官方扩展默认提供签名，但不作为 v1 的安装前置条件。

## 4. 模型生态复用

### 4.1 目标

复用 `@earendil-works/pi-ai` 的 provider/model 生态，减少自研协议适配，同时保持配置界面简洁。

现状：

- `runtime/src/models.ts` 只支持 3 种 API；
- `internal/platform/model_options.go` 只允许这 3 种协议；
- Go 侧模型配置没有 `provider` 概念。

### 4.2 打包策略

采用“一个官方总包 + 重依赖 provider 单独包”：

- `siagent-model-providers`：轻量 provider 总包；
  - OpenAI、Anthropic、Google、OpenRouter、Groq、DeepSeek、Mistral、xAI、Together、Fireworks、Ollama/vLLM 自定义等；
  - provider 目录按需懒加载，未选中的 provider 不加载 SDK；
- `siagent-model-bedrock`、`siagent-model-vertex` 等：重依赖单独包；
- 不在 v1 为每个 provider 建独立安装包；
- 若后续某个 provider 依赖过大或更新节奏不同，再拆出。

基础 3 种协议长期保留：

- OpenAI Chat Completions；
- OpenAI Responses；
- Anthropic Messages；
- 它们是首次使用和已有连接的兼容底座，不迁移为扩展。

### 4.3 配置界面

- 第一层：选择 provider（分组 + 搜索 + 最近使用）；
- 第二层：选择/填写模型 ID（catalog 下拉或手动）；
- 第三层：连接参数（密钥、地址；本地 provider 可省略密钥）；
- 高级参数折叠：上下文窗口、最大输出、推理、采样、输出字段；
- 未加载扩展时只显示基础协议，不显示灰色空列表；
- 已有连接引用未加载 provider 时，显示“供应商扩展未加载”，并提供启用入口；
- 不展示原始 JSON，不把所有 provider 字段平铺。

### 4.4 数据与迁移

- `models` 增加 `provider` 字段；
- 现有连接迁移为 `provider = openai | anthropic`；
- `models.api` 保留，作为基础协议/适配器标识；
- 运行快照记录 `provider`、`model`、`agent_revision`；
- 扩展未加载时，已有连接保留，不删除配置。

### 4.5 运行时行为

- Runtime 根据 `provider` 解析适配器；
- 扩展未加载/加载失败时，任务在派发前失败，错误码 `model_provider_unavailable`；
- 不自动回退到其他 provider；
- 模型连接测试复用同一解析逻辑；
- 模型目录发现优先使用 `pi-ai` catalog，fallback 到现有 `/models` 发现。

### 4.6 验收

- 未加载 Pi 供应商扩展时，界面只显示基础协议，功能正常；
- 加载后 provider 列表出现，配置界面不杂乱；
- 选择 Ollama/vLLM 等本地 provider 可以完成问答；
- 未加载 provider 的已有连接派发任务时明确失败；
- 重依赖 provider 单独包可以独立安装/卸载；
- 现有 3 种协议和已保存连接不回归。

## 5. 高级工具开关

### 5.1 范围

v1 高级工具包只包含：

```text
read / write / edit / bash
```

不包含：

- `codemode`：后续作为独立官方扩展，单独开关；
- `tool-search`：低风险，后续可在工具很多时自动启用，不需要管理员开关；
- `images`：按需能力，不纳入本开关。

### 5.2 配置方式

- 入口：Agent 的“工具授权”中新增“高级工具”区域；
- 只有一个总开关：“允许此 Agent 使用本地工作区工具”；
- 默认关闭；
- 不提供逐工具勾选；
- 开关值随 Agent 配置保存，并在任务提交时冻结；
- 对外客服、纯知识库问答建议保持关闭。

### 5.3 部署级 kill switch

- 新增部署级硬开关，例如 `SIAGENT_DISABLE_ADVANCED_TOOLS=true`；
- 开启后，所有 Agent 的高级工具不可用，已有开关显示“已被部署策略禁用”；
- 默认不干预，只作为企业运维/安全阀；
- 不进入日常配置界面，避免增加用户理解成本。

### 5.4 开启提醒

开启时弹出确认：

- 这些工具可以读写服务器文件、执行命令；
- 工作目录约定不是操作系统沙箱；
- 只应在可信 Agent、可信用户场景开启；
- 对外客服、纯知识库问答建议保持关闭；
- 需要勾选“我了解风险”后才能保存；
- 记录审计：操作者、时间、Agent、变更前后值。

### 5.5 运行时行为

- 关闭：Runtime 不注册这些工具，模型上下文中没有它们；
- 开启：只为该 Agent 的本次运行注册工具包；
- 工作目录：`<data>/workspaces/<agent_id>/`；
- 工具调用仍受运行时限、工具时限、轮数限制；
- 工具调用记录进入现有审计/运行详情；
- 修改开关只影响新提交的任务，排队任务保持提交时快照；
- 扩展包未安装/未启用/被部署策略禁用时，开关不可用，并显示原因。

### 5.6 验收

- 默认关闭时，模型无法调用 read/write/edit/bash；
- 开启后可以调用，且仅限于该 Agent；
- 关闭后新任务不再注册工具；
- 部署级 kill switch 能覆盖所有 Agent；
- 客服/知识库场景保持关闭时无工具定义进入上下文；
- 开启/关闭有审计记录和风险确认；
- 现有 Skills/MCP 授权不回归。

## 6. 对外集成 API

### 6.1 范围

独立 `/v1` API，不复用管理员 `/api`：

```text
/api/*   管理员后台（Cookie + CSRF）
/v1/*    集成 API（Bearer API Key）
```

首批能力：

- Agent 运行：创建、查询、取消、SSE 事件；
- 知识库：推送文档、查询状态、删除、检索、读取来源；
- Webhook：运行完成/失败通知；
- 后续：MCP Server 暴露、OpenAI 兼容门面。

### 6.2 认证与权限

v1 只做 API Key：

- `Authorization: Bearer <key>`；
- 认证只使用 key hash + prefix；为管理员回看另存主密钥加密副本，列表默认隐藏，显式查看写审计；
- 支持过期、吊销、轮换；
- 作用域：`agent:run`、`agent:read`、`knowledge:write`、`knowledge:search`、`webhook:manage`；
- 作用域资源：指定 Agent / 知识库；
- 可选 IP allowlist、CORS allowlist；
- 每 key 限流、并发上限、每日配额；
- 审计：key id、动作、资源、状态、耗时、request id，不记录正文和密钥。

OIDC/JWT 后置：

- 内部保留 `principal` 抽象；
- 后续可增加用户委托访问；
- 不在 v1 混入两套认证。

### 6.3 Agent API

```text
POST   /v1/agents/{agent_id}/runs
GET    /v1/runs/{run_id}
GET    /v1/runs/{run_id}/events?since=seq
POST   /v1/runs/{run_id}/cancel
GET    /v1/conversations/{id}
POST   /v1/conversations
```

语义：

- 默认异步，返回 `202 + run_id`；
- 支持 `Idempotency-Key`；
- 支持 `external_conversation_id` 映射；
- 支持可选 `end_user_id`；
- 支持可选 `agent_revision`；不传使用最新；
- SSE 增量事件：`run.queued`、`run.started`、`message.delta`、`tool.start`、`tool.end`、`source`、`run.completed`、`run.failed`、`run.cancelled`、`run.interrupted`；
- 复用现有 `enqueueRun`、队列、快照和调度；增加来源身份。

Agent revision：

- `agents` 增加 `revision` 整数；
- 每次保存配置自增；
- 运行快照记录 `agent_revision`；
- API 可传 `agent_revision`，不传使用最新；
- 不传时行为与现有 Web 一致；
- 运行详情返回实际使用的 revision；
- v1 不做 draft/publish 双版本。

### 6.4 Knowledge API

```text
GET    /v1/knowledge/bases
POST   /v1/knowledge/{base_id}/documents
PUT    /v1/knowledge/{base_id}/documents/{doc_id}
DELETE /v1/knowledge/{base_id}/documents/{doc_id}
POST   /v1/knowledge/{base_id}/search
GET    /v1/knowledge/sources/{source_id}
```

- 采用 Push Ingestion，不做外部系统 Connector；
- `external_id` 在知识库内唯一；
- 相同 `external_id` 再次推送 = 新版本，原子替换；
- 删除软删除，立即退出检索；
- 解析、切分、索引仍由平台掌控；
- 检索返回真实片段、来源、定位和分数。

### 6.5 Webhook、MCP、兼容层

- Webhook：`run.completed` / `run.failed`，HMAC 签名、事件 ID、至少一次投递；
- MCP Server：暴露 `knowledge_search`、`knowledge_read`、`agent_chat`，供其他 Agent 调用；
- OpenAI 兼容门面：可选，`model: "agent:<id>"`，只映射基础问答，不作为主接口。

### 6.6 数据模型

新增/修改：

- `agents.revision`；
- `models.provider`；
- `api_keys`；
- `api_conversations`；
- `api_idempotency`；
- `webhook_subscriptions`；
- `webhook_deliveries`；
- `audit_log`；
- `plugins` / `plugin_configs`。

原则：增量迁移，不改变现有表的核心语义。

### 6.7 安全与审计

- `/v1` 不使用 Cookie、不走 CSRF；
- 当前 `sameOrigin` 需对 `/v1` 放行；
- API Key 认证只查 hash；加密副本仅用于管理员回看，列表不回显；
- 请求体大小限制、上传限制；
- 限流与并发上限；
- Webhook 签名与幂等；
- 审计可导出；
- 更新期间对 API 返回 503，而不是挂起。

### 6.8 验收

- 创建 key、限定 Agent/知识库、调用运行、SSE、轮询、取消；
- 幂等键重复请求返回同一 run；
- 越权 key 无法访问其他 Agent/会话；
- `agent_revision` 能钉住行为；
- 知识推送、更新、删除、检索、来源读取；
- Webhook 签名、重试、幂等；
- 审计记录完整，密钥不回显；
- 现有管理员 API 不回归。

## 7. 本阶段不做 / 不改

- 不改 Skills/MCP 的现有接入、授权和运行方式；
- 不做公开 Web 问答页；
- 不做外部文档系统 Connector；
- 不做逐工具的高级权限配置；
- 不做通用可视化工作流；
- 不做多 Agent 编排；
- 不把 `pi-durable` 的实验性 API 直接作为公共插件 ABI；
- 不引入 OpenCode 插件生态或 OpenCode Runtime；
- 不做 Pi extension 兼容，仅复用 `pi-ai` provider 与 `pi-durable` 官方 tools。

## 8. Pi 生态边界

### 8.1 本阶段采用

- `pi-ai` 的模型供应商能力；
- `pi-durable` 官方 tools/codemode/tool-search（作为可选官方扩展，本阶段只接 tools 的 read/write/edit/bash）；
- Skills 与 MCP 两个跨生态标准；
- 不加载 `pi-coding-agent` 的插件包；
- 不在 `pi-durable` 上模拟 `ExtensionAPI`；
- 不引入 `pi-coding-agent` / OpenCode 作为第二套 Agent Runtime。

### 8.2 暂缓

Pi 现有插件包的兼容暂缓，不影响当前里程碑。

原因：

- Pi 的代码型 extension 依赖 `pi-coding-agent` 的 `ExtensionAPI`、事件、命令、Session 和 TUI，和 `pi-durable` 不兼容；
- `pi-durable` 官方标注 Experimental，不适合作为公共插件 ABI；
- OpenCode / Magpie 的经验表明，真正可移植的是“供应商插件”子集，而不是整个 Agent 插件生态；
- 当前阶段更重要的是知识效果、模型生态、扩展宿主和集成 API。

### 8.3 未来路线

如果未来确实需要承接 Pi extension 生态，再单独评估：

- 路线 A：只复用能力，不复用插件包格式；
- 路线 B：兼容 Pi package 的一个子集；
- 路线 C：把 `pi-coding-agent` 作为可选 sidecar。

推荐顺序：

1. 先 A；
2. 有真实需求再 B；
3. C 只用于 coding/automation Agent，不进入知识问答核心。

## 9. 里程碑与依赖

### M1：模型生态与配置界面

- 模型连接支持 provider + model；
- 基础 3 协议保留；
- Pi 供应商总包 + 重依赖单独包；
- 配置界面分组、搜索、高级折叠；
- `models.provider` 迁移与回归测试；
- 未加载 provider 的失败语义。

### M2：高级工具开关

- Agent 级一键开关；
- 默认关闭、风险确认、审计；
- 部署级 kill switch；
- Runtime 按 Agent 注册 read/write/edit/bash；
- 关闭时工具不进入上下文；
- 验收与文档。

### M3：扩展宿主与官方扩展

- 通用 plugin host；
- manifest / 配置 / 密钥 / 健康 / 日志；
- 管理端“扩展”页面；
- 渠道插件保留现有进程宿主，在扩展页登记为随系统分发；
- 解析扩展使用新宿主；先接 model、parser 官方扩展；
- embedder/reranker 为可选扩展，后置，由知识评测触发；
- 阶段契约以知识效果专项计划为准。

### M4：Integration API v1

- API Key、scope、审计、限流；
- Agent run（异步、SSE、幂等、取消、agent_revision）；
- Knowledge push/search；
- 数据模型与迁移；
- OpenAPI 文档。

### M5：Webhook、MCP Server、兼容层

- Webhook 签名与重试；
- MCP Server 暴露；
- OpenAI 兼容门面（可选）。

### M6（暂缓）：Pi 现有插件兼容

- 本阶段不实施；
- 后续有明确需求时，再按第 8.3 节评估。

### 依赖与建议顺序

```text
知识效果专项 plan
        │
        ├── 影响 M3 的阶段契约
        │
M1 模型生态 ──┬── M3 扩展宿主
              │
M2 高级工具 ──┘
              │
              └── M4 Integration API ── M5 Webhook/MCP
```

建议：

- 先写知识效果专项 plan；
- M1 与 M2 可以并行；
- M3 在知识阶段契约定稿后做；
- M4 可以在 M1/M2 稳定后并行推进；
- M5 最后；
- M6 不排期。

## 10. 风险

| 风险 | 影响 | 应对 |
| --- | --- | --- |
| `pi-durable` 实验性 API 变更 | 插件/适配层破坏 | 自建稳定 ABI，adapter 隔离 |
| Pi 供应商扩展包过大 | 启动/构建变慢 | 按 provider 懒加载，重依赖拆包 |
| 高级工具被误开 | 文件/命令风险 | 默认关闭、风险确认、部署级 kill switch、审计 |
| 扩展质量参差 | 知识效果下降 | 评测台、官方认证、默认实现优先 |
| API Key 泄漏 | 越权调用 | 认证只查 hash；加密副本只允许管理员显式查看并审计；scope、限流、轮换 |
| 流式事件不可回放 | 断线体验差 | 实时 hub + 快照恢复，不存全量 delta |
| 扩展配置界面膨胀 | 用户困惑 | 分组、按需加载、高级折叠 |
| 签名强制导致冷启动难 | 生态起不来 | v1 不强制签名，企业版可开策略 |

## 11. 剩余实施选择（非阻塞）

这些是工程实现选择，不改变上面的产品决策：

1. 第三方插件默认“一插件一进程”，还是同类型共享宿主？
2. 官方扩展是否允许进程内加载，边界如何划分？
3. 模型供应商总包的具体 provider 清单和拆包边界；
4. 重依赖 provider 的懒加载实现；
5. 部署级 kill switch 用环境变量还是配置文件；
6. 签名算法、发布者信任列表和密钥轮换方式；
7. API 限流的具体阈值和配额模型；
8. SSE 事件的精确字段和重连语义；
9. `agent_revision` 是否在管理端展示；
10. 知识流水线阶段契约（由知识效果专项计划确定；v1 不含 embedder/reranker）；
11. 扩展日志保留时长和脱敏规则；
12. 插件健康检查频率和重启退避参数。
