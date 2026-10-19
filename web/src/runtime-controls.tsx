import { ApplicationUpdates } from "./updates";
import { FormEvent, useEffect, useRef, useState } from "react";
import { Icon } from "./icons";
import { ToolCallDetails } from "./capabilities";

type Api = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
type KnowledgeLimits = {
  max_file_bytes: number;
  max_characters: number;
  max_chunks: number;
  max_pdf_pages: number;
  allowed_extensions: string[];
  extend_with_parsers: boolean;
};
const builtinFormats: { key: string; label: string }[] = [
  { key: "txt", label: "TXT" },
  { key: "markdown", label: "Markdown" },
  { key: "pdf", label: "PDF" },
  { key: "docx", label: "DOCX" },
];
const megabyte = 1024 * 1024;

function KnowledgeUploadSettings({ request }: { request: Api }) {
  const [value, setValue] = useState<KnowledgeLimits | null>(null);
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");
  const [pending, setPending] = useState(false);
  useEffect(() => {
    void request<KnowledgeLimits>("/knowledge/limits")
      .then(setValue)
      .catch((error) => setError(message(error)));
  }, []);
  async function save(event: FormEvent) {
    event.preventDefault();
    if (!value) return;
    if (!value.allowed_extensions.length && !value.extend_with_parsers) {
      setError("请至少选择一种允许的上传格式，或允许解析扩展格式。");
      setFeedback("");
      return;
    }
    setPending(true);
    setError("");
    setFeedback("");
    try {
      setValue(await request<KnowledgeLimits>("/knowledge/limits", "PUT", value));
      setFeedback("上传限制已保存，立即作用于新的上传与重新索引。");
    } catch (error) {
      setError(message(error));
    } finally {
      setPending(false);
    }
  }
  async function restore() {
    setPending(true);
    setError("");
    setFeedback("");
    try {
      setValue(await request<KnowledgeLimits>("/knowledge/limits", "PUT", {}));
      setFeedback("已恢复默认上传限制。");
    } catch (error) {
      setError(message(error));
    } finally {
      setPending(false);
    }
  }
  const toggleFormat = (key: string) => {
    if (!value) return;
    const active = value.allowed_extensions.includes(key);
    setValue({
      ...value,
      allowed_extensions: active
        ? value.allowed_extensions.filter((item) => item !== key)
        : [...value.allowed_extensions, key],
    });
  };
  const extra = value
    ? value.allowed_extensions.filter(
        (item) => !builtinFormats.some((format) => format.key === item),
      )
    : [];
  return (
    <section className="settings-section" aria-labelledby="knowledge-limits-title">
      <header className="settings-section-head">
        <div>
          <h2 id="knowledge-limits-title">知识库上传限制</h2>
          <p>
            控制新上传和重新索引时接受的文档。单文件上限越大，解析耗时、内存与磁盘占用越高；PDF
            另受页数限制。
          </p>
        </div>
      </header>
      {value && (
        <form onSubmit={save}>
          <div className="limits-grid">
            <label>
              单文件上限（MiB）
              <input
                type="number"
                min={1}
                max={1024}
                step={1}
                required
                value={Math.round(value.max_file_bytes / megabyte)}
                onChange={(event) =>
                  setValue({
                    ...value,
                    max_file_bytes: Number(event.target.value) * megabyte,
                  })
                }
              />
            </label>
            <label>
              单文档字符上限
              <input
                type="number"
                min={1000}
                max={50000000}
                step={1000}
                required
                value={value.max_characters}
                onChange={(event) =>
                  setValue({
                    ...value,
                    max_characters: Number(event.target.value),
                  })
                }
              />
            </label>
            <label>
              单文档片段上限
              <input
                type="number"
                min={1}
                max={200000}
                step={1}
                required
                value={value.max_chunks}
                onChange={(event) =>
                  setValue({
                    ...value,
                    max_chunks: Number(event.target.value),
                  })
                }
              />
            </label>
            <label>
              PDF 页数上限
              <input
                type="number"
                min={1}
                max={10000}
                step={1}
                required
                value={value.max_pdf_pages}
                onChange={(event) =>
                  setValue({
                    ...value,
                    max_pdf_pages: Number(event.target.value),
                  })
                }
              />
            </label>
          </div>
          <fieldset className="format-grid">
            <legend>允许的上传格式</legend>
            {builtinFormats.map((format) => (
              <label className="checkbox-label" key={format.key}>
                <input
                  type="checkbox"
                  checked={value.allowed_extensions.includes(format.key)}
                  onChange={() => toggleFormat(format.key)}
                />
                {format.label}
              </label>
            ))}
          </fieldset>
          <label>
            解析扩展格式（逗号分隔）
            <input
              value={extra.join(", ")}
              placeholder="例如 xlsx, epub"
              onChange={(event) =>
                setValue({
                  ...value,
                  allowed_extensions: [
                    ...value.allowed_extensions.filter((item) =>
                      builtinFormats.some((format) => format.key === item),
                    ),
                    ...event.target.value
                      .split(",")
                      .map((item) => item.trim().replace(/^\./, "").toLowerCase())
                      .filter(Boolean),
                  ],
                })
              }
            />
          </label>
          <label className="checkbox-label">
            <input
              type="checkbox"
              checked={value.extend_with_parsers}
              onChange={(event) =>
                setValue({
                  ...value,
                  extend_with_parsers: event.target.checked,
                })
              }
            />
            同时接受已启用解析扩展声明的其他格式
          </label>
          <p className="muted">
            显式列出的格式就是全部允许范围；勾选上一项时，安装并启用的解析扩展可自动增加格式。未列出的文件会在解析前被拒绝。
          </p>
          <div className="form-actions">
            <button className="primary" disabled={pending}>
              {pending ? "正在保存…" : "保存上传限制"}
            </button>
            <button type="button" disabled={pending} onClick={() => void restore()}>
              恢复默认
            </button>
          </div>
        </form>
      )}
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      {feedback && (
        <p className="success" role="status">
          {feedback}
        </p>
      )}
    </section>
  );
}

