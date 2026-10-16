# 原生知识库实现设计

依据 `knowledge.md` 和 `plan.md`，复用现有 Go 权威存储、管理认证、Agent 配置快照、Runtime 工具注册和 SSE 会话展示。知识库与 Skills、MCP 同为核心能力，在管理工作台提供独立入口，不要求先配置 MCP。

## 模块与依赖

```text
管理后台 ──鉴权 API── Knowledge Core（Go：生命周期、状态、绑定、访问范围）
                            │
                            └── Provider（Search / Read）── Local Provider（SQLite FTS5）
Pi Durable 工具 ──私有 NDJSON 请求── Go 执行面（从运行快照确认范围）── Knowledge Core

可选的未来 MCP Adapter ──显式能力及结果映射── Provider
可选的未来 MCP 暴露适配 ──调用相同 Core 检索接口
```

Core 管理知识库、文档和版本、异步解析、切分、原子发布索引、删除、失败重试和 Agent 绑定。Provider 仅统一有实际使用方的检索及文档内容读取，不要求外部知识服务实现本地 CRUD。Local Provider 不导入 Pi Durable 或 MCP。

Runtime 注册 `knowledge_search` / `knowledge_read`，内部操作对应 `knowledge.search` / `knowledge.read`。OpenAI 函数名使用下划线以符合兼容接口约束。工具的知识库范围来自 Go 保存的提交快照，参数只能缩小范围；Go 再次校验，不信任模型或 Runtime 请求中声明的权限。

本版预留 Provider 的 MCP 适配位置，不凭空假设第三方 MCP 的检索工具名或返回结构。现有通用 MCP 工具仍独立可用；把它识别为知识源需要未来按具体服务增加明确的请求/结果映射。对外暴露 MCP 也通过同一 Core 接口接入，不改变原生知识库的依赖方向。

## 数据与状态

平台 SQLite 保存知识库、文档、不可变文档版本、块及位置、FTS5 索引、Agent 绑定、运行来源记录。原文件和完整提取文本在 `<data>/knowledge/<document_id>/<version>/`，临时上传在 `<data>/knowledge/staging/`。Runtime 不打开平台数据库。

文档版本经历 queued → processing → ready / failed。首次成功后才进入检索。更新单独创建版本，解析和分词在事务外进行，索引与 active_version 在单个事务中切换；更新失败继续使用旧版本。成功版本及来源定位保持稳定，重新索引创建新版本。失败版本可重试，不伪装成已完成。

删除先原子标记、解除绑定并移除索引，再删除文件。清理失败保留 cleanup_pending，后台继续清理；新检索立即排除已删除内容。历史来源链接可显示“来源已删除”。旧版本作为历史引用保留，随文档显式删除一起清理，不自动清理业务历史。

## 解析、切分与检索

支持 UTF-8 TXT、Markdown、可提取文本的 PDF。文件上限 20 MiB，提取上限 100 万字符、片段最多 5000 个、PDF 最多 1000 页；拒绝扫描件、加密或损坏 PDF、Office 和二进制文本。PDF 复用 Poppler 的 pdfinfo / pdftotext 子进程，限制时间和输出，不经 Shell；容器安装 poppler-utils。本地缺少依赖时明确报告，TXT/Markdown 及基础对话照常可用。

中文使用固定版本 gse 及嵌入词典，离线分词后写入 FTS5；英文归一化后使用相同索引。按页和解析文本段落切分，长段落采用最多 600 字符、重叠 80 字符的窗口，优先句末、换行和词间空白，保留真实页码、段落序号及字符范围。先尝试所有有效查询词，必要时退回任一查询词，返回真实 BM25 排序及实际文本片段，不提供虚构的语义相似度。管理测试使用 140/220 字符紧凑摘要，Agent 保留 600 字符摘要预算；来源读取不受摘要缩短影响。发布事务复用插入语句，已有文档重新索引时才应用新切片规则，性能小样见 [knowledge-tuning.md](knowledge-tuning.md)。

检索结果统一包含知识库/文档/版本、标题、片段、来源 ID 与可打开的链接、实际定位及可选分数。每轮真实检索的来源另存并在对话中展示；来源页面经管理员认证，按不可变版本查看，内容作为文本展示，不执行文档 HTML。

## 实施顺序与验证

1. Core / Provider 与 schema 迁移，验证原子更新、失败保留旧索引和权限范围。
2. 文档上传、后台处理、TXT/Markdown/PDF 解析与中文分词，验证中英文与异常文件。
3. 管理 API 和独立知识库工作台，验证创建、上传、状态、重试、删除与 Agent 绑定。
4. NDJSON 检索请求、运行范围校验、Pi Durable 工具和来源展示，验证未绑定工具禁用、越权参数被拒绝、引用可打开。
5. 完整进程、浏览器、重启和迁移检查，记录外部依赖与检索质量的实际边界。

MVP 不加入 Embedding、独立向量服务、重排序、复杂 RAG 工作流或通用插件框架。后续 Provider 可按真实服务扩展，保持同一 Agent 工具协议和 Core 的授权检查。


## 主要实现文件

- `internal/knowledge/provider.go`：统一请求/结果、授权范围、来源及 Provider 接口。
- `internal/knowledge/core.go`：元数据、版本、后台作业、事务发布、绑定和删除一致性。
- `internal/knowledge/parse.go`、`local.go`：文件提取、定位、切分、gse 分词、FTS5 与版本来源读取。
- `internal/platform/knowledge.go`：复用平台认证的管理 API，以及按运行快照校验的私有执行面。
- `internal/platform/store.go`、`conversations.go`、`scheduler.go`：schema 10（知识库在 4 引入）、提交绑定快照、派发及持久化来源展示。
- `runtime/src/knowledge.ts`、`index.ts`：Pi Durable 原生工具、RPC 请求/结果和取消，不加入知识库业务实现。
- `web/src/knowledge.tsx`、`main.tsx`：独立管理工作台、认证来源页及回答来源链接。
- `internal/knowledge/core_test.go`、`internal/platform/knowledge_test.go`、`migration_test.go`、Runtime/完整进程/浏览器测试：核心一致性、权限及用户闭环验证。

以上实施顺序已落地。完整操作、字段语义、测试证据和限制见 [knowledge.md](knowledge.md)。
