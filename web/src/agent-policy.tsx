export type AgentPolicy = {
  advanced_tools: boolean;
  timezone: string;
  session: {
    types: "all" | "private" | "group";
    group_scope: "member" | "shared";
    idle_minutes: number;
    allow_reset: boolean;
  };
  context: {
    strategy: "full" | "recent" | "time" | "summary";
    max_turns: number;
    max_age_minutes: number;
    summary_threshold: number;
    keep_recent_tokens: number;
  };
  output: {
    thinking: boolean;
    tools: "hidden" | "summary" | "detailed";
    sources: boolean;
    progress: boolean;
  };
};
export const defaultAgentPolicy: AgentPolicy = {
  advanced_tools: false,
  timezone: "Asia/Shanghai",
  session: {
    types: "all",
    group_scope: "member",
    idle_minutes: 0,
    allow_reset: true,
  },
  context: {
    strategy: "recent",
    max_turns: 20,
    max_age_minutes: 1440,
    summary_threshold: 70,
    keep_recent_tokens: 4096,
  },
  output: { thinking: false, tools: "summary", sources: true, progress: true },
};
const descriptions = {
  full: "携带当前会话全部有效历史。达到模型窗口上限时停止并提示新建会话，适合较短的连续任务。",
  recent:
    "按完整轮次保留最近对话，包含本轮提问及其工具调用。窗口不足时继续移除最早的轮次，完整聊天记录仍保留。",
  time: "只携带指定时间范围内的对话，并限制最多轮数。当前提问始终保留，适合临时咨询和日常服务。",
  summary:
    "接近设定阈值时，调用当前模型将较早内容压缩为摘要，并保留近期原文。适合长期任务，会增加模型用量，摘要可能丢失细节。",
};
export function AgentPolicySettings({
  section,
  value,
  onChange,
}: {
  section: "session" | "context" | "output";
  value: AgentPolicy;
  onChange: (value: AgentPolicy) => void;
}) {
  const session = value.session,
    context = value.context,
    output = value.output;
  const setSession = (next: Partial<AgentPolicy["session"]>) =>
    onChange({ ...value, session: { ...session, ...next } });
  const setContext = (next: Partial<AgentPolicy["context"]>) =>
    onChange({ ...value, context: { ...context, ...next } });
  const setOutput = (next: Partial<AgentPolicy["output"]>) =>
    onChange({ ...value, output: { ...output, ...next } });
  if (section === "session")
    return (
      <div className="agent-policy-fields">
        <p className="muted">
          决定助手接收哪些对话，以及群成员之间是否共享上下文。
        </p>
        <label>
          允许的会话类型
          <select
            aria-label="允许的会话类型"
            value={session.types}
            onChange={(e) =>
              setSession({ types: e.target.value as typeof session.types })
            }
          >
            <option value="all">私聊和群聊</option>
            <option value="private">仅私聊</option>
            <option value="group">仅群聊</option>
          </select>
        </label>
        <small>
          Web 对话属于私聊。此限制作用于新消息，已有排队任务继续执行。
        </small>
        <label>
          群聊上下文范围
          <select
            disabled={session.types === "private"}
            aria-label="群聊上下文范围"
            value={session.group_scope}
            onChange={(e) =>
              setSession({
                group_scope: e.target.value as typeof session.group_scope,
              })
            }
          >
            <option value="member">按群内成员隔离</option>
            <option value="shared">全群共享一个会话</option>
          </select>
        </label>
        <small>
          按成员隔离时，同一群的不同成员、不同群和私聊互不共享历史。回复始终发送到原聊天位置。
        </small>
        {session.types !== "private" && session.group_scope === "shared" && (
          <p className="permission-notice">
            同一群内所有成员的提问、回复和工具结果都可进入共享上下文。允许 /new
            时，任一成员重置都会影响整个群的后续会话。
          </p>
        )}
        <label>
          空闲多久后开启新会话（分钟）
          <input
            type="number"
            min={0}
            max={10080}
            step={1}
            required
            aria-label="空闲多久后开启新会话（分钟）"
            value={session.idle_minutes}
            onChange={(e) =>
              setSession({ idle_minutes: Number(e.target.value) })
            }
          />
        </label>
        <small>
          0 表示不自动重置，最多 7
          天。仅作用于渠道；正在执行或排队的任务不会因空闲重置而被拆分，旧记录继续保留。
        </small>
        <label className="checkbox-label">
          <input
            type="checkbox"
            checked={session.allow_reset}
            onChange={(e) => setSession({ allow_reset: e.target.checked })}
          />
          允许用户通过 /new 开启新会话
        </label>
        <small>
          仅识别适配器传入的完整 /new 命令；此设置不限制管理员在 Web
          中新建或清理对话。
        </small>
      </div>
    );
  if (section === "context")
    return (
      <div className="agent-policy-fields">
        <p className="muted">
          控制每次请求携带的历史。角色指令和当前提问始终保留，聊天记录不会因为裁剪或摘要而删除。
        </p>
        <label>
          上下文策略
          <select
            aria-label="上下文策略"
            value={context.strategy}
            onChange={(e) =>
              setContext({
                strategy: e.target.value as typeof context.strategy,
              })
            }
          >
            <option value="recent">最近若干轮（推荐）</option>
            <option value="time">近期时间窗口</option>
            <option value="summary">自动摘要压缩</option>
            <option value="full">完整历史</option>
          </select>
        </label>
        <p className="policy-description">{descriptions[context.strategy]}</p>
        {(context.strategy === "recent" || context.strategy === "time") && (
          <label>
            最多保留对话轮数
            <input
              type="number"
              required
              min={1}
              max={200}
              step={1}
              aria-label="最多保留对话轮数"
              value={context.max_turns}
              onChange={(e) =>
                setContext({ max_turns: Number(e.target.value) })
              }
            />
            <small>包含本轮，1 轮指一次用户提问及其后的回答和工具交互。</small>
          </label>
        )}
        {context.strategy === "time" && (
          <label>
            历史时间窗口（分钟）
            <input
              type="number"
              required
              min={1}
              max={43200}
              step={1}
              aria-label="历史时间窗口（分钟）"
              value={context.max_age_minutes}
              onChange={(e) =>
                setContext({ max_age_minutes: Number(e.target.value) })
              }
            />
            <small>例如 60 分钟、1440 分钟（1 天），最多 30 天。</small>
          </label>
        )}
        {context.strategy === "summary" && (
          <>
            <div className="policy-input-grid">
              <label>
                输入预算使用阈值（%）
                <input
                  type="number"
                  required
                  min={50}
                  max={90}
                  step={1}
                  aria-label="输入预算使用阈值（%）"
                  value={context.summary_threshold}
                  onChange={(e) =>
                    setContext({ summary_threshold: Number(e.target.value) })
                  }
                />
              </label>
              <label>
                保留近期原文（Token）
                <input
                  type="number"
                  required
                  min={128}
                  max={32768}
                  step={1}
                  aria-label="保留近期原文（Token）"
                  value={context.keep_recent_tokens}
                  onChange={(e) =>
                    setContext({ keep_recent_tokens: Number(e.target.value) })
                  }
                />
              </label>
            </div>
            <small>
              输入预算为模型窗口减去最大输出。近期原文会随可用预算自动缩小；摘要失败时停止本次任务，不会静默丢弃历史或自动重跑工具。
            </small>
          </>
        )}
        <p className="muted">
          超过预算的单条消息、指令或工具定义仍会被拒绝。已生成的摘要会继续属于该会话，若需重新开始请新建会话。
        </p>
      </div>
    );
  const preset =
    !output.thinking &&
    output.tools === "hidden" &&
    output.sources &&
    !output.progress
      ? "concise"
      : !output.thinking &&
          output.tools === "summary" &&
          output.sources &&
          output.progress
        ? "balanced"
        : output.thinking &&
            output.tools === "detailed" &&
            output.sources &&
            output.progress
          ? "detailed"
          : "custom";
  return (
    <div className="agent-policy-fields">
      <p className="muted">
        为日常使用提供简洁回复，也可为排查和复杂任务展开更多执行信息。
      </p>
      <label>
        回复预设
        <select
          aria-label="回复预设"
          value={preset}
          onChange={(e) => {
            if (e.target.value === "concise")
              setOutput({
                thinking: false,
                tools: "hidden",
                sources: true,
                progress: false,
              });
            if (e.target.value === "balanced")
              setOutput({
                thinking: false,
                tools: "summary",
                sources: true,
                progress: true,
              });
            if (e.target.value === "detailed")
              setOutput({
                thinking: true,
                tools: "detailed",
                sources: true,
                progress: true,
              });
          }}
        >
          <option value="concise">简洁回复</option>
          <option value="balanced">均衡展示</option>
          <option value="detailed">完整展示</option>
          <option value="custom" disabled>
            自定义
          </option>
        </select>
      </label>
      <label className="checkbox-label">
        <input
          type="checkbox"
          checked={output.thinking}
          onChange={(e) => setOutput({ thinking: e.target.checked })}
        />
        展示模型返回的思考摘要
      </label>
      <small>
        仅展示模型接口实际返回的推理文本；不支持的模型不会生成该内容。此设置不改变模型的推理强度。
      </small>
      <label>
        工具调用展示
        <select
          aria-label="工具调用展示"
          value={output.tools}
          onChange={(e) =>
            setOutput({ tools: e.target.value as typeof output.tools })
          }
        >
          <option value="hidden">隐藏工具过程</option>
          <option value="summary">简要状态（名称和结果状态）</option>
          <option value="detailed">详细信息（参数和结果）</option>
        </select>
      </label>
      <small>
        详细信息在 Web
        中按需展开，在渠道回复中附带截取的参数和结果。选择前请确认接收者可以查看这些业务数据。
      </small>
      <label className="checkbox-label">
        <input
          type="checkbox"
          checked={output.sources}
          onChange={(e) => setOutput({ sources: e.target.checked })}
        />
        展示本轮检索来源
      </label>
      <label className="checkbox-label">
        <input
          type="checkbox"
          checked={output.progress}
          onChange={(e) => setOutput({ progress: e.target.checked })}
        />
        渠道排队超过 30 秒或处理超过 90 秒时发送提示
      </label>
      <p className="muted">
        这些设置用于新任务的 Web
        展示与渠道回复。管理员仍可在运行详情中核对工具审计记录；模型自行写入回答的内容不受展示开关控制。
      </p>
    </div>
  );
}
