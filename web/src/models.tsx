import { Icon } from "./icons";
import { FormEvent, useState, useEffect, useRef } from "react";
import type { Model, ModelOptions } from "./agents";
type Api = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
const protocols = [
  {
    id: "openai-completions",
    name: "OpenAI Chat Completions",
    path: "/chat/completions",
  },
  { id: "openai-responses", name: "OpenAI Responses", path: "/responses" },
  {
    id: "anthropic-messages",
    name: "Anthropic Messages",
    path: "/v1/messages",
  },
] as const;
const defaults: ModelOptions = {
  display_name: "",
  api: "openai-completions",
  context_window: 32768,
  max_tokens: 4096,
  reasoning: false,
  thinking_level: "off",
  temperature: null,
  top_p: null,
  max_tokens_field: "",
};
const blank: Model = {
  id: 0,
  name: "",
  base_url: "https://api.openai.com/v1",
  has_key: false,
  version: 0,
  archived: false,
  options: defaults,
};
const count = (n: number) => n.toLocaleString();
export function ModelsWorkspace({
  request,
  models,
  onSaved,
  onStart,
}: {
  request: Api;
  models: Model[];
  onSaved: () => Promise<void>;
  onStart: () => void;
}) {
  const [editor, setEditor] = useState<Model>(
    models.find((model) => !model.archived) ?? blank,
  );
  const [key, setKey] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");
  const [query, setQuery] = useState("");
  const [available, setAvailable] = useState<string[] | null>(null);
  const [manual, setManual] = useState(false);
  const [discovering, setDiscovering] = useState(false);
  const [discoveryError, setDiscoveryError] = useState("");
  const sequence = useRef(0);
  useEffect(() => {
    sequence.current++;
    setAvailable(null);
    setManual(false);
    setDiscovering(false);
    setDiscoveryError("");
    return () => {
      sequence.current++;
    };
  }, [editor.id, editor.base_url, editor.options.api, key]);
  async function discover() {
    const current = ++sequence.current;
    setDiscovering(true);
    setDiscoveryError("");
    try {
      const result = await request<{ models: string[] }>(
        "/models/discover",
        "POST",
        {
          model_id: editor.id,
          base_url: editor.base_url,
          api: editor.options.api,
          api_key: key,
        },
      );
      if (current === sequence.current) {
        setAvailable(result.models);
        setManual(false);
      }
    } catch (error) {
      if (current === sequence.current)
        setDiscoveryError(
          error instanceof Error ? error.message : "获取失败，请稍后重试。",
        );
    } finally {
      if (current === sequence.current) setDiscovering(false);
    }
  }
  const first = models.length === 0;
  const o = editor.options;
  const protocol = protocols.find((item) => item.id === o.api)!;
  const saved = models.find((model) => model.id === editor.id);
  const dirty =
    key !== "" || !saved || JSON.stringify(saved) !== JSON.stringify(editor);
  const visible = models.filter((model) =>
    `${model.options.display_name} ${model.name} ${model.base_url}`
      .toLowerCase()
      .includes(query.toLowerCase()),
  );
  function edit(model: Model) {
    setEditor(model);
    setKey("");
    setError("");
    setFeedback("");
  }
  function options(value: Partial<ModelOptions>) {
    setEditor((current) => ({
      ...current,
      options: { ...current.options, ...value },
    }));
    setFeedback("");
  }
  async function action(work: () => Promise<void>) {
    setPending(true);
    setError("");
    setFeedback("");
    try {
      await work();
    } catch (error) {
      setError(
        error instanceof Error ? error.message : "操作未完成，请稍后重试。",
      );
    } finally {
      setPending(false);
    }
  }
  async function save(event: FormEvent) {
    event.preventDefault();
    await action(async () => {
      const model = await request<Model>(
        editor.id ? `/models/${editor.id}` : "/models",
        editor.id ? "PUT" : "POST",
        {
          base_url: editor.base_url,
          name: editor.name,
          api_key: key,
          options: o,
        },
      );
      setEditor(model);
      setKey("");
      await onSaved();
      setFeedback("模型连接已保存，密钥不会回显。");
      if (first) onStart();
    });
  }
  return (
    <div className="model-console">
      <aside className="model-catalog-panel" aria-label="模型连接列表">
        <div className="model-catalog-heading">
          <h2>
            连接列表 <span>{models.length}</span>
          </h2>
          <button
            disabled={pending}
            onClick={() => edit(blank)}
            aria-label="新增模型连接"
            title="新增模型连接"
          >
            <Icon name="plus" size={16} /> 新增
          </button>
        </div>
        {models.length > 3 && (
          <input
            className="model-search"
            aria-label="搜索模型连接"
            type="search"
            placeholder="搜索名称、模型或地址"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
        )}
        <div className="model-connection-list">
          {visible.map((model) => (
            <button
              key={model.id}
              disabled={pending}
              className={`model-connection ${editor.id === model.id ? "selected" : ""} ${model.archived ? "is-archived" : ""}`}
              aria-current={editor.id === model.id ? "true" : undefined}
              onClick={() => edit(model)}
            >
              <span className="model-provider-icon" aria-hidden="true">
                <Icon name="models" size={18} />
              </span>
              <span className="model-connection-copy">
                <strong>{model.options.display_name || model.name}</strong>
                <span>{model.name}</span>
                <small>
                  {model.archived
                    ? "已归档"
                    : protocols.find((p) => p.id === model.options.api)?.name}
                </small>
              </span>
              <span
                className="model-connection-status"
                aria-label={model.archived ? "已归档" : "已配置"}
              />
            </button>
          ))}
        </div>
        {!models.length && (
          <div className="model-list-empty">
            <strong>连接第一个模型</strong>
            <p>填写服务信息并保存，即可使用默认 Agent 开始对话。</p>
          </div>
        )}
        {!!models.length && !visible.length && (
          <p className="muted">没有匹配的连接。</p>
        )}
        <p className="model-catalog-note">
          一个连接可供多个 Agent 使用。每次运行保留提交时的配置。
        </p>
      </aside>
      <section className="model-detail">
        <header className="model-detail-heading">
          <div>
            <p className="eyebrow">
              {first
                ? "首次配置"
                : editor.id
                  ? `连接 #${editor.id} · v${editor.version}`
                  : "新增连接"}
            </p>
            <h2>
              {first
                ? "连接你的模型"
                : o.display_name || editor.name || "新增模型连接"}
            </h2>
          </div>
          <span className={`model-save-state ${dirty ? "unsaved" : ""}`}>
            {editor.archived ? "已归档" : dirty ? "待保存" : "已保存"}
          </span>
        </header>
        <form
          className="agents-model-editor model-form"
          onSubmit={save}
          onInvalidCapture={(event) => {
            const details = (event.target as HTMLElement).closest("details");
            if (details) details.open = true;
          }}
        >
          <fieldset disabled={pending || editor.archived}>
            <section className="model-form-section">
              <div className="model-section-title">
                <h3>连接信息</h3>
                <p>选择接口协议，填写模型服务提供的地址和凭据。</p>
              </div>
              <div className="model-fields">
                <label>
                  连接名称
                  <input
                    maxLength={100}
                    value={o.display_name}
                    onChange={(event) =>
                      options({ display_name: event.target.value })
                    }
                    placeholder="例如：主力模型、内网推理服务"
                  />
                </label>
                <label>
                  接口协议
                  <select
                    aria-label="接口协议"
                    value={o.api}
                    onChange={(event) => {
                      const api = event.target.value as ModelOptions["api"];
                      options({
                        api,
                        max_tokens_field: "",
                        ...(api === "anthropic-messages"
                          ? { top_p: null, temperature: null }
                          : {}),
                      });
                      if (
                        editor.base_url === "https://api.openai.com/v1" &&
                        api === "anthropic-messages"
                      )
                        setEditor((current) => ({
                          ...current,
                          base_url: "https://api.anthropic.com",
                        }));
                      else if (
                        editor.base_url === "https://api.anthropic.com" &&
                        api !== "anthropic-messages"
                      )
                        setEditor((current) => ({
                          ...current,
                          base_url: "https://api.openai.com/v1",
                        }));
                    }}
                  >
                    {protocols.map((p) => (
                      <option key={p.id} value={p.id}>
                        {p.name}
                      </option>
                    ))}
                  </select>
                </label>
                <div className="model-field-wide">
                  <label>
                    服务地址
                    <input
                      required
                      type="url"
                      maxLength={2048}
                      value={editor.base_url}
                      onChange={(event) =>
                        setEditor({ ...editor, base_url: event.target.value })
                      }
                      placeholder={
                        o.api === "anthropic-messages"
                          ? "https://api.anthropic.com"
                          : "https://api.example.com/v1"
                      }
                      aria-describedby="model-url-help"
                    />
                  </label>
                  <small id="model-url-help">
                    填写基础地址，不包含 {protocol.path}
                    。支持兼容此协议的代理或本地服务。
                  </small>
                </div>
                <div>
                  <label htmlFor="model-name">模型名称</label>
                  <div className="model-name-control">
                    {available?.length && !manual ? (
                      <select
                        id="model-name"
                        required
                        value={editor.name}
                        onChange={(event) =>
                          setEditor({ ...editor, name: event.target.value })
                        }
                      >
                        {!available.includes(editor.name) && (
                          <option value={editor.name}>
                            {editor.name
                              ? `${editor.name}（当前填写）`
                              : "请选择模型"}
                          </option>
                        )}
                        {available.map((name) => (
                          <option key={name} value={name}>
                            {name}
                          </option>
                        ))}
                      </select>
                    ) : (
                      <input
                        id="model-name"
                        required
                        maxLength={200}
                        value={editor.name}
                        onChange={(event) =>
                          setEditor({ ...editor, name: event.target.value })
                        }
                        placeholder="服务商提供的模型 ID"
                        spellCheck={false}
                      />
                    )}
                    <button
                      type="button"
                      onClick={() => void discover()}
                      disabled={
                        discovering ||
                        !editor.base_url.trim() ||
                        (!key && !editor.has_key)
                      }
                    >
                      {discovering ? "正在获取…" : "获取模型列表"}
                    </button>
                  </div>
                  {!!available?.length && (
                    <div className="model-discovery-note">
                      <small>已获取 {available.length} 个模型</small>
                      <button
                        type="button"
                        onClick={() => setManual((value) => !value)}
                      >
                        {manual ? "从列表选择" : "手动填写"}
                      </button>
                    </div>
                  )}
                  {available?.length === 0 && (
                    <small>服务商未返回可选模型，可继续手动填写。</small>
                  )}
                  {discoveryError && (
                    <small className="error" role="alert">
                      {discoveryError}
                    </small>
                  )}
                </div>
                <div>
                  <label>
                    API 密钥
                    <input
                      type="password"
                      autoComplete="new-password"
                      required={!editor.has_key}
                      maxLength={8192}
                      value={key}
                      onChange={(event) => setKey(event.target.value)}
                      placeholder={
                        editor.has_key
                          ? "已加密保存，留空不修改"
                          : "输入 API 密钥"
                      }
                    />
                  </label>
                  <small>
                    {editor.has_key
                      ? "已保存的密钥不会回显。"
                      : "无鉴权的本地服务可使用其接受的占位密钥。"}
                  </small>
                </div>
              </div>
            </section>
            <section className="model-form-section">
              <div className="model-section-title">
                <h3>上下文与输出</h3>
                <p>按服务商公布的模型规格填写，单位为 Token。</p>
              </div>
              <div className="model-fields">
                <label>
                  上下文窗口
                  <input
                    type="number"
                    required
                    min={256}
                    max={10000000}
                    step={1}
                    value={o.context_window}
                    onChange={(event) =>
                      options({ context_window: Number(event.target.value) })
                    }
                  />
                </label>
                <label>
                  最大输出长度
                  <input
                    type="number"
                    required
                    min={
                      o.api === "anthropic-messages" &&
                      o.thinking_level !== "off"
                        ? 2048
                        : 16
                    }
                    max={o.context_window - 1}
                    step={1}
                    value={o.max_tokens}
                    onChange={(event) =>
                      options({ max_tokens: Number(event.target.value) })
                    }
                  />
                </label>
              </div>
              <p className="model-budget">
                可用于输入的预算：
                <strong>
                  ≤ {count(Math.max(0, o.context_window - o.max_tokens))} Token
                </strong>
                （已预留输出空间）
              </p>
              <small>
                最大输出包含推理消耗，并从上下文窗口中预留。输入按 Token
                估算，实际可用长度以服务商为准。
              </small>
            </section>
            <details className="model-advanced">
              <summary>
                <Icon
                  name="caret-right"
                  size={14}
                  className="disclosure-icon"
                />
                高级配置 <span>推理、采样与协议兼容</span>
              </summary>
              <div className="model-advanced-content">
                <section className="model-form-section">
                  <div className="model-section-title">
                    <h3>推理与采样</h3>
                    <p>留空沿用服务默认值；只开启模型与接口确实支持的能力。</p>
                  </div>
                  <label className="checkbox-label">
                    <input
                      type="checkbox"
                      checked={o.reasoning}
                      onChange={(event) =>
                        options({
                          reasoning: event.target.checked,
                          thinking_level: event.target.checked
                            ? o.thinking_level
                            : "off",
                        })
                      }
                    />
                    模型支持推理
                  </label>
                  <div className="model-fields">
                    {o.reasoning && (
                      <label className="model-field-wide">
                        推理强度
                        <select
                          aria-label="推理强度"
                          value={o.thinking_level}
                          onChange={(event) =>
                            options({
                              thinking_level: event.target
                                .value as ModelOptions["thinking_level"],
                              ...(o.api === "anthropic-messages"
                                ? { temperature: null }
                                : {}),
                            })
                          }
                        >
                          <option value="off">关闭</option>
                          <option value="minimal">最低 · minimal</option>
                          <option value="low">低 · low</option>
                          <option value="medium">中 · medium</option>
                          <option value="high">高 · high</option>
                        </select>
                      </label>
                    )}
                    <label>
                      Temperature
                      <input
                        type="number"
                        step="0.01"
                        min={0}
                        max={o.api === "anthropic-messages" ? 1 : 2}
                        disabled={
                          o.api === "anthropic-messages" &&
                          o.thinking_level !== "off"
                        }
                        value={o.temperature ?? ""}
                        onChange={(event) =>
                          options({
                            temperature:
                              event.target.value === ""
                                ? null
                                : Number(event.target.value),
                          })
                        }
                        placeholder="服务默认"
                      />
                    </label>
                    {o.api !== "anthropic-messages" && (
                      <label>
                        Top P
                        <input
                          type="number"
                          step="0.01"
                          min={0.01}
                          max={1}
                          value={o.top_p ?? ""}
                          onChange={(event) =>
                            options({
                              top_p:
                                event.target.value === ""
                                  ? null
                                  : Number(event.target.value),
                            })
                          }
                          placeholder="服务默认"
                        />
                      </label>
                    )}
                  </div>
                </section>
                {o.api === "openai-completions" && (
                  <details className="model-compat">
                    <summary>
                      <Icon
                        name="caret-right"
                        size={14}
                        className="disclosure-icon"
                      />
                      协议兼容设置
                    </summary>
                    <p>仅在服务商明确要求时覆盖 SDK 的自动识别。</p>
                    <label>
                      输出长度字段
                      <select
                        aria-label="输出长度字段"
                        value={o.max_tokens_field}
                        onChange={(event) =>
                          options({
                            max_tokens_field: event.target
                              .value as ModelOptions["max_tokens_field"],
                          })
                        }
                      >
                        <option value="">自动识别</option>
                        <option value="max_tokens">max_tokens</option>
                        <option value="max_completion_tokens">
                          max_completion_tokens
                        </option>
                      </select>
                    </label>
                  </details>
                )}
              </div>
            </details>
            <footer className="model-form-actions">
              <div className="form-actions">
                <button className="primary">
                  {pending
                    ? "正在处理…"
                    : first
                      ? "保存并开始对话"
                      : "保存模型连接"}
                </button>
                {editor.id > 0 && (
                  <button
                    type="button"
                    disabled={dirty}
                    title={
                      dirty
                        ? "请先保存更改，再测试连接"
                        : "发送简短请求测试已保存的连接"
                    }
                    onClick={() =>
                      void action(async () => {
                        const result = await request<{ message: string }>(
                          `/models/${editor.id}/test`,
                          "POST",
                        );
                        setFeedback(result.message);
                      })
                    }
                  >
                    测试模型连接
                  </button>
                )}
              </div>
              <small>配置用于新提交的任务。连接测试会发送一条简短文本。</small>
            </footer>
          </fieldset>
          {editor.id > 1 && !editor.archived && (
            <details className="agent-archive">
              <summary>
                <Icon
                  name="caret-right"
                  size={14}
                  className="disclosure-icon"
                />
                归档模型连接
              </summary>
              <p>先解除所有未归档 Agent 的关联。归档保留历史和已提交任务。</p>
              <button
                type="button"
                disabled={pending}
                onClick={() =>
                  void action(async () => {
                    await request(`/models/${editor.id}`, "DELETE");
                    setEditor({ ...editor, archived: true });
                    setKey("");
                    await onSaved();
                    setFeedback("模型连接已归档，已提交任务仍使用其独立快照。");
                  })
                }
              >
                确认归档模型连接
              </button>
            </details>
          )}
        </form>
        {error && (
          <p className="error model-feedback" role="alert">
            {error}
          </p>
        )}
        {feedback && (
          <p className="success model-feedback" role="status">
            {feedback}
          </p>
        )}
      </section>
    </div>
  );
}
