import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";
import { existsSync } from "node:fs";

const GATEWAY = "http://169.254.169.254/gateway/llm";

// Fireworks uses the OpenAI chat completions API but doesn't support
// the developer role or max_completion_tokens.
const fwCompat = {
  supportsDeveloperRole: false,
  maxTokensField: "max_tokens" as const,
};

export default function (pi: ExtensionAPI) {
  // Only activate on exe.dev VMs.
  if (!existsSync("/exe.dev")) return;

  // Route Anthropic, OpenAI, and Fireworks models through the exe.dev LLM gateway.
  pi.registerProvider("anthropic", {
    baseUrl: `${GATEWAY}/anthropic`,
    apiKey: "gateway",
  });
  pi.registerProvider("openai", {
    baseUrl: `${GATEWAY}/openai/v1`,
    apiKey: "gateway",
  });
  pi.registerProvider("fireworks", {
    baseUrl: `${GATEWAY}/fireworks/inference/v1`,
    apiKey: "gateway",
    api: "openai-completions",
    models: [
      // Qwen
      {
        id: "accounts/fireworks/models/qwen3-8b",
        name: "Qwen3 8B (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 40960,
        maxTokens: 16384,
        cost: { input: 0.2, output: 0.2, cacheRead: 0.1, cacheWrite: 0 },
        compat: fwCompat,
      },

      // GLM
      {
        id: "accounts/fireworks/models/glm-5p1",
        name: "GLM 5.1 (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 202752,
        maxTokens: 16384,
        cost: { input: 1.4, output: 4.4, cacheRead: 0.26, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/glm-5",
        name: "GLM 5 (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 202752,
        maxTokens: 16384,
        cost: { input: 1.0, output: 3.2, cacheRead: 0.2, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/glm-4p7",
        name: "GLM 4.7 (Fireworks)",
        reasoning: true,
        input: ["text"],
        contextWindow: 202752,
        maxTokens: 16384,
        cost: { input: 0.6, output: 2.2, cacheRead: 0.3, cacheWrite: 0 },
        compat: { ...fwCompat, thinkingFormat: "openai" as const },
      },

      // Kimi
      {
        id: "accounts/fireworks/models/kimi-k2p6",
        name: "Kimi K2.6 (Fireworks)",
        reasoning: true,
        input: ["text", "image"],
        contextWindow: 262144,
        maxTokens: 16384,
        cost: { input: 0.95, output: 4.0, cacheRead: 0.16, cacheWrite: 0 },
        compat: { ...fwCompat, thinkingFormat: "openai" as const },
      },
      {
        id: "accounts/fireworks/models/kimi-k2p5",
        name: "Kimi K2.5 (Fireworks)",
        reasoning: true,
        input: ["text", "image"],
        contextWindow: 262144,
        maxTokens: 16384,
        cost: { input: 0.6, output: 3.0, cacheRead: 0.1, cacheWrite: 0 },
        compat: { ...fwCompat, thinkingFormat: "openai" as const },
      },

      // DeepSeek
      {
        id: "accounts/fireworks/models/deepseek-v3p2",
        name: "DeepSeek V3p2 (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 163840,
        maxTokens: 16384,
        cost: { input: 0.56, output: 1.68, cacheRead: 0.28, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/deepseek-v3p1",
        name: "DeepSeek V3p1 (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 163840,
        maxTokens: 16384,
        cost: { input: 0.56, output: 1.68, cacheRead: 0.28, cacheWrite: 0 },
        compat: fwCompat,
      },

      // MiniMax
      {
        id: "accounts/fireworks/models/minimax-m2p5",
        name: "MiniMax M2.5 (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 196608,
        maxTokens: 16384,
        cost: { input: 0.3, output: 1.2, cacheRead: 0.03, cacheWrite: 0 },
        compat: fwCompat,
      },

      // GPT-OSS
      {
        id: "accounts/fireworks/models/gpt-oss-120b",
        name: "GPT-OSS 120B (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.15, output: 0.6, cacheRead: 0.01, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/gpt-oss-20b",
        name: "GPT-OSS 20B (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.07, output: 0.3, cacheRead: 0.04, cacheWrite: 0 },
        compat: fwCompat,
      },

      // Llama
      {
        id: "accounts/fireworks/models/llama-v3p3-70b-instruct",
        name: "Llama 3.3 70B (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.9, output: 0.9, cacheRead: 0.45, cacheWrite: 0 },
        compat: fwCompat,
      },
    ],
  });

  // Inject exe.dev context into the system prompt.
  pi.on("before_agent_start", async (event) => {
    return {
      systemPrompt:
        event.systemPrompt +
        `

You are running inside an exe.dev VM, which provides HTTPS proxy, auth, email, and more. Docs index: https://exe.dev/docs.md

`,
    };
  });
}
