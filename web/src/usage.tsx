import { Icon } from "./icons";
import { useEffect, useRef, useState } from "react";

type Api = <T>(path: string, method?: string, body?: unknown) => Promise<T>;

type Totals = {
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
  total: number;
  cost: number;
  calls: number;
  reported_calls: number;
};

type Bucket = {
  start: number;
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
  total: number;
  calls: number;
};

type ModelUsage = {
  model: string;
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
  total: number;
  cost: number;
  calls: number;
};

type ConversationUsage = {
  id: string;
  title: string;
  agent_id: string;
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
  total: number;
  cost: number;
  calls: number;
  last_seen: number;
};

type Summary = {
  from: number;
  to: number;
  bucket: "hour" | "day" | "week" | "month";
  totals: Totals;
  series: Bucket[];
  models: ModelUsage[];
  conversations: ConversationUsage[];
};

type RangeKey = "today" | "yesterday" | "7d" | "30d" | "90d" | "all" | "custom";
type Query = { from: number; to: number };

const day = 24 * 60 * 60 * 1000;
const numberFormat = new Intl.NumberFormat("zh-CN");
const compactFormat = new Intl.NumberFormat("zh-CN", {
  notation: "compact",
  maximumFractionDigits: 1,
});

function formatCost(value: number) {
  if (!value) return "$0";
  return value < 1 ? `$${value.toFixed(4)}` : `$${value.toFixed(2)}`;
}

function startOfDay(value: Date) {
  return new Date(value.getFullYear(), value.getMonth(), value.getDate());
}

function endOfDay(value: Date) {
  return new Date(
    value.getFullYear(),
    value.getMonth(),
    value.getDate(),
    23,
    59,
    59,
    999,
  );
}

function dateInput(value: Date) {
  const month = String(value.getMonth() + 1).padStart(2, "0");
  const day = String(value.getDate()).padStart(2, "0");
  return `${value.getFullYear()}-${month}-${day}`;
}

function parseDate(value: string, end: boolean) {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match) return null;
  const date = new Date(
    Number(match[1]),
    Number(match[2]) - 1,
    Number(match[3]),
  );
  if (Number.isNaN(date.getTime())) return null;
  return (end ? endOfDay(date) : startOfDay(date)).getTime();
}

function queryFor(key: RangeKey, from = "", to = ""): Query | null {
  const now = Date.now();
  if (key === "today") return { from: startOfDay(new Date(now)).getTime(), to: now };
  if (key === "yesterday") {
    const value = new Date(now);
    value.setDate(value.getDate() - 1);
    return { from: startOfDay(value).getTime(), to: endOfDay(value).getTime() };
  }
  if (key === "all") return { from: 0, to: now };
  if (key === "custom") {
    const start = parseDate(from, false);
    const finish = parseDate(to, true);
    if (start === null || finish === null || start >= finish) return null;
    return { from: start, to: finish };
  }
  const days = key === "7d" ? 7 : key === "90d" ? 90 : 30;
  return {
    from: startOfDay(new Date(now - (days - 1) * day)).getTime(),
    to: now,
  };
}

function bucketLabel(value: number, bucket: Summary["bucket"]) {
  const date = new Date(value);
  if (bucket === "hour") {
    return `${date.getMonth() + 1}/${date.getDate()} ${String(
      date.getHours(),
    ).padStart(2, "0")}:00`;
  }
  if (bucket === "month") {
    return `${date.getFullYear()}/${date.getMonth() + 1}`;
  }
  return `${date.getMonth() + 1}/${date.getDate()}`;
}

function rangeLabel(from: number, to: number) {
  if (!from) return "全部时间";
  const start = new Date(from);
  const finish = new Date(to);
  const format = (value: Date) =>
    `${value.getFullYear()}/${String(value.getMonth() + 1).padStart(
      2,
      "0",
    )}/${String(value.getDate()).padStart(2, "0")}`;
  return `${format(start)} – ${format(finish)}`;
}

