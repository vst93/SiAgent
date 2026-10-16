# Pi Durable 技术验证记录

## 依赖确认

| 项目 | 固定选择 |
| --- | --- |
| 项目地址 | https://github.com/earendil-works/pi/tree/main/packages/durable |
| 包名 / 版本 | `@earendil-works/pi-durable@1.3.0` |
| MCP 依赖 | `@modelcontextprotocol/sdk@1.32.1`，MIT，固定 lockfile |
| 配套依赖 | `@earendil-works/pi-ai@1.3.0`、`@earendil-works/chord@1.3.0` |
| 许可证 | MIT（上述三个包的 npm metadata） |
| 接入 | Node.js 子进程使用 SDK；Go 只通过 NDJSON 通信 |
| 持久化 | `openNodeSqliteStorage`；`<data>/runtime/conversations/<id>.sqlite` |
| 限制 | 上游标记 Experimental，API 可能变化；固定版本和 lockfile，升级前重新运行故障验证 |

资料依据：该固定版本 npm 包内 README、发布的类型定义和实现，以及上游示例 `16-real-model.ts`、`13-recovery.ts` 和 durable specification。生产代码不依赖 Pi TUI 或 coding-agent CLI，不加载通用编码工具。

## 已运行的本地验证

执行 `make test`，真实加载上述固定版本 SDK，模型边界使用本地 HTTP SSE 测试服务。测试模型只用于确定性地产生文本、工具请求和挂起请求，不代表已联调任何外部供应商。

| 场景 | 本地结果 | 实现 / 证据 |
| --- | --- | --- |
| 一次问答 | 通过 | 提交 input，模型流转为事件，最终 `run_completed`，会话包含 user/assistant |
| 一次工具调用 | 通过 | 模型请求 `add(2,3)`，真实 SDK 调用工具、写结果，再请求模型得到 `sum=5` |
| 工具默认禁用 | 通过 | 未传 `probe_tool` 且未显式关联能力时运行 tools 为空；关联后也仅注册所选工具 |
| 重复 run | 通过 | 同一标识返回 `duplicate_run`，不提交第二次模型请求 |
| 历史恢复 | 通过 | 进程关闭再启动，新运行的模型上下文仍包含旧回答及工具结果 |
| 模型请求取消 | 通过 | 模型流挂起且已产生 partial 时取消，进入 cancelling 后以 cancelled 结束，不发起后续请求 |
| 模型请求中 SIGKILL | 通过 | 重启后状态为 interrupted；模型请求总数未增加；后续显式发起新运行时可继续使用历史 |
| 工具副作用后、结果提交前 SIGKILL | 通过（SDK 级进程验证） | 专用测试进程工具写入一次本地 effect，然后挂起；恢复执行与包装层相同的 abort-before-schedule 操作，不再次写 effect，也不发出后续模型请求 |
| 运行超时 | 通过 | 挂起模型请求按时取消，失败原因 timeout；资源释放后可继续显式发起同会话运行 |
| 工具超时 | 通过 | wait 探针超过单工具时限后停止，失败原因 tool_timeout，不发出下一次模型请求 |
| 工具轮数上限 | 通过 | 循环请求工具时，允许的两轮执行后停止，不执行第三轮工具 |
| 上下文保护 | 通过 | 超过模型 Token 预算时不联系模型服务，输入仍保留；超过旧字节上限且在模型预算内的输入可请求；内部资源保护独立检查 |
| FIFO / 并发 / 队列容量 | 通过（完整进程 E2E） | 同会话三条任务按序执行，其他会话可并发；排队溢出明确拒绝、排队取消不发送模型请求 |
| Runtime 实际 SIGKILL | 通过（完整进程 E2E） | Go 监管的 Node 进程被终止，活跃及排队任务均中断；重启后没有自动重放 |
| 平台迁移 1/2 → 3 | 通过 | 保留原管理员哈希、模型密钥及其版本、用户消息、已有部分回答和工具历史 |
| 协议错误 | 通过 | 非 JSON、null、路径穿越标识返回错误，包装层继续服务 |
| Go 监管 | 通过 | Node 子进程异常退出后退避重启，重新就绪，取消上下文后可关闭；Go race 检查通过 |

