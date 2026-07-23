import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

export default function registerPlatformProvider(pi: ExtensionAPI) {
  const baseUrl = process.env.CTF_MODEL_BASE_URL;
  const apiKey = process.env.CTF_TASK_TOKEN;
  const modelId = process.env.CTF_MODEL_ID ?? "ctf-reasoning";

  if (!baseUrl || !apiKey) {
    return;
  }

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
    models: [
      {
        id: modelId,
        name: modelId,
        reasoning: true,
        input: ["text", "image"],
        contextWindow: Number(process.env.CTF_MODEL_CONTEXT ?? 200000),
        maxTokens: Number(process.env.CTF_MODEL_MAX_TOKENS ?? 32768),
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      },
    ],
  });
}

