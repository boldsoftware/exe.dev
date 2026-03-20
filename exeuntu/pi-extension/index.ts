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
        id: "accounts/fireworks/models/qwen3-coder-480b-a35b-instruct",
        name: "Qwen3 Coder 480B (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.45, output: 1.8, cacheRead: 0.22, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/qwen3-235b-a22b",
        name: "Qwen3 235B (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.22, output: 0.88, cacheRead: 0.11, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/qwen3-8b",
        name: "Qwen3 8B (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.05, output: 0.2, cacheRead: 0.02, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/qwen3-vl-235b-a22b-thinking",
        name: "Qwen3 VL 235B Thinking (Fireworks)",
        reasoning: true,
        input: ["text", "image"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.22, output: 0.88, cacheRead: 0.11, cacheWrite: 0 },
        compat: { ...fwCompat, thinkingFormat: "qwen" as const },
      },
      {
        id: "accounts/fireworks/models/qwen2p5-vl-32b-instruct",
        name: "Qwen2.5 VL 32B (Fireworks)",
        reasoning: false,
        input: ["text", "image"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.2, output: 0.2, cacheRead: 0.1, cacheWrite: 0 },
        compat: fwCompat,
      },

      // GLM
      {
        id: "accounts/fireworks/models/glm-4p7",
        name: "GLM 4.7 (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.6, output: 2.2, cacheRead: 0.3, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/glm-4p6",
        name: "GLM 4.6 (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.55, output: 2.19, cacheRead: 0.27, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/glm-4p5",
        name: "GLM 4.5 (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.55, output: 2.19, cacheRead: 0.27, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/glm-4p5-air",
        name: "GLM 4.5 Air (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.22, output: 0.88, cacheRead: 0.11, cacheWrite: 0 },
        compat: fwCompat,
      },

      // Kimi
      {
        id: "accounts/fireworks/models/kimi-k2p5",
        name: "Kimi K2.5 (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.6, output: 3.0, cacheRead: 0.3, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/kimi-k2-thinking",
        name: "Kimi K2 Thinking (Fireworks)",
        reasoning: true,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.6, output: 2.5, cacheRead: 0.3, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/kimi-k2-instruct",
        name: "Kimi K2 Instruct (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 1.0, output: 3.0, cacheRead: 0.5, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/kimi-k2-instruct-0905",
        name: "Kimi K2 Instruct 0905 (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 1.0, output: 3.0, cacheRead: 0.5, cacheWrite: 0 },
        compat: fwCompat,
      },

      // DeepSeek
      {
        id: "accounts/fireworks/models/deepseek-v3p2",
        name: "DeepSeek V3p2 (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.56, output: 1.68, cacheRead: 0.28, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/deepseek-v3p1",
        name: "DeepSeek V3p1 (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.56, output: 1.68, cacheRead: 0.28, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/deepseek-v3-0324",
        name: "DeepSeek V3 0324 (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.9, output: 0.9, cacheRead: 0.45, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/deepseek-r1-0528",
        name: "DeepSeek R1 0528 (Fireworks)",
        reasoning: true,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 65536,
        cost: { input: 3.0, output: 8.0, cacheRead: 1.5, cacheWrite: 0 },
        compat: fwCompat,
      },

      // MiniMax
      {
        id: "accounts/fireworks/models/minimax-m2",
        name: "MiniMax M2 (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.3, output: 1.2, cacheRead: 0.15, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/minimax-m2p1",
        name: "MiniMax M2.1 (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.3, output: 1.2, cacheRead: 0.15, cacheWrite: 0 },
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
        cost: { input: 0.15, output: 0.6, cacheRead: 0.07, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/gpt-oss-20b",
        name: "GPT-OSS 20B (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.05, output: 0.2, cacheRead: 0.02, cacheWrite: 0 },
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
      {
        id: "accounts/fireworks/models/llama-v3p1-405b-instruct",
        name: "Llama 3.1 405B (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 3.0, output: 3.0, cacheRead: 1.5, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/llama-v3p1-70b-instruct",
        name: "Llama 3.1 70B (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.9, output: 0.9, cacheRead: 0.45, cacheWrite: 0 },
        compat: fwCompat,
      },
      {
        id: "accounts/fireworks/models/llama-v3p1-8b-instruct",
        name: "Llama 3.1 8B (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 131072,
        maxTokens: 16384,
        cost: { input: 0.2, output: 0.2, cacheRead: 0.1, cacheWrite: 0 },
        compat: fwCompat,
      },

      // Mixtral
      {
        id: "accounts/fireworks/models/mixtral-8x22b-instruct",
        name: "Mixtral 8x22B (Fireworks)",
        reasoning: false,
        input: ["text"],
        contextWindow: 65536,
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
