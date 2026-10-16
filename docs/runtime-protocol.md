# Runtime 私有协议 v1

Go 是公开入口，通过 stdin 发送逐行 JSON 请求，从 stdout 读取逐行 JSON 事件；stderr 仅用于进程日志。协议不监听端口。每帧包含 `v: 1`；有效请求的 `request_id` 以及 `run_id`、`conversation_id` 为 1–80 位字母、数字、下划线或连字符。

Go 持久化队列、配置快照、用户可见消息、工具摘要及诊断事件，并以鉴权 SSE 发布会话快照。Runtime 独占写入自己的 SQLite 上下文和执行日志，不直接写入平台数据库。

## 请求

```json
{"v":1,"type":"start","request_id":"run-1","run_id":"run-1","conversation_id":"chat-1","input":"你好","model":{"base_url":"https://api.example.com/v1","name":"example","api_key":"PRIVATE_IPC_ONLY"},"instructions":"请简洁回答","limits":{"run_timeout_ms":300000,"tool_timeout_ms":60000,"tool_rounds":10}}
{"v":1,"type":"cancel","request_id":"cancel-run-1","run_id":"run-1","reason":"timeout"}
{"v":1,"type":"status","request_id":"status-run-1","run_id":"run-1"}
```

- `model` 为提交时保存的连接快照，仅通过 IPC 传入，包装层在内存中使用；省略时使用 CLI 探针的模型环境变量。每个 Harness 使用独立 Models collection，不共享活跃模型配置。
- `instructions` 是本次运行的助手指令；不额外注入 Go 平台历史，历史上下文由 Runtime 恢复。
- `context_policy` 是冻结的 Agent 上下文策略，支持 `full`、`recent`、`time`、`summary` 及轮数、时间窗口和摘要预算；省略时兼容旧协议，使用完整历史。`output_policy` 控制思考摘要展示、工具输出级别、来源和渠道提示；Runtime 只据此决定是否发出 `thinking_delta`，其余由 Go 展示层处理。字段与范围见 [Agent 策略说明](agent-policy.md)。
- `limits` 默认运行 300,000 ms、工具 60,000 ms、工具最多 10 轮。Go 高级设置保存为秒，派发时转换为毫秒。私有测试协议允许较短的毫秒时限；管理 API 最少 1 秒。
- 可选 `probe_tool:true` 仅供探针显式开放无副作用的 `add` 和 `wait`。普通 Web 任务没有这些工具，也不开放 Shell 或任意文件工具。
- Runtime 自身最多承载 8 个活跃运行，同一会话最多 1 个；Go 默认并发 2 个并负责 FIFO 队列，绝不把同会话队列直接交给 Runtime。
- 同一 `run_id` 再次提交返回 `duplicate_run`，不会执行第二次。取消不能撤销工具已经产生的外部操作。

## 事件

| type | 说明 |
| --- | --- |
| `ready` | 恢复核对完成；`data.model_configured` 仅表示探针环境配置，不表示外部模型已验证 |
| `run_started` | 内部执行记录已建立，可能仍在打开会话 |
| `message_delta` | `data.text` 为新增文本，包含首段及结束时剩余文本 |
| `thinking_delta` | 仅开启展示时发出，`data.text` 为模型实际返回的推理文本，与回答分开保存；普通诊断只记录大小 |
| `compaction_start` / `compaction_end` | 上下文压缩生命周期，结束时 `data.failed` 表示失败；不复制摘要正文到诊断 |
| `tool_start` / `tool_end` | SDK 工具事件，携带工具名、调用标识及内部结果信息；诊断事件仅保存摘要，参数/结果作为独立业务详情按需鉴权查询 |
| `run_cancelling` | 已开始取消，当前调用可能还没停止 |
| `run_completed` / `run_failed` / `run_cancelled` / `run_interrupted` | 运行终态；成功时 `data.answer` 为本轮最终文本；可能含本轮新建的内部 `entries`，不重复传递全部历史；超过 8 MiB 时省略并设 `entries_truncated:true`，Go 不将其直接转发给 Web |
| `run_released` | Harness 和 MCP 连接/本地进程均已关闭，活跃映射已移除，此时才能复用运行位和同会话存储 |
| `cancel_ack` | 取消请求处理完成；运行终态沿用原 start 请求 ID |
| `status` | `data` 为内部执行记录，包含 run_id、conversation_id、status、seq、released；未知运行返回 null |
| `error` | 请求拒绝；`code` 为错误码 |
| `runtime_unavailable` | Go 监管层内部事件：进程退出或控制通道丢失，需中断核对 |

运行事件携带 `request_id`、`run_id`、`conversation_id` 和逐条递增的 `seq`。内部执行日志保存序号，不保存协议帧以供重放。终态和释放是两个事件；收到终态不能立即把同会话存储交给下一条任务。

