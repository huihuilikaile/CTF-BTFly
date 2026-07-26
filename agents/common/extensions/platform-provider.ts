import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

// registerPlatformProvider 将 daemon 注入的短期模型配置注册为 Pi Provider。
// 容器只知道任务 Token，不接触真实上游 API Key。
export default function registerPlatformProvider(pi: ExtensionAPI) {
  const baseUrl = process.env.CTF_MODEL_BASE_URL;
  const apiKey = process.env.CTF_TASK_TOKEN;
  const modelId = process.env.CTF_MODEL_ID ?? "ctf-reasoning";
  const supportsImages = /^(1|true|yes|on)$/i.test(process.env.CTF_MODEL_SUPPORTS_IMAGES ?? "");

  // 配置不完整时不注册半可用 Provider，让 Pi 给出明确的模型缺失错误。
  if (!baseUrl || !apiKey) {
    return;
  }

  // Provider 使用 OpenAI completions 兼容协议，并声明模型支持的输入和上下文能力。
  pi.registerProvider("ctf-gateway", {
    name: "CTF Platform Gateway",
    baseUrl,
    apiKey,
    authHeader: true,
    api: "openai-completions",
    compat: {
      supportsDeveloperRole: false,
      supportsReasoningEffort: true,
    },

    // Token 上限可由镜像运行环境覆盖；本地 cost 固定为零，
    // 实际用量由宿主 daemon 网关统一记录而非由 Pi 推算。
    models: [
      {
        id: modelId,
        name: modelId,
        reasoning: true,
        // Pi 0.81 的动态 Provider 在部分场景不会将 Provider 层 compat
        // 继承给 models。将此项同时固定在模型层，保证 DeepSeek 等只接受
        // system/user/assistant/tool 的 OpenAI 兼容端点永不收到 developer。
        compat: {
          supportsDeveloperRole: false,
          supportsReasoningEffort: true,
        },
        input: supportsImages ? ["text", "image"] : ["text"],
        contextWindow: Number(process.env.CTF_MODEL_CONTEXT ?? 200000),
        maxTokens: Number(process.env.CTF_MODEL_MAX_TOKENS ?? 32768),
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      },
    ],
  });
}
