# 集成 API（/v1）

面向业务系统的机器对机器接口，与管理后台的 Cookie/CSRF 认证完全分离。所有请求使用 `Authorization: Bearer <key>`，密钥在“配置 → 集成接口”创建，只保存 SHA-256 哈希，完整密钥只显示一次。

## 密钥与作用域

| 作用域 | 能力 |
| --- | --- |
| `agent:run` | 发起任务、取消任务 |
| `agent:read` | 读取任务、SSE 事件 |
| `knowledge:write` | 推送、替换、删除文档 |
| `knowledge:search` | 列出知识库、检索、读取来源 |
| `webhook:manage` | 管理 Webhook（见后续版本） |

密钥可以限定到单个 Agent 或单个知识库；限定后访问其他资源返回 403。另有每分钟请求上限（1–6000）与可选有效期；吊销立即生效。

## Agent 任务

```text
POST /v1/agents/{agent_id}/runs
GET  /v1/runs/{run_id}
GET  /v1/runs/{run_id}/events
POST /v1/runs/{run_id}/cancel
```

创建任务默认异步，返回 `202`：

```json
{
  "run_id": "…",
  "conversation_id": "…",
  "status": "queued",
  "agent_revision": 3
}
```

请求字段：

| 字段 | 说明 |
| --- | --- |
| `input` | 1–100000 字节文本，必填 |
| `external_conversation_id` | 调用方会话标识，映射到内部会话；省略则新建 |
| `agent_revision` | 可选。与当前配置不一致时返回 409，用于钉住行为 |
| `metadata` | 可选，保留给调用方 |

幂等：请求头 `Idempotency-Key` 在密钥范围内唯一。相同键与相同内容返回已有任务（200）；相同键与不同内容返回 409。

任务视图包含 `status`、`error`、`answer`、`evidence_state`、`citation_valid` 与 `sources`。终态为 `completed`、`failed`、`cancelled`、`interrupted`。

SSE（`/events`）发送 `run.state`、`message.delta` 与 `run.settled` 事件；`message.delta` 是当前回答文本的增量，断线重连后从最新文本继续，不做历史事件回放。

## 知识库

```text
GET    /v1/knowledge/bases
POST   /v1/knowledge/{base_id}/documents?external_id=…
GET    /v1/knowledge/documents/{document_id}
DELETE /v1/knowledge/documents/{document_id}
POST   /v1/knowledge/{base_id}/search
GET    /v1/knowledge/sources/{source_id}
```

- 上传使用 multipart，字段名 `file`；`external_id` 在知识库内唯一，重复推送会创建新版本并原子切换。
- 上传返回 `202` 与 `document_id`，处理状态通过文档接口查询（`queued`、`processing`、`ready`、`failed`）。
- 检索返回真实片段、来源链接与定位；无结果时同时记录内容缺口。
- 删除立即退出新检索，历史来源显示删除标记。

## 错误与限制

统一错误体：`{"error":"说明"}`，HTTP 状态区分 400/401/403/404/409/413/429/503。429 带 `Retry-After`。所有写操作写入审计，不记录正文与密钥。