失败分类 `error_code` 包含 `auth`、`quota`、`not_found`、`timeout`、`tool_timeout`、`tool_rounds`、`context_limit`、`compaction_failed`、`tool_setup`、`tool_uncertain` 和通用 `request_failed`。Go 使用固定错误文案，不向用户直接回显供应商错误原文。工具异常/模型失败不自动重跑整个任务。

请求错误包含 `invalid_json`、`invalid_request`、`invalid_run`、`invalid_limits`、`invalid_context_policy`、`invalid_output_policy`、`unknown_type`、`model_not_configured`、`busy`、`duplicate_run`、`run_not_cancellable`、`stopping`。无法解析 JSON 时没有请求 ID。

## 持久化、缺失事件和状态核对

Go 将消息、状态、last_seq 和诊断事件在一个事务中保存。重复序号无副作用；发现序号缺口时记录 event_gap，将结果标为中断并要求取消，不根据后来的成功帧推断整次任务成功。SDK 事件监听溢出会以 snapshot 替换帧；包装层遇到此情况中止当前运行，发出 run_interrupted，不把缺失事件隐去。

平台诊断仅保存文本大小、状态和工具摘要，不复制正文、工具参数、原始输出或密钥。管理员可查询最新 200 条事件；这不是重新执行模型或工具的接口。

Go 每 5 秒核对尚未释放的运行。Runtime 确认 released=1 或不存在该运行时，平台可释放占位；若缺失完整终态，则标为中断，不把 status=completed 当作可恢复的成功消息。

## 超时、停止与恢复

运行或工具超时先触发取消，并停止后续模型/工具调用；在执行已停止后发布失败终态。工具轮数上限在新一轮工具执行前拦截。上下文在调用模型前按冻结策略选择和检查；近期策略只裁剪请求中的完整旧轮次，摘要策略使用原生 compaction，持久化历史保留。压缩失败停止本次任务。

Go 对已进入取消/释放核对的运行给出 10 秒确认宽限，仍无确认时强制重启 Runtime。管道写入最多等待 2 秒，避免失去响应的子进程使调度器永久阻塞。强制退出时未知操作标为中断，不承诺撤销外部副作用。

SIGINT、SIGTERM、stdin EOF 均触发 Runtime 关闭。Go 停止派发队列，最多等待 Runtime 5 秒，再强制终止。启动时先验证平台数据库兼容性，再启动 Runtime；包装层逐个打开已有会话，在恢复调度前调用 abort({background:true})，先取消旧任务再清理。核对失败不发布 ready。

服务或 Runtime 重启后，平台把所有未完成及排队任务标为中断，清除对应加密密钥快照，不自动恢复旧队列。历史消息和工具摘要保留，用户核对后显式发起新消息。SSE 重连只重新读取已持久化平台快照。

## Skills / MCP 私有字段

`start` 额外接收 `agent_id`、`capabilities:{skills:[],mcps:[]}` 和 `mcp_secrets`。Skill 快照包含不可变导入 ID、指令和资源清单；MCP 快照包含配置版本、连接参数及允许的工具定义，敏感环境变量/请求头按服务 ID 放入 `mcp_secrets`，只经私有 IPC 传入，不合并到公开配置快照。Go 的任务凭据列加密保存模型密钥及 MCP 凭据，并在终态清除。

MCP 注册名为 `mcp_<server_id>_<sha256(original_name) 前 16 位>`，Go 在运行摘要中映射为服务名与原工具名。普通任务不开放诊断探针或通用 Shell。

`tool_start.data` 包含 `toolName`、`toolCallId`、脱敏后的 `args`；`tool_end.data` 包含相同标识、`entry.model[].isError` 摘要、脱敏文本 `result` 和 `outcome_unknown`。不再转发完整 SDK 工具事件。Go 将参数/结果限制到约 32 KB 后保存为工具业务详情，`run_events` 仍只记录名称和状态。不可确认的 MCP 调用会停止后续执行，并保留“结果不明”状态。

连接测试使用独立请求 `mcp_test`，包含 `request_id`、不含秘密的 `mcp` 配置及私有 `mcp_secret`。成功返回 `mcp_test_result`，`data.tools` 为工具名、脱敏说明、参数结构和服务声明的只读标记；失败返回固定错误码。无 `run_id` 或运行事件序号，不进入对话历史。Go 按请求 ID 路由结果并校验配置版本，防止较早的测试覆盖新配置。最多同时 4 次测试，Runtime 连接/发现期限 10 秒，Go 等待上限 12 秒。

MCP 生命周期、进程守护、数据边界与本地验证详见 [tools.md](tools.md)。

