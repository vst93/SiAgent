import { useEffect, useRef, useState } from "react";

type Api = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
type UpdateStatus = {
  mode: "installer" | "docker" | "managed" | "source";
  supported: boolean;
  current: string;
  latest: string;
  available: boolean;
  phase: string;
  message: string;
  backup?: string;
};
const phases: Record<string, string> = {
  idle: "就绪",
  checking: "检查中",
  downloading: "下载校验中",
  ready: "准备完成",
  restarting: "正在重启",
  completed: "更新完成",
  rolled_back: "已恢复旧版本",
  failed: "更新失败",
};
export function ApplicationUpdates({ request }: { request: Api }) {
  const [status, setStatus] = useState<UpdateStatus | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const restarting = useRef(false);
  const [reconnecting, setReconnecting] = useState(false);
  const requestRef = useRef(request);
  requestRef.current = request;
  useEffect(() => {
    let disposed = false;
    const refresh = async () => {
      try {
        const next = await requestRef.current<UpdateStatus>("/updates");
        if (disposed) return;
        setStatus(next);
        setReconnecting(false);
        if (
          restarting.current &&
          ["completed", "rolled_back"].includes(next.phase)
        ) {
          restarting.current = false;
          window.location.reload();
        }
      } catch (error) {
        if (disposed) return;
        if (restarting.current) setReconnecting(true);
        else
          setError(
            error instanceof Error ? error.message : "无法读取更新状态。",
          );
      }
    };
    void refresh();
    const timer = window.setInterval(() => void refresh(), 2000);
    return () => {
      disposed = true;
      window.clearInterval(timer);
    };
  }, []);
  async function action(path: string, body?: unknown) {
    setPending(true);
    setError("");
    if (path === "apply") restarting.current = true;
    try {
      setStatus(await request<UpdateStatus>(`/updates/${path}`, "POST", body));
      setConfirmed(false);
    } catch (error) {
      if (path === "apply") restarting.current = false;
      setError(
        error instanceof Error ? error.message : "更新操作未完成，请重试。",
      );
    } finally {
      setPending(false);
    }
  }
  if (!status)
    return (
      <section className="application-updates">
        <h2>应用更新</h2>
        <p className="muted">{error || "正在读取更新状态…"}</p>
      </section>
    );
  const busy =
    pending || ["checking", "downloading", "restarting"].includes(status.phase);
  return (
    <section className="application-updates" aria-label="应用更新">
      <h2>应用更新</h2>
      <p>
        当前版本 <strong>{status.current}</strong>
        {status.latest && (
          <>
            {" "}
            · 最新版本 <strong>{status.latest}</strong>
          </>
        )}{" "}
        <span className="muted">{phases[status.phase] ?? status.phase}</span>
      </p>
      <p
        className={
          status.phase === "failed" || status.phase === "rolled_back"
            ? "context-warning"
            : "muted"
        }
        role="status"
      >
        {reconnecting
          ? "更新正在重启服务，暂时断开连接。正在自动重连，请勿手动启动另一实例；成功后页面会自动刷新。"
          : status.message}
      </p>
      {!status.supported && status.mode !== "installer" && (
        <p className="muted">
          检查新版本不会修改当前部署。
          {status.mode === "docker"
            ? "使用原 Compose 文件和数据卷更新；若使用本地构建镜像，请先拉取源码再重新构建。"
            : "独立安装包需通过官方安装脚本安装后，才能使用应用内下载及重启。"}
        </p>
      )}
      <div className="form-actions">
        <button
          type="button"
          disabled={busy || status.phase === "ready"}
          onClick={() => void action("check")}
        >
          检查新版本
        </button>
        {status.supported && status.available && status.phase !== "ready" && (
          <button
            type="button"
            disabled={busy}
            onClick={() => void action("prepare")}
          >
            下载并校验更新
          </button>
        )}
        {status.supported && status.phase === "ready" && (
          <button
            type="button"
            disabled={pending}
            onClick={() => void action("cancel")}
          >
            取消本次更新
          </button>
        )}
      </div>
      {status.supported && status.phase === "ready" && (
        <div className="update-confirmation">
          <p>
            更新期间会短暂停机。系统会等待当前任务和渠道投递结束，关闭服务后备份完整数据，再切换程序并重启；若新版本启动失败，将恢复旧程序和数据备份。
          </p>
          <label className="checkbox-label">
            <input
              type="checkbox"
              checked={confirmed}
              onChange={(event) => setConfirmed(event.target.checked)}
            />
            我确认现在停机备份并更新，期间请勿手动启动另一实例
          </label>
          <button
            type="button"
            className="primary"
            disabled={!confirmed || pending}
            onClick={() => void action("apply", { confirm: true })}
          >
            安装更新并重启
          </button>
        </div>
      )}
      {status.backup && (
        <p className="muted">
          最近更新备份：<code>{status.backup}</code>
          。备份含私密数据，不会自动删除；请按需要离线保存或清理。
        </p>
      )}
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      <small>
        先尝试 GitHub
        官方地址并重试；失败或超时后依次尝试配置的镜像，仍会校验发布包的
        SHA-256。
      </small>
    </section>
  );
}