type Limits = {
  concurrent: number;
  conversation_queue: number;
  global_queue: number;
  run_timeout_seconds: number;
  tool_timeout_seconds: number;
  tool_rounds: number;
};
const fields: { key: keyof Limits; label: string; min: number; max: number }[] =
  [
    { key: "concurrent", label: "同时执行的任务数", min: 1, max: 8 },
    { key: "conversation_queue", label: "每个会话最多等待", min: 1, max: 1000 },
    { key: "global_queue", label: "实例最多等待", min: 1, max: 10000 },
    {
      key: "run_timeout_seconds",
      label: "单次运行时限（秒）",
      min: 1,
      max: 3600,
    },
    {
      key: "tool_timeout_seconds",
      label: "单次工具时限（秒）",
      min: 1,
      max: 600,
    },
    { key: "tool_rounds", label: "最多工具调用轮数", min: 1, max: 100 },
  ];
const message = (error: unknown) =>
  error instanceof Error ? error.message : "无法连接服务，请稍后重试。";

type Storage = {
  disk_total_bytes: number;
  disk_available_bytes: number;
  data_bytes: number;
  files: number;
  complete: boolean;
  cleanup_pending: number;
  warning: string;
  groups: Record<string, number>;
};
const bytes = (value: number) =>
  value >= 2 ** 30
    ? `${(value / 2 ** 30).toFixed(2)} GiB`
    : value >= 2 ** 20
      ? `${(value / 2 ** 20).toFixed(1)} MiB`
      : `${value.toLocaleString()} B`;
