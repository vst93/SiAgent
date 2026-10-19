import {
  selectContext,
  estimateSelectedTokens,
  type ContextPolicy,
} from "./context.js";
import { estimateContextTokens } from "@earendil-works/pi-ai/utils/estimate";
import { createModels, createProvider } from "@earendil-works/pi-ai/models";
import type {
  StreamFunction,
  SimpleStreamOptions,
  TranscriptContext,
} from "@earendil-works/pi-ai";
import { createAssistantMessageEventStream } from "@earendil-works/pi-ai";
import * as completions from "@earendil-works/pi-ai/api/openai-completions";
import * as responses from "@earendil-works/pi-ai/api/openai-responses";
import * as anthropic from "@earendil-works/pi-ai/api/anthropic-messages";
export type ModelOptions = {
  display_name?: string;
  api?: "openai-completions" | "openai-responses" | "anthropic-messages";
  context_window?: number;
  max_tokens?: number;
  reasoning?: boolean;
  thinking_level?: "off" | "minimal" | "low" | "medium" | "high";
  temperature?: number | null;
  top_p?: number | null;
  max_tokens_field?: "" | "max_tokens" | "max_completion_tokens";
  input?: ("text" | "image")[];
};
export type ModelConfig = {
  provider?: string;
  base_url: string;
  name: string;
  api_key: string;
  options?: ModelOptions;
  fallbacks?: ModelConfig[];
};
type Attempt = {
  model: {
    id: string;
    name: string;
    provider: string;
    api: string;
    baseUrl: string;
    reasoning: boolean;
    input: ("text" | "image")[];
    contextWindow: number;
    maxTokens: number;
    cost: { input: number; output: number; cacheRead: number; cacheWrite: number };
  };
  stream: StreamFunction;
  simple: StreamFunction<string, SimpleStreamOptions>;
};

function makeAttempt(
  config: ModelConfig,
  check: (context: unknown, failure?: string) => void,
  policy: ContextPolicy | undefined,
  suffix = "",
): Attempt {
  const o = {
    api: "openai-completions",
    context_window: 32768,
    max_tokens: 4096,
    thinking_level: "off",
    ...config.options,
  } as const;
  const adapter =
    o.api === "openai-completions"
      ? completions
      : o.api === "openai-responses"
        ? responses
        : o.api === "anthropic-messages"
          ? anthropic
          : null;
  if (
    !adapter ||
    !Number.isSafeInteger(o.context_window) ||
    !Number.isSafeInteger(o.max_tokens) ||
    o.max_tokens < 16 ||
    o.max_tokens >= o.context_window
  )
    throw new Error("invalid_model_options");
  const settings = {
    maxTokens: o.max_tokens,
    ...(o.temperature != null ? { temperature: o.temperature } : {}),
    ...(o.top_p != null ? { samplingParams: { top_p: o.top_p } } : {}),
  };
  const now = Date.now();
  const prepare = (context: TranscriptContext) =>
    policy
      ? selectContext(context, policy, o.context_window - o.max_tokens, now)
      : context;
  const guard = (context: TranscriptContext) => {
    const failure =
      Buffer.byteLength(JSON.stringify(context)) > 8 * 1024 * 1024
        ? "request_size_limit"
        : (policy?.strategy === "recent" || policy?.strategy === "time"
              ? estimateSelectedTokens(context)
              : estimateContextTokens(context).tokens) +
              o.max_tokens >
            o.context_window
          ? "context_limit"
          : undefined;
    check(context, failure);
    if (failure) throw new Error(failure);
  };
  const baseUrl =
    o.api === "anthropic-messages"
      ? config.base_url.replace(/\/v1\/?$/, "")
      : config.base_url;
  const model: Attempt["model"] = {
    id: config.name + suffix,
    name: o.display_name || config.name,
    provider: config.provider || "siagent",
    api: o.api,
    baseUrl,
    reasoning: o.reasoning ?? false,
    input: o.input ?? ["text"],
    contextWindow: o.context_window,
    maxTokens: o.max_tokens,
    ...(o.api === "openai-completions" && o.max_tokens_field
      ? { compat: { maxTokensField: o.max_tokens_field } }
      : {}),
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
  };
  const stream: StreamFunction = (requestModel, context, options) => {
    context = prepare(context);
    guard(context);
    return (adapter.stream as StreamFunction)(requestModel, context, {
      ...options,
      ...settings,
      apiKey: config.api_key,
      maxTokens: Math.min(options?.maxTokens ?? o.max_tokens, o.max_tokens),
    });
  };
  const simple: StreamFunction<string, SimpleStreamOptions> = (
    requestModel,
    context,
    options,
  ) => {
    context = prepare(context);
    guard(context);
    return (adapter.streamSimple as StreamFunction<string, SimpleStreamOptions>)(
      requestModel,
      context,
      {
        ...options,
        ...settings,
        apiKey: config.api_key,
        maxTokens: Math.min(options?.maxTokens ?? o.max_tokens, o.max_tokens),
        reasoning:
          o.reasoning && o.thinking_level !== "off"
            ? o.thinking_level
            : undefined,
      },
    );
  };
  return { model, stream, simple };
}

