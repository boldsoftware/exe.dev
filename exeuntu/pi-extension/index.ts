import type { ExtensionAPI, ProviderConfig, ProviderModelConfig } from "@mariozechner/pi-coding-agent";
import { existsSync, mkdirSync, readFileSync, renameSync, unlinkSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// LLM gateway base, reachable inside every exe.dev VM. The metadata server
// rewrites /gateway/llm/X to /_/gateway/X (exelet/metadata/metadata.go) and
// forwards to the gateway, which serves the catalog at /_/gateway/models.json
// before the credit check (llmgateway/gateway.go).
const GATEWAY = "http://169.254.169.254/gateway/llm";
const CATALOG_URL = `${GATEWAY}/models.json`;

// Catalog freshness strategy: the on-disk cache is the preferred source;
// the bundled catalog.json shipped with the extension image is the fallback;
// after registering providers we refresh the cache in the background so the
// next launch picks up new models, pricing, or compat hints.
const HERE = dirname(fileURLToPath(import.meta.url));
const BUNDLED_CATALOG = join(HERE, "catalog.json");
const CACHE_DIR = join(homedir(), ".cache", "pi-exe-dev");
const CACHE_FILE = join(CACHE_DIR, "catalog.json");

// Keep in lockstep with llmpricing.CatalogSchemaVersion. Any breaking change
// there requires shipping a new exe-dev pi extension that knows the new shape;
// until then the extension treats the catalog as unavailable and falls back.
const SCHEMA_VERSION = 1;

// In-VM fetches go to a link-local address with no DNS, so anything beyond a
// short budget is almost certainly stuck. The fetch is non-blocking (it only
// updates the on-disk cache for the next launch), so a tight timeout is fine.
const FETCH_TIMEOUT_MS = 1500;

// Allowlists for fields we forward into pi-ai. Forwarding an unknown value
// would silently misconfigure the model, so we drop it and let pi-ai keep its
// auto-detected default. Keep these in sync with @mariozechner/pi-ai's types.
// pi-ai's Api type is `KnownApi | (string & {})`, which permits any string
// for forward compatibility. We don't get static checking for this list, so
// keep it in sync with pi-ai's KnownApi by hand.
const KNOWN_APIS: ReadonlySet<string> = new Set([
  "openai-completions",
  "openai-responses",
  "openai-codex-responses",
  "azure-openai-responses",
  "anthropic-messages",
  "bedrock-converse-stream",
  "google-generative-ai",
  "google-vertex",
  "mistral-conversations",
]);
const KNOWN_THINKING_FORMATS: ReadonlySet<string> = new Set([
  "openai",
  "openrouter",
  "deepseek",
  "zai",
  "qwen",
  "qwen-chat-template",
]);
const KNOWN_MAX_TOKENS_FIELDS: ReadonlySet<string> = new Set(["max_tokens", "max_completion_tokens"]);
const KNOWN_CACHE_CONTROL_FORMATS: ReadonlySet<string> = new Set(["anthropic"]);

// pi-ai's Model.compat is a discriminated union keyed on the model's Api
// type, so generic reads/writes are not type-safe. We build a structural bag
// of fields we know about and cast at the boundary when assigning to
// ProviderModelConfig.compat. The cast is sound as long as the field types
// here remain a subset of pi-ai's compat shapes.
type CompatBag = {
  supportsDeveloperRole?: boolean;
  supportsReasoningEffort?: boolean;
  maxTokensField?: "max_tokens" | "max_completion_tokens";
  thinkingFormat?: "openai" | "openrouter" | "deepseek" | "zai" | "qwen" | "qwen-chat-template";
  cacheControlFormat?: "anthropic";
};

interface CatalogCompat {
  supportsDeveloperRole?: boolean;
  maxTokensField?: string;
  supportsReasoningEffort?: boolean;
  thinkingFormat?: string;
  cacheControlFormat?: string;
}

interface CatalogModel {
  id: string;
  name?: string;
  type?: string;
  reasoning?: boolean;
  input?: ("text" | "image")[];
  contextWindow?: number;
  maxTokens?: number;
  cost: { input: number; output: number; cacheRead: number; cacheWrite: number };
  compat?: CatalogCompat;
}

interface CatalogProvider {
  id: string;
  path: string;
  api?: string;
  models: CatalogModel[];
}

interface Catalog {
  schemaVersion: number;
  providers: CatalogProvider[];
}

// validCatalog is a structural check that rejects shapes we cannot safely
// register from. It deliberately does not validate every field; per-provider
// and per-model errors are caught later in the registration loop.
function validCatalog(cat: unknown): cat is Catalog {
  if (!cat || typeof cat !== "object") return false;
  const c = cat as Catalog;
  if (c.schemaVersion !== SCHEMA_VERSION) return false;
  if (!Array.isArray(c.providers) || c.providers.length === 0) return false;
  return true;
}

// loadCatalogSync returns the freshest usable catalog, or null if none. The
// on-disk cache wins over the bundled fallback so users pick up updates
// without waiting for a new image. Synchronous so providers are registered
// before the factory returns and pi flushes them.
//
// A bad cache file (corrupt JSON or unknown schema) is removed eagerly: the
// bundled fallback is always available, and removing the bad file means a
// chronically failing refresh doesn't leave us logging the same warning
// every launch.
function loadCatalogSync(): { catalog: Catalog; source: string } | null {
  for (const path of [CACHE_FILE, BUNDLED_CATALOG]) {
    if (!existsSync(path)) continue;
    let reason: string | undefined;
    try {
      const cat = JSON.parse(readFileSync(path, "utf8")) as unknown;
      if (validCatalog(cat)) return { catalog: cat, source: path };
      reason = "schemaVersion or shape mismatch";
    } catch (err) {
      reason = (err as Error).message;
    }
    console.warn(`[pi-exe-dev] ignoring ${path}: ${reason}`);
    if (path === CACHE_FILE) {
      try {
        unlinkSync(path);
      } catch {
        // best-effort cleanup; bundled fallback still works
      }
    }
  }
  return null;
}

// refreshCatalogAsync fetches the latest catalog and writes it to the cache
// file for use on the next pi launch. We deliberately do not re-register
// providers in this session: pi.registerProvider supports it, but races with
// in-flight model selection are not worth the risk for a once-per-launch
// freshness gain. Failures are logged once and never propagate.
async function refreshCatalogAsync(): Promise<void> {
  let text: string;
  try {
    const res = await fetch(CATALOG_URL, { signal: AbortSignal.timeout(FETCH_TIMEOUT_MS) });
    if (!res.ok) {
      console.warn(`[pi-exe-dev] catalog fetch returned HTTP ${res.status}`);
      return;
    }
    text = await res.text();
  } catch (err) {
    console.warn(`[pi-exe-dev] catalog fetch failed: ${(err as Error).message}`);
    return;
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch (err) {
    console.warn(`[pi-exe-dev] catalog fetch returned invalid JSON: ${(err as Error).message}`);
    return;
  }
  if (!validCatalog(parsed)) {
    console.warn(`[pi-exe-dev] catalog fetch returned unrecognized shape; skipping cache update`);
    return;
  }
  // Atomic write: tempfile + rename keeps concurrent pi launches from
  // observing a half-written cache file. The .tmp suffix is per-pid so two
  // refreshes can't clobber each other's tempfiles either.
  const tmp = `${CACHE_FILE}.${process.pid}.tmp`;
  try {
    mkdirSync(CACHE_DIR, { recursive: true });
    writeFileSync(tmp, text);
    renameSync(tmp, CACHE_FILE);
  } catch (err) {
    console.warn(`[pi-exe-dev] catalog cache write failed: ${(err as Error).message}`);
    try {
      unlinkSync(tmp);
    } catch {
      // tempfile may not exist if mkdir/write failed first
    }
  }
}

// sanitizeCompat copies only fields whose values pi-ai understands. Unknown
// values would misconfigure the model silently; dropping them lets pi-ai fall
// back to its URL-based auto-detection. Returns undefined if nothing is set.
function sanitizeCompat(c: CatalogCompat | undefined, providerId: string, modelId: string): CompatBag | undefined {
  if (!c) return undefined;
  const out: CompatBag = {};
  if (typeof c.supportsDeveloperRole === "boolean") out.supportsDeveloperRole = c.supportsDeveloperRole;
  if (typeof c.supportsReasoningEffort === "boolean") out.supportsReasoningEffort = c.supportsReasoningEffort;
  if (c.maxTokensField) {
    if (KNOWN_MAX_TOKENS_FIELDS.has(c.maxTokensField)) {
      out.maxTokensField = c.maxTokensField as CompatBag["maxTokensField"];
    } else {
      console.warn(`[pi-exe-dev] dropping unknown maxTokensField "${c.maxTokensField}" on ${providerId}/${modelId}`);
    }
  }
  if (c.thinkingFormat) {
    if (KNOWN_THINKING_FORMATS.has(c.thinkingFormat)) {
      out.thinkingFormat = c.thinkingFormat as CompatBag["thinkingFormat"];
    } else {
      console.warn(`[pi-exe-dev] dropping unknown thinkingFormat "${c.thinkingFormat}" on ${providerId}/${modelId}`);
    }
  }
  if (c.cacheControlFormat) {
    if (KNOWN_CACHE_CONTROL_FORMATS.has(c.cacheControlFormat)) {
      out.cacheControlFormat = c.cacheControlFormat as CompatBag["cacheControlFormat"];
    } else {
      console.warn(`[pi-exe-dev] dropping unknown cacheControlFormat "${c.cacheControlFormat}" on ${providerId}/${modelId}`);
    }
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

// chatModelsWithFullMetadata picks chat models whose metadata is complete
// enough for pi to register directly. Models with partial metadata are
// dropped (and logged) rather than silently registered with bogus values.
// At the provider level, returning an empty list lets pi keep its built-in
// catalog for that provider — see registerOne.
function chatModelsWithFullMetadata(p: CatalogProvider): ProviderModelConfig[] {
  const out: ProviderModelConfig[] = [];
  const skipped: string[] = [];
  for (const m of p.models) {
    if (m.type && m.type !== "chat") continue; // skip embedding/reranker
    // Use explicit nullish/empty checks so a (hypothetical) zero-valued
    // contextWindow or maxTokens isn't misread as "missing", and so an
    // explicit empty input array is treated as missing rather than valid.
    if (m.name == null || m.contextWindow == null || m.maxTokens == null || !m.input?.length) {
      // Partial metadata is a Go-side bug (entry in allowedModels, no
      // gatewayMeta). Surface it instead of hiding the model silently.
      if (m.name || m.contextWindow != null || m.maxTokens != null || m.input?.length) {
        skipped.push(m.id);
      }
      continue;
    }
    const compat = sanitizeCompat(m.compat, p.id, m.id);
    out.push({
      id: m.id,
      name: m.name,
      reasoning: m.reasoning ?? false,
      input: m.input,
      contextWindow: m.contextWindow,
      maxTokens: m.maxTokens,
      cost: m.cost,
      ...(compat ? { compat: compat as ProviderModelConfig["compat"] } : {}),
    });
  }
  if (skipped.length > 0) {
    console.warn(`[pi-exe-dev] dropping ${p.id} models with incomplete metadata: ${skipped.join(", ")}`);
  }
  return out;
}

// registerOne registers a single provider from the catalog. p.id must be one
// of pi-ai's KnownProvider names (anthropic, openai, fireworks, ...) because
// pi merges its built-in catalog by provider name; the Go-side providerCatalog
// in llmpricing/pricing.go is the source of truth for these IDs.
function registerOne(pi: ExtensionAPI, p: CatalogProvider): void {
  const path = p.path.replace(/^\/+/, "");
  const config: ProviderConfig = {
    baseUrl: `${GATEWAY}/${path}`,
    apiKey: "gateway",
  };
  if (p.api) {
    if (KNOWN_APIS.has(p.api)) {
      config.api = p.api as ProviderConfig["api"];
    } else {
      console.warn(`[pi-exe-dev] dropping unknown api "${p.api}" for provider ${p.id}; pi-ai will auto-detect`);
    }
  }
  const models = chatModelsWithFullMetadata(p);
  // Leaving config.models undefined tells pi to keep its built-in catalog for
  // this provider. anthropic and openai rely on this — the gateway does not
  // ship rich metadata for them, only cost.
  if (models.length > 0) config.models = models;
  pi.registerProvider(p.id, config);
}

export default function (pi: ExtensionAPI) {
  // Only activate on exe.dev VMs.
  if (!existsSync("/exe.dev")) return;

  const loaded = loadCatalogSync();
  if (loaded) {
    for (const p of loaded.catalog.providers) {
      try {
        registerOne(pi, p);
      } catch (err) {
        console.warn(`[pi-exe-dev] failed to register provider ${p.id}: ${(err as Error).message}`);
      }
    }
  } else {
    // No cache, no bundled fallback (or both invalid). Point each known
    // gateway provider at its gateway URL with no models defined; pi-ai
    // keeps its built-in catalog for the provider. Some built-in models may
    // not be gateway-allowed and will error at request time, but the model
    // picker still works for the common cases.
    console.warn(`[pi-exe-dev] no usable catalog; falling back to pi's built-in models for anthropic/openai/fireworks`);
    pi.registerProvider("anthropic", { baseUrl: `${GATEWAY}/anthropic`, apiKey: "gateway" });
    pi.registerProvider("openai", { baseUrl: `${GATEWAY}/openai/v1`, apiKey: "gateway" });
    pi.registerProvider("fireworks", {
      baseUrl: `${GATEWAY}/fireworks/inference/v1`,
      apiKey: "gateway",
      api: "openai-completions",
    });
  }

  // Refresh the on-disk cache so the next pi launch starts from fresh data.
  void refreshCatalogAsync();

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