export function ConversationMaintenance({
  request,
  id,
  onDeleted,
}: {
  request: Api;
  id: string;
  onDeleted: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const dialog = useRef<HTMLDialogElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (open) {
      dialog.current?.showModal();
      cancel.current?.focus();
    }
  }, [open]);
  function close() {
    if (pending) return;
    dialog.current?.close();
    setOpen(false);
    setConfirmed(false);
    setError("");
  }
  return (
    <>
      <button
        className="delete-conversation-button"
        aria-label="删除对话"
        title="删除此对话"
        onClick={() => setOpen(true)}
      >
        <Icon name="trash" />
      </button>
      {open && (
        <dialog
          className="confirmation-dialog"
          ref={dialog}
          aria-labelledby="delete-conversation-title"
          aria-describedby="delete-conversation-description"
          onCancel={(event) => {
            event.preventDefault();
            close();
          }}
          onClick={(event) => {
            if (event.target !== event.currentTarget) return;
            const bounds = event.currentTarget.getBoundingClientRect();
            if (
              event.clientX < bounds.left ||
              event.clientX > bounds.right ||
              event.clientY < bounds.top ||
              event.clientY > bounds.bottom
            )
              close();
          }}
        >
          <div className="confirmation-heading">
            <span className="delete-symbol" aria-hidden="true">
              <Icon name="warning" size={20} />
            </span>
            <h2 id="delete-conversation-title">删除此对话？</h2>
          </div>
          <p id="delete-conversation-description">
            此操作将永久删除消息、运行与工具记录，以及模型上下文。已发生的外部操作不会撤销。
          </p>
          <p className="muted">
            请先等待或取消未完成的任务；有待发送回复的对话暂不能删除。渠道消息的去重标识会保留。
          </p>
          <label className="checkbox-label">
            <input
              type="checkbox"
              checked={confirmed}
              disabled={pending}
              onChange={(event) => setConfirmed(event.target.checked)}
            />
            我已核对历史，确认永久删除此会话
          </label>
          {error && (
            <p className="error" role="alert">
              {error}
            </p>
          )}
          <div className="confirmation-actions">
            <button ref={cancel} disabled={pending} onClick={close}>
              取消
            </button>
            <button
              className="danger-button"
              disabled={!confirmed || pending}
              onClick={async () => {
                setPending(true);
                setError("");
                try {
                  await request(`/conversations/${id}`, "DELETE", {
                    confirm: true,
                  });
                  dialog.current?.close();
                  setOpen(false);
                  onDeleted();
                } catch (error) {
                  setError(message(error));
                } finally {
                  setPending(false);
                }
              }}
            >
              {pending ? "正在删除…" : "确认删除会话"}
            </button>
          </div>
        </dialog>
      )}
    </>
  );
}

export function ExecutionSettings({ request }: { request: Api }) {
  const [limits, setLimits] = useState<Limits | null>(null);
  const [counts, setCounts] = useState<Record<string, number>>({});
  const [storage, setStorage] = useState<Storage | null>(null);
  const [ready, setReady] = useState(false);
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");
  const [pending, setPending] = useState(false);
  async function refresh() {
    const state = await request<{
      limits: Limits;
      counts: Record<string, number>;
      runtime_ready: boolean;
    }>("/diagnostics");
    setLimits(state.limits);
    setCounts(state.counts);
    setReady(state.runtime_ready);
    setStorage(await request<Storage>("/storage"));
  }
  useEffect(() => {
    void refresh().catch((error) => setError(message(error)));
  }, []);
  async function save(event: FormEvent) {
    event.preventDefault();
    setError("");
    setFeedback("");
    setPending(true);
    try {
      setLimits(await request<Limits>("/limits", "PUT", limits));
      setFeedback(
        "限制已保存。并发与队列上限立即生效，时限和工具轮数用于新提交的任务。",
      );
    } catch (error) {
      setError(message(error));
    } finally {
      setPending(false);
    }
  }
  return (
    <section className="system-settings">
      <div className="execution-settings">
        <ApplicationUpdates request={request} />
        <p className="muted">
          {ready ? "对话服务已就绪" : "对话服务正在恢复"} · 执行中{" "}
          {counts.running ?? 0} · 取消中 {counts.cancelling ?? 0} · 等待{" "}
          {counts.queued ?? 0}
        </p>
        {storage && (
          <section className="storage-usage" aria-label="磁盘使用">
            <p>
              数据目录使用 <strong>{bytes(storage.data_bytes)}</strong> ·{" "}
              {storage.files.toLocaleString()} 个文件
              {!storage.complete ? "（部分统计）" : ""}
            </p>
            <p>
              所在磁盘可用 {bytes(storage.disk_available_bytes)} /{" "}
              {bytes(storage.disk_total_bytes)} · 待清理会话{" "}
              {storage.cleanup_pending}
            </p>
            {storage.warning && (
              <p className="context-warning" role="status">
                {storage.warning}
              </p>
            )}
            <small>
              统计包含数据库、模型上下文、知识文件和工作区；不会自动删除业务历史。SQLite
              删除记录后可能保留空闲页，数据大小不等于立即可回收空间。
            </small>
          </section>
        )}
        <KnowledgeUploadSettings request={request} />
        {limits && (
          <form onSubmit={save}>
            <div className="limits-grid">
              {fields.map((field) => (
                <label key={field.key}>
                  {field.label}
                  <input
                    type="number"
                    min={field.min}
                    max={field.max}
                    step={1}
                    required
                    value={limits[field.key]}
                    onChange={(event) =>
                      setLimits({
                        ...limits,
                        [field.key]: Number(event.target.value),
                      })
                    }
                  />
                </label>
              ))}
            </div>
            <small>
              总等待数不能小于单个会话的等待数。上下文策略在 Agent
              中配置，窗口与最大输出由模型连接决定。
            </small>
            <div className="form-actions">
              <button disabled={pending}>保存运行限制</button>
              <button
                type="button"
                disabled={pending}
                onClick={() =>
                  void refresh().catch((error) => setError(message(error)))
                }
              >
                刷新状态
              </button>
            </div>
          </form>
        )}
        {error && (
          <p className="error" role="alert">
            {error}
          </p>
        )}
        {feedback && (
          <p className="success" role="status">
            {feedback}
          </p>
        )}
      </div>
    </section>
  );
}