## 原生知识库私有 RPC

`start.knowledge_ids` 是 Go 从提交快照取得的知识库 ID 数组。空数组或省略时不注册知识工具；它与 Skills / MCP 配置独立。工具仅通过 `defineTool` 注册，名称为 `knowledge_search` / `knowledge_read`。

```json
{"v":1,"type":"knowledge_request","request_id":"knowledge-rpc-1","run_id":"run-1","data":{"operation":"search","input":{"query":"发票开具流程","limit":3}}}
{"v":1,"type":"knowledge_result","request_id":"knowledge-rpc-1","data":{"ok":true,"result":{"hits":[],"match_mode":"any_terms"},"error":""}}
{"v":1,"type":"knowledge_cancel","request_id":"knowledge-rpc-1","run_id":"run-1"}
```

这三类是双向控制消息，不分配运行事件 `seq`，不进入普通诊断事件。Go 依据 `run_id` 查询平台保存的运行状态和绑定快照；不信任 Runtime 或工具参数自行声明的权限。只接受仍在运行且有知识授权的任务，模型传入的知识库集合只能缩小范围。`read` 使用 `source_id`、可选 `offset` / `limit`，同样检查来源文档的知识库归属。

Runtime 按请求 ID 等待，最多 8 秒，随工具/运行取消发出 `knowledge_cancel`；迟到回复被忽略。Go 最多同时处理 32 个请求，单请求期限 10 秒；停止时取消并等待处理结束。错误以 `ok:false` 及固定或 Core 定义的安全文案回复，不返回伪造命中。

成功结果在回复前将真实来源写入 `run_sources`；这里只保存文档、版本、定位和来源链接，正文与分数不复制。鉴权会话快照新增 `sources` 数组，经 SSE 显示本轮实际来源。索引生命周期、文档内容及数据库完全由 Go 管理，Runtime 不直接打开平台 SQLite。


## 多助手派发

平台 schema 5 为会话固定 `agent_id`、为助手保存 `model_id`。同一 `start` 协议继续携带该任务提交时的助手 ID、指令、模型连接与凭据、能力快照和知识库范围，不增加另一套运行引擎。各运行的模型集合和能力注册独立；MCP 工作目录按助手 ID 区分。

停用/归档只拒绝新任务，已接收队列仍按冻结配置派发，即使助手或模型连接后来归档也不改写其快照。Runtime 不按当前管理配置重新推导权限。历史会话保留原助手归属；旧版未带模型 ID 的运行配置仍可读取。


## 企微任务的协议边界

企微回调、入站/出站、token 与发送状态全部由 Go 管理。后台接纳入站时复用 Web 的任务创建与配置冻结，仍使用同一个 `start` / 事件 / `run_released` 协议。Node 不持有企微 Secret，不直接回复成员，也不决定渠道路由。完成回复由 Go 根据权威运行终态生成持久化出站；发送不确定不会重新提交 `start`。会话永久记录 `channel_id`，Web 管理只读查看/取消任务，成员的新消息只能通过签名回调进入。


## 显式会话清理

新增 v1 控制请求 `conversation_delete`（请求 ID、conversation_id），返回 `conversation_deleted`（相同请求及会话 ID）或 `error`。该控制消息没有运行事件序号。Runtime 验证会话 ID，拒绝执行中或正在清理的会话，仅由 Runtime 删除自己的会话 SQLite/WAL/SHM 及内部 executions 记录，不触碰其他会话。

Go 先在 schema 7 的 conversation_cleanup 表持久化请求并阻止新入站，等待 Runtime 确认后原子删除平台历史及关联。确认丢失后幂等重试；单个失败不会阻塞其他清理请求。该请求不产生模型调用，也不撤销外部操作。

## 模型配置扩展

schema 8 为模型增加非敏感 `options`，入队时作为 `model_options` 冻结，派发至 `start.model.options`。支持 `api`（`openai-completions`、`openai-responses`、`anthropic-messages`）、`context_window`、`max_tokens`、`reasoning`、`thinking_level`、`temperature`、`top_p`、`max_tokens_field` 和 `display_name`。缺省兼容旧 Runtime 请求：Chat Completions、32768 上下文、4096 输出、无推理、服务默认采样。凭据仍只通过私有请求传入。

`limits.context_bytes` 是已弃用的兼容字段，不再派发或参与上下文预算。Runtime 请求前使用 SDK `estimateContextTokens` 与模型窗口、输出预留检查；Go 只检查独立的内部 8 MiB 会话文本保护，Runtime 同样限制序列化上下文为 8 MiB。资源保护返回独立的数据量错误，窗口超限为 `context_limit`；SDK 估算不等于供应商精确分词。模型元数据与请求输出上限直接传入 SDK。
