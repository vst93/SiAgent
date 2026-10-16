import { estimateContextTokens } from "@earendil-works/pi-ai/utils/estimate";
import { createModels, createProvider } from "@earendil-works/pi-ai/models";
import type {
  StreamFunction,
  SimpleStreamOptions,
  TranscriptContext,
} from "@earendil-works/pi-ai";
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
};
export type ModelConfig = {
  base_url: string;
  name: string;
  api_key: string;
  options?: ModelOptions;
};
export function configuredModels(
  config: ModelConfig,
  check: (context: unknown, failure?: string) => void,
) {
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
  const stream = adapter.stream as StreamFunction;
  const simple = adapter.streamSimple as StreamFunction<
    string,
    SimpleStreamOptions
  >;
  const settings = {
    maxTokens: o.max_tokens,
    ...(o.temperature != null ? { temperature: o.temperature } : {}),
    ...(o.top_p != null ? { samplingParams: { top_p: o.top_p } } : {}),
  };
  const guard = (context: TranscriptContext) => {
    // Keep room for entry metadata inside the 16 MiB private protocol frame.
    const failure =
      Buffer.byteLength(JSON.stringify(context)) > 8 * 1024 * 1024
        ? "request_size_limit"
        : estimateContextTokens(context).tokens + o.max_tokens >
            o.context_window
          ? "context_limit"
          : undefined;
    check(context, failure);
    if (failure) throw new Error(failure);
  };
  const checkedStream: StreamFunction = (model, context, options) => {
    guard(context);
    return stream(model, context, { ...options, ...settings });
  };
  const checkedSimple: StreamFunction<string, SimpleStreamOptions> = (
    model,
    context,
    options,
  ) => {
    guard(context);
    return simple(model, context, {
      ...options,
      ...settings,
      reasoning:
        o.reasoning && o.thinking_level !== "off"
          ? o.thinking_level
          : undefined,
    });
  };
  const baseUrl =
    o.api === "anthropic-messages"
      ? config.base_url.replace(/\/v1\/?$/, "")
      : config.base_url;
  const models = createModels();
  models.setProvider(
    createProvider({
      id: "siagent",
      baseUrl,
      auth: {
        apiKey: {
          name: "Configured API key",
          resolve: async () =>
            config.api_key ? { auth: { apiKey: config.api_key } } : undefined,
        },
      },
      models: [
        {
          id: config.name,
          name: o.display_name || config.name,
          provider: "siagent",
          api: o.api,
          baseUrl,
          reasoning: o.reasoning ?? false,
          input: ["text"],
          contextWindow: o.context_window,
          maxTokens: o.max_tokens,
          cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
          ...(o.api === "openai-completions" && o.max_tokens_field
            ? { compat: { maxTokensField: o.max_tokens_field } }
            : {}),
        },
      ],
      api: { stream: checkedStream, streamSimple: checkedSimple },
    }),
  );
  return models;
}