type Detail = {
  run: { status: string; error: string; created: number };
  started: number;
  released: boolean;
  model: string;
  model_version: number;
  agent_name?: string;
  agent_id?: string;
  model_id?: number;
  tools: { id: string; name: string; display_name: string; status: string }[];
  events: {
    seq: number;
    kind: string;
    created: number;
    data: Record<string, unknown>;
  }[];
};
const labels: Record<string, string> = {
  running: "执行中",
  completed: "已完成",
  failed: "失败",
  interrupted: "结果不明，需核对",
};
const kinds: Record<string, string> = {
  thinking_delta: "思考摘要已保存",
  compaction_start: "正在压缩上下文",
  compaction_end: "上下文压缩已结束",
  queued: "已进入队列",
  cancel_requested: "已请求取消",
  timeout_requested: "已请求超时停止",
  dispatched: "已派发",
  run_started: "已开始",
  message_delta: "文本已保存",
  run_completed: "已完成",
  run_failed: "已失败",
  run_cancelled: "已取消",
  run_cancelling: "正在取消",
  run_released: "资源已释放",
  run_interrupted: "运行已中断",
  tool_start: "工具开始",
  tool_end: "工具结束",
  interrupted: "运行已中断",
  event_gap: "事件缺失",
  reconciled: "状态已核对",
  error: "请求未接受",
};
export function RunDiagnostics({
  run,
  request,
}: {
  run: { id: string; status: string };
  request: Api;
}) {
  const [open, setOpen] = useState(false);
  const [detail, setDetail] = useState<Detail | null>(null);
  const [error, setError] = useState("");
  async function refresh() {
    try {
      setDetail(await request<Detail>(`/runs/${run.id}`));
      setError("");
    } catch (error) {
      setError(message(error));
    }
  }
  useEffect(() => {
    if (open) void refresh();
  }, [open, run.id, run.status]);
  return (
    <details
      className="run-diagnostics"
      onToggle={(event) => setOpen(event.currentTarget.open)}
    >
      <summary>
        <Icon name="caret-right" size={14} className="disclosure-icon" />
        运行详情
      </summary>
      {open && (
        <div>
          {error && (
            <p className="error" role="alert">
              {error}
            </p>
          )}
          {detail && (
            <>
              <p className="muted">
                {detail.agent_name && <>助手 {detail.agent_name} · </>}模型{" "}
                {detail.model}
                {detail.model_id ? `（连接 ${detail.model_id}）` : ""} ·
                配置版本 {detail.model_version} ·{" "}
                {detail.released ? "资源已释放" : "仍在确认资源释放"}
              </p>
              {detail.tools.length > 0 && (
                <ul className="tool-list">
                  {detail.tools.map((tool) => (
                    <li key={tool.id}>
                      <div>
                        <strong>{tool.display_name || tool.name}</strong>
                        <span>{labels[tool.status] ?? tool.status}</span>
                        <ToolCallDetails
                          request={request}
                          runID={run.id}
                          callID={tool.id}
                        />
                      </div>
                    </li>
                  ))}
                </ul>
              )}
              {detail.tools.length > 0 && (
                <p className="muted">
                  取消或中断不能撤销已经发生的操作。结果不明时，请先核对外部系统；参数和结果可逐项展开查看。
                </p>
              )}
              <ol className="event-list">
                {detail.events.map((event, index) => (
                  <li key={`${event.seq}-${index}`}>
                    <time>
                      {new Date(event.created).toLocaleTimeString("zh-CN")}
                    </time>
                    <span>{kinds[event.kind] ?? event.kind}</span>
                    <span>{event.seq ? `#${event.seq}` : "平台"}</span>
                  </li>
                ))}
              </ol>
              <small>
                显示最近 200
                条诊断记录；文本只记录大小，密钥、工具参数及原始输出不进入此记录。
              </small>
            </>
          )}
          <button type="button" onClick={() => void refresh()}>
            刷新运行详情
          </button>
        </div>
      )}
    </details>
  );
}
