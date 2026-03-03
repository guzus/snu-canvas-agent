import { hasCodexAccount, loadConfig, saveCodexAccount, saveConfig } from "./config-store";
import { exchangeCodexCode, startCodexOAuthFlow } from "./oauth";

type JsonRecord = Record<string, unknown>;

function corsHeaders(origin?: string | null): HeadersInit {
  return {
    "Access-Control-Allow-Origin": origin || "*",
    "Access-Control-Allow-Headers": "Content-Type",
    "Access-Control-Allow-Methods": "GET,POST,PUT,OPTIONS",
  };
}

function json(body: JsonRecord, init: ResponseInit = {}, origin?: string | null): Response {
  return new Response(JSON.stringify(body), {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...corsHeaders(origin),
      ...(init.headers || {}),
    },
  });
}

async function parseJson(req: Request): Promise<JsonRecord> {
  try {
    return (await req.json()) as JsonRecord;
  } catch {
    return {};
  }
}

const port = Number(process.env.ADMIN_BACKEND_PORT || 8787);

Bun.serve({
  port,
  async fetch(req) {
    const origin = req.headers.get("origin");
    const url = new URL(req.url);

    if (req.method === "OPTIONS") {
      return new Response(null, { status: 204, headers: corsHeaders(origin) });
    }

    if (url.pathname === "/health") {
      return json({ ok: true }, {}, origin);
    }

    if (url.pathname === "/api/config" && req.method === "GET") {
      const config = await loadConfig();
      const linked = await hasCodexAccount();
      return json({ config, linked }, {}, origin);
    }

    if (url.pathname === "/api/config" && req.method === "PUT") {
      const body = await parseJson(req);
      const current = await loadConfig();
      const nextModel = String(body.defaultModel || current.defaultModel);
      if (!["openai-codex/gpt-5.3-codex-spark", "openai-codex/gpt-5.3-codex"].includes(nextModel)) {
        return json({ ok: false, error: "unsupported model" }, { status: 400 }, origin);
      }

      const next = {
        ...current,
        defaultModel: nextModel as "openai-codex/gpt-5.3-codex-spark" | "openai-codex/gpt-5.3-codex",
      };
      await saveConfig(next);
      return json({ ok: true, config: next }, {}, origin);
    }

    if (url.pathname === "/api/providers/openai-codex/oauth/start" && req.method === "POST") {
      try {
        const result = await startCodexOAuthFlow();
        return json({ ok: true, ...result }, {}, origin);
      } catch (err) {
        return json({ ok: false, error: err instanceof Error ? err.message : String(err) }, { status: 500 }, origin);
      }
    }

    if (url.pathname === "/api/providers/openai-codex/oauth/callback" && req.method === "POST") {
      try {
        const body = await parseJson(req);
        const token = await exchangeCodexCode({
          redirectUrl: typeof body.redirectUrl === "string" ? body.redirectUrl : undefined,
          code: typeof body.code === "string" ? body.code : undefined,
          state: typeof body.state === "string" ? body.state : undefined,
        });

        await saveCodexAccount(token);
        const current = await loadConfig();
        await saveConfig({
          ...current,
          codex: {
            ...current.codex,
            enabled: true,
            linkedAt: new Date().toISOString(),
          },
        });

        return json({ ok: true }, {}, origin);
      } catch (err) {
        return json({ ok: false, error: err instanceof Error ? err.message : String(err) }, { status: 400 }, origin);
      }
    }

    return json({ ok: false, error: "not found" }, { status: 404 }, origin);
  },
});

console.log(`admin-backend listening on http://localhost:${port}`);