测试源：`runtime/test/runtime.test.mjs`、`runtime/test/tool-crash-worker.mjs`、`internal/runtime/supervisor_test.go`。工具崩溃测试使用专用 SDK 测试进程，生产包装层只在显式探针模式提供无副作用的 add / wait 工具；阶段四新增经管理员逐项授权的 Skills / MCP 工具。

## 实际恢复边界

- **历史**：本地 SQLite 可以在正常退出和进程强制退出后重新读取。流式内容只保证上游已提交的部分；未提交的末尾内容可能丢失。未模拟主机断电或磁盘损坏。
- **模型请求**：上游有任务恢复和重试机制，包装层默认先取消旧任务；不承诺恢复 HTTP 请求或补回漏掉的协议事件。测试确认重启不重新发送挂起的模型请求。
- **工具执行**：不能撤销外部副作用，也不能根据 Runtime 是否已保存结果推断外部操作是否成功。测试确认 abort-before-schedule 不重放已发生的测试 effect。未验证任意第三方工具或每种外部系统的幂等性。
- **最终状态**：崩溃时仍为 running/cancelling 的执行记录保守标为 interrupted；即使 Runtime 实际已经完成但包装层尚未写入终态，也不推断成功。
- **平台历史**：阶段二新增 Go 权威平台消息及运行记录，Runtime 内部历史仍独立保存。完整进程端到端测试确认 Go 重启后历史可读，继续运行时 Runtime 自动使用旧上下文；Go 不重复注入平台历史。阶段三已新增持久化 FIFO 队列、按序号幂等保存的事件摘要和工具状态、资源释放确认以及诊断查看；正文和工具原始输出不复制到诊断日志。

## 模型、Skills、MCP、上下文压缩

| 能力 | 上游情况与当前结论 |
| --- | --- |
| 模型 | pi-ai 可注册 OpenAI Chat Completions provider；文本流、工具往返、取消已通过本地协议测试。**真实外部模型未验证**，未公布供应商兼容名单 |
| Skills | Go 导入并校验本地 SKILL.md，展示依赖/脚本资源；选中指令通过 Durable configure 注入，资源通过 defineTool 限定读取。已验证未选中 Skill 和越界路径不可访问；不自动执行脚本 |
| MCP | 通过固定 MCP SDK 连接，经 defineTool 注册所选工具；stdio 精确环境、工作区、取消/超时、未知结果、Runtime SIGKILL 进程清理及 Streamable HTTP 初始化/认证/发现/调用/取消/会话关闭已在本地验证，见 [tools.md](tools.md)。未验证第三方产品，不开放默认 Shell |
| 知识库 | defineTool 注册 knowledge_search / knowledge_read，私有 RPC 请求 Go Core；验证未绑定不注册、取消及迟到回复；完整 Go/Node 和浏览器验证实际本地索引、运行权限、来源、更新与重启。知识库不依赖 MCP，见 [knowledge.md](knowledge.md) |
| 上下文压缩 | 上游提供 compact 和自动 compaction API；**尚未完成本项目的行为验证**，包装层关闭自动压缩，不承诺长对话。当前按模型窗口与输出预留执行 SDK Token 估算检查，超限提示新会话，并保留独立内部资源保护；不静默裁剪历史 |

## 尚未完成的阶段一验收

没有使用真实模型服务和测试企业凭据；当前不能宣布阶段一全部验收通过。阶段二至五本地功能已继续开发，并增加 Web 配置、队列/超时和完整进程端到端验证；外部依赖验收仍未完成。正式对外宣布兼容前，需要按 README 的外部模型探针完成真实问答和工具调用，记录供应商、模型及结果，继续检查失败、取消和持久化行为。企业微信真实验证单独见 `wecom-validation.md`。

Skills 与 MCP 的本地实现及剩余边界见 [tools.md](tools.md)。后续压缩或其他能力若不满足计划，应先修订对应接入方案；不得用另造 Agent 引擎替代本项目的薄包装边界。
