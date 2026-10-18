import { MessageMarkdown } from "./message-markdown";
import { appURL } from "./urls";

const GITHUB_DOC =
  "https://github.com/vst93/SiAgent/blob/main/docs/integration-api.md";

const content = `# SiAgent 集成接口

面向业务系统的机器对机器接口。使用 \`Authorization: Bearer <key>\`，与管理员后台的 Cookie 登录完全分离。

## 快速开始

1. 在“集成接口”创建密钥，选择作用域；
2. 默认限定到需要的 Agent 或知识库；
3. 调用 \`/v1\` 接口，或从 OpenAPI 生成客户端。

\`\`\`bash
curl -sS ${appURL("/v1/agents/default/runs")} \\
  -H "Authorization: Bearer siagent_xxx" \\
  -H "Content-Type: application/json" \\
  -H "Idempotency-Key: ticket-1001" \\
  -d '{"input":"出差住宿标准是多少？","external_conversation_id":"user-42"}'
\`\`\`

## 作用域

| 作用域 | 能力 |
| --- | --- |
| \`agent:run\` | 发起任务、取消任务 |
| \`agent:read\` | 查询任务、SSE 事件 |
| \`knowledge:write\` | 推送、替换、删除文档 |
| \`knowledge:search\` | 列出知识库、检索、读取来源 |
| \`webhook:manage\` | 管理 Webhook |

密钥可以限定到单个 Agent 或知识库；另有每分钟限额与有效期。完整密钥加密保存，默认隐藏，管理员可在列表中点击“查看”并写入审计。

## Agent 任务

- \`POST /v1/agents/{agent_id}/runs\`：异步创建，返回 \`run_id\`；
- \`GET /v1/runs/{run_id}\`：查询状态、回答、来源、证据状态；
- \`GET /v1/runs/{run_id}/events\`：SSE，发送 \`run.state\`、\`message.delta\`、\`run.settled\`；
- \`POST /v1/runs/{run_id}/cancel\`：取消排队或执行中的任务。

支持 \`Idempotency-Key\`、\`external_conversation_id\` 和 \`agent_revision\`。同一幂等键与相同内容返回已有任务；不同内容返回 409。

## 知识库

- \`GET /v1/knowledge/bases\`
- \`POST /v1/knowledge/{base_id}/documents?external_id=...\`：multipart，字段 \`file\`
- \`GET /v1/knowledge/documents/{document_id}\`
- \`DELETE /v1/knowledge/documents/{document_id}\`
- \`POST /v1/knowledge/{base_id}/search\`
- \`GET /v1/knowledge/sources/{source_id}\`

\`external_id\` 在知识库内唯一，重复推送会创建新版本并原子切换。

## Webhook

终态事件 \`run.completed\`、\`run.failed\`、\`run.cancelled\`、\`run.interrupted\` 会推送到配置地址。请求带：

- \`X-SiAgent-Event\`
- \`X-SiAgent-Event-Id\`
- \`X-SiAgent-Timestamp\`
- \`X-SiAgent-Signature\`：HMAC-SHA256，签名内容为 \`event_id + "." + event + "." + timestamp + "." + body\`

失败按 0/2/5 秒重试 3 次；接收方应按事件 ID 幂等。

## MCP 与其他

- \`POST /mcp\`：最小 MCP JSON-RPC 服务，暴露 \`knowledge_search\`、\`knowledge_read\`、\`agent_chat\`；
- \`POST /v1/chat/completions\`：OpenAI 兼容门面，\`model\` 使用 \`agent:<id>\`，仅非流式基础问答；
- \`GET /v1/openapi.yaml\`：机器可读接口规范。

## 文档与源码

- [完整接口说明（GitHub）](${GITHUB_DOC})
- [OpenAPI 规范](${appURL("/v1/openapi.yaml")})

> 密钥默认隐藏；查看会记录审计。吊销后仍可删除；未吊销的密钥需先吊销再删除。`;

export function ApiDocsWorkspace() {
  return (
    <article className="api-docs">
      <MessageMarkdown content={content} />
    </article>
  );
}