function errorMessage(error: any) {
  if (error && error.role === "assistant") return error;
  return {
    role: "assistant",
    content: [],
    api: "openai-completions",
    provider: "siagent",
    model: "fallback",
    usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } },
    stopReason: "error",
    errorMessage: error instanceof Error ? error.message : String(error),
    timestamp: Date.now(),
  };
}

export function configuredModels(
  config: ModelConfig,
  check: (context: unknown, failure?: string) => void,
  policy?: ContextPolicy,
) {
  const primary = makeAttempt(config, check, policy);
  const fallbacks = (config.fallbacks ?? []).map((fallback, index) =>
    makeAttempt(fallback, check, policy, `#fallback${index + 1}`),
  );
  const attempts = [primary, ...fallbacks];

  const relay = (
    context: TranscriptContext,
    options: SimpleStreamOptions | undefined,
    simple: boolean,
  ) => {
    const out = createAssistantMessageEventStream();
    queueMicrotask(async () => {
      let lastError: any;
      for (const attempt of attempts) {
        try {
          const source = simple
            ? attempt.simple(attempt.model, context, options)
            : attempt.stream(attempt.model, context, options);
          let start: any;
          let committed = false;
          let content = false;
          let finalMessage: any;
          const commit = () => {
            if (committed) return;
            if (start) out.push(start);
            committed = true;
          };
          for await (const event of source) {
            if (event.type === "start") {
              start = event;
              continue;
            }
            if (event.type === "error") {
              lastError = event.error;
              if (content) {
                commit();
                out.push(event);
                out.end(event.error);
                return;
              }
              break;
            }
            if (event.type === "done") {
              commit();
              out.push(event);
              finalMessage = event.message;
              out.end(finalMessage);
              return;
            }
            commit();
            content = true;
            out.push(event);
            if (event.type === "text_start" || event.type === "toolcall_start") content = true;
          }
          if (committed) {
            // A provider ended without a terminal event; surface the failure
            // rather than retrying after partial content.
            const message = errorMessage(lastError ?? new Error("model_stream_incomplete"));
            out.push({ type: "error", reason: "error", error: message });
            out.end(message);
            return;
          }
        } catch (error) {
          lastError = error;
          const message = error instanceof Error ? error.message : String(error);
          if (message === "context_limit" || message === "request_size_limit") break;
        }
      }
      const message = errorMessage(lastError ?? new Error("model_unavailable"));
      out.push({ type: "error", reason: "error", error: message });
      out.end(message);
    });
    return out;
  };

  const models = createModels();
  models.setProvider(
    createProvider({
      id: primary.model.provider,
      baseUrl: primary.model.baseUrl,
      auth: {
        apiKey: {
          name: "Configured API key",
          resolve: async () =>
            config.api_key ? { auth: { apiKey: config.api_key } } : undefined,
        },
      },
      models: [primary.model as any],
      api: {
        stream: ((_model, context, options) =>
          relay(context, options as SimpleStreamOptions, false)) as StreamFunction,
        streamSimple: ((_model, context, options) =>
          relay(context, options, true)) as StreamFunction<string, SimpleStreamOptions>,
      },
    }),
  );
  return models;
}