function bucketName(bucket: Summary["bucket"]) {
  return bucket === "hour"
    ? "按小时"
    : bucket === "week"
      ? "按周"
      : bucket === "month"
        ? "按月"
        : "按天";
}

export function UsageWorkspace({ request }: { request: Api }) {
  const now = new Date();
  const [preset, setPreset] = useState<RangeKey>("today");
  const [query, setQuery] = useState<Query | null>(() => queryFor("today"));
  const [fromDate, setFromDate] = useState(dateInput(new Date(Date.now() - 29 * day)));
  const [toDate, setToDate] = useState(dateInput(now));
  const [data, setData] = useState<Summary | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const sequence = useRef(0);

  async function load(next: Query) {
    const current = ++sequence.current;
    setLoading(true);
    setError("");
    try {
      const summary = await request<Summary>(
        `/usage?from=${next.from}&to=${next.to}`,
      );
      if (current === sequence.current) setData(summary);
    } catch (failure) {
      if (current === sequence.current)
        setError(
          failure instanceof Error ? failure.message : "用量读取失败，请重试。",
        );
    } finally {
      if (current === sequence.current) setLoading(false);
    }
  }

  useEffect(() => {
    if (query) void load(query);
  }, [query]);

  function choose(next: RangeKey) {
    setPreset(next);
    const value = queryFor(next, fromDate, toDate);
    if (!value) {
      setError("结束日期必须晚于开始日期。");
      return;
    }
    setQuery(value);
  }

  function applyCustom() {
    const value = queryFor("custom", fromDate, toDate);
    if (!value) {
      setError("结束日期必须晚于开始日期。");
      return;
    }
    setPreset("custom");
    setQuery(value);
  }

  const totals = data?.totals;
  const series = data?.series ?? [];
  const max = Math.max(...series.map((item) => item.total), 0);
  const axisMax = max || 1;
  const step = Math.max(1, Math.ceil(series.length / 6));
  const labelIndices: number[] = [];
  for (let index = 0; index < series.length; index += step) {
    labelIndices.push(index);
  }
  if (series.length > 1) {
    const last = series.length - 1;
    if (labelIndices[labelIndices.length - 1] !== last) {
      if (last - labelIndices[labelIndices.length - 1] < step) {
        labelIndices[labelIndices.length - 1] = last;
      } else {
        labelIndices.push(last);
      }
    }
  }
  const hasUsage = (totals?.total ?? 0) > 0;
  const range = data ? rangeLabel(data.from, data.to) : "";

  return (
    <div className="usage-workspace">
      <div className="usage-toolbar">
        <div className="usage-presets" role="group" aria-label="统计时间范围">
          {(
            [
              ["today", "今日"],
              ["yesterday", "昨日"],
              ["7d", "近 7 天"],
              ["30d", "近 30 天"],
              ["90d", "近 90 天"],
              ["all", "全部"],
              ["custom", "自定义"],
            ] as const
          ).map(([key, label]) => (
            <button
              key={key}
              type="button"
              className={preset === key ? "selected" : ""}
              aria-pressed={preset === key}
              onClick={() => choose(key)}
            >
              {label}
            </button>
          ))}
        </div>
        <div className="usage-range">
          {preset === "custom" ? (
            <div className="usage-custom-range">
              <label>
                <span>从</span>
                <input
                  type="date"
                  value={fromDate}
                  max={toDate}
                  onChange={(event) => setFromDate(event.target.value)}
                />
              </label>
              <label>
                <span>到</span>
                <input
                  type="date"
                  value={toDate}
                  min={fromDate}
                  onChange={(event) => setToDate(event.target.value)}
                />
              </label>
              <button type="button" onClick={applyCustom}>
                应用
              </button>
            </div>
          ) : (
            <span className="usage-range-label">{range}</span>
          )}
          {loading && data && <span className="usage-updating">正在更新…</span>}
        </div>
      </div>

      {error && (
        <div className="usage-error" role="alert">
          <span>{error}</span>
          <button type="button" onClick={() => query && void load(query)}>
            重试
          </button>
        </div>
      )}

      {!data && loading ? (
        <div className="usage-loading" role="status">
          <span />
          <span />
          <span />
        </div>
      ) : (
        <>
          <section className="usage-metrics" aria-label="Token 用量汇总">
            <div>
              <span>总 tokens</span>
              <strong>{numberFormat.format(totals?.total ?? 0)}</strong>
              <small>含缓存与工具轮次</small>
            </div>
            <div>
              <span>非缓存输入</span>
              <strong>{numberFormat.format(totals?.input ?? 0)}</strong>
              <small>提示与上下文</small>
            </div>
            <div>
              <span>命中缓存</span>
              <strong>{numberFormat.format(totals?.cache_read ?? 0)}</strong>
              <small>读取已有前缀</small>
            </div>
            <div>
              <span>缓存写入</span>
              <strong>{numberFormat.format(totals?.cache_write ?? 0)}</strong>
              <small>写入新前缀</small>
            </div>
            <div>
              <span>输出</span>
              <strong>{numberFormat.format(totals?.output ?? 0)}</strong>
              <small>回答与工具轮次</small>
            </div>
            <div>
              <span>调用次数</span>
              <strong>{numberFormat.format(totals?.calls ?? 0)}</strong>
              <small>
                {totals && totals.calls > totals.reported_calls ? (
                  <>
                    {totals.calls - totals.reported_calls}{" "}
                    <span>次按 0 记录</span>
                  </>
                ) : (
                  "全部返回用量"
                )}
              </small>
            </div>
            <div>
              <span>有计量调用</span>
              <strong>{numberFormat.format(totals?.reported_calls ?? 0)}</strong>
              <small>返回 usage 的调用</small>
            </div>
            <div>
              <span>预估成本</span>
              <strong>{formatCost(totals?.cost ?? 0)}</strong>
              <small>USD · 未返回价格按 0</small>
            </div>
          </section>

          <section className="usage-panel usage-trend" aria-label="Token 趋势">
            <header className="usage-panel-head">
              <div>
                <h2>趋势</h2>
                <span>
                  {data ? bucketName(data.bucket) : "按天"}
                  <span className="usage-trend-range"> · {range}</span>
                </span>
              </div>
              <div className="usage-legend" aria-hidden="true">
                <span className="usage-legend-input">非缓存</span>
                <span className="usage-legend-cache-hit">命中</span>
                <span className="usage-legend-cache-write">写入</span>
                <span className="usage-legend-output">输出</span>
              </div>
            </header>
            <div className="usage-chart">
              <div className="usage-axis" aria-hidden="true">
                <span>{max > 0 ? compactFormat.format(max) : ""}</span>
                <span>{max > 0 ? compactFormat.format(Math.round(max / 2)) : ""}</span>
                <span>0</span>
              </div>
              <div
                className="usage-plot"
                role="img"
                aria-label={`${range} 的 token 趋势，共 ${series.length} 个时间点`}
              >
                <div className="usage-grid" aria-hidden="true">
                  <i />
                  <i />
                  <i />
                </div>
                <div className="usage-bars">
                  {series.map((item) => (
                    <div
                      className="usage-chart-column"
                      key={item.start}
                      title={`${bucketLabel(item.start, data?.bucket ?? "day")} · 非缓存 ${numberFormat.format(
                        item.input,
                      )} · 命中缓存 ${numberFormat.format(
                        item.cache_read,
                      )} · 缓存写入 ${numberFormat.format(
                        item.cache_write,
                      )} · 输出 ${numberFormat.format(
                        item.output,
                      )} · 合计 ${numberFormat.format(item.total)}`}
                    >
                      <div
                        className="usage-chart-bar"
                        style={{
                          height: `${item.total > 0 ? Math.max(2, (item.total / axisMax) * 100) : 0}%`,
                        }}
                      >
                        {item.total > 0 &&
                          (item.input + item.output + item.cache_read + item.cache_write > 0 ? (
                            <>
                              <span
                                className="usage-chart-input"
                                style={{ flex: item.input }}
                              />
                              <span
                                className="usage-chart-cache-hit"
                                style={{ flex: item.cache_read }}
                              />
                              <span
                                className="usage-chart-cache-write"
                                style={{ flex: item.cache_write }}
                              />
                              <span
                                className="usage-chart-output"
                                style={{ flex: item.output }}
                              />
                            </>
                          ) : (
                            <span
                              className="usage-chart-output"
                              style={{ flex: 1 }}
                            />
                          ))}
                      </div>
                    </div>
                  ))}
                </div>
                <div className="usage-chart-labels" aria-hidden="true">
                  {labelIndices.map((index) => {
                    const item = series[index];
                    return (
                      <span
                        key={item.start}
                        className={
                          index === 0 && series.length > 1
                            ? "first"
                            : index === series.length - 1
                              ? "last"
                              : ""
                        }
                        style={{
                          left: `${((index + 0.5) / series.length) * 100}%`,
                        }}
                      >
                        {bucketLabel(item.start, data?.bucket ?? "day")}
                      </span>
                    );
                  })}
                </div>
              </div>
            </div>
            {!hasUsage && (
              <p className="usage-empty">所选时间内没有模型调用记录。</p>
            )}
          </section>

          <div className="usage-tables">
            <section className="usage-panel" aria-label="按模型统计">
              <header className="usage-panel-head">
                <h2>按模型</h2>
                <span>{data?.models.length ?? 0} 个</span>
              </header>
              {data?.models.length ? (
                <table className="usage-table">
                  <thead>
                    <tr>
                      <th>模型</th>
                      <th>调用</th>
                      <th>tokens</th>
                      <th>成本</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.models.map((item) => (
                      <tr
                        key={item.model}
                        title={`非缓存 ${numberFormat.format(
                          item.input,
                        )} · 命中缓存 ${numberFormat.format(
                          item.cache_read,
                        )} · 缓存写入 ${numberFormat.format(
                          item.cache_write,
                        )} · 输出 ${numberFormat.format(item.output)} · 成本 ${formatCost(
                          item.cost,
                        )}`}
                      >
                        <td title={item.model}>{item.model}</td>
                        <td>{numberFormat.format(item.calls)}</td>
                        <td>{numberFormat.format(item.total)}</td>
                        <td>{formatCost(item.cost)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              ) : (
                <p className="usage-empty">所选时间内没有模型调用记录。</p>
              )}
            </section>

            <section className="usage-panel" aria-label="按会话统计">
              <header className="usage-panel-head">
                <h2>会话消耗</h2>
                <span>Top {Math.min(data?.conversations.length ?? 0, 12)}</span>
              </header>
              {data?.conversations.length ? (
                <table className="usage-table">
                  <thead>
                    <tr>
                      <th>会话</th>
                      <th>调用</th>
                      <th>tokens</th>
                      <th>成本</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.conversations.map((item) => (
                      <tr
                        key={item.id}
                        title={`非缓存 ${numberFormat.format(
                          item.input,
                        )} · 命中缓存 ${numberFormat.format(
                          item.cache_read,
                        )} · 缓存写入 ${numberFormat.format(
                          item.cache_write,
                        )} · 输出 ${numberFormat.format(item.output)} · 成本 ${formatCost(
                          item.cost,
                        )}`}
                      >
                        <td title={item.title}>{item.title}</td>
                        <td>{numberFormat.format(item.calls)}</td>
                        <td>{numberFormat.format(item.total)}</td>
                        <td>{formatCost(item.cost)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              ) : (
                <p className="usage-empty">所选时间内没有会话消耗记录。</p>
              )}
            </section>
          </div>

          <p className="usage-note">
            <Icon name="chart" size={14} />
            数据来自模型服务返回的 usage；非缓存输入、命中缓存、缓存写入与输出分开统计。未返回用量时 tokens 按 0 记录；适配器未返回价格时成本按 0 记录。
          </p>
        </>
      )}
    </div>
  );
}
