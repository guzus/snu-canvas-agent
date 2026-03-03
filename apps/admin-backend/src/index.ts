import { hasCodexAccount, loadConfig, saveCodexAccount, saveConfig } from "./config-store";
import { exchangeCodexCode, startCodexOAuthFlow } from "./oauth";
import { stat } from "node:fs/promises";
import { extname, resolve } from "node:path";
import { Pool } from "pg";

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

const port = Number(process.env.PORT || process.env.ADMIN_BACKEND_PORT || 8787);
const frontendDist = resolve(process.cwd(), "apps/admin-frontend/dist");
const databaseURL = String(process.env.DATABASE_URL || "").trim();
const pool = databaseURL ? new Pool({ connectionString: databaseURL }) : null;
let schemaReady = false;
let schemaInitPromise: Promise<void> | null = null;

type ChatRow = {
  chat_id: string;
  lang: string;
  bindings: number;
  subscriptions: number;
  sent_alerts: number;
  last_sent_at: string | null;
};

async function ensureChatSchema(): Promise<void> {
  if (!pool || schemaReady) return;
  if (schemaInitPromise) return schemaInitPromise;

  schemaInitPromise = (async () => {
    const client = await pool.connect();
    try {
      await client.query("BEGIN");
      await client.query("SELECT pg_advisory_xact_lock(781234567890123)");
      await client.query(`
CREATE TABLE IF NOT EXISTS telegram_bindings (
  canvas_api_key TEXT PRIMARY KEY,
  chat_id TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`);
      await client.query(`
CREATE TABLE IF NOT EXISTS telegram_chat_settings (
  chat_id TEXT PRIMARY KEY,
  lang TEXT NOT NULL DEFAULT 'ko',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`);
      await client.query(`
CREATE TABLE IF NOT EXISTS telegram_chat_courses (
  chat_id TEXT NOT NULL,
  course_id INTEGER NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (chat_id, course_id)
)`);
      await client.query(`
CREATE TABLE IF NOT EXISTS telegram_sent_alerts (
  chat_id TEXT NOT NULL,
  dedupe_key TEXT NOT NULL,
  alert_type TEXT NOT NULL,
  course_id INTEGER NULL,
  entity_id BIGINT NULL,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  sent_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (chat_id, dedupe_key)
)`);
      await client.query("COMMIT");
      schemaReady = true;
    } catch (err) {
      await client.query("ROLLBACK");
      throw err;
    } finally {
      client.release();
      schemaInitPromise = null;
    }
  })();

  return schemaInitPromise;
}

async function listChats(): Promise<ChatRow[]> {
  if (!pool) return [];
  await ensureChatSchema();

  const result = await pool.query<ChatRow>(`
SELECT
  c.chat_id,
  COALESCE(s.lang, 'ko') AS lang,
  COALESCE(b.bindings, 0) AS bindings,
  COALESCE(cc.subscriptions, 0) AS subscriptions,
  COALESCE(sa.sent_alerts, 0) AS sent_alerts,
  sa.last_sent_at
FROM (
  SELECT chat_id FROM telegram_bindings
  UNION
  SELECT chat_id FROM telegram_chat_settings
  UNION
  SELECT chat_id FROM telegram_chat_courses
  UNION
  SELECT chat_id FROM telegram_sent_alerts
) c
LEFT JOIN telegram_chat_settings s ON s.chat_id = c.chat_id
LEFT JOIN (
  SELECT chat_id, COUNT(*)::int AS bindings
  FROM telegram_bindings
  GROUP BY chat_id
) b ON b.chat_id = c.chat_id
LEFT JOIN (
  SELECT chat_id, COUNT(*)::int AS subscriptions
  FROM telegram_chat_courses
  GROUP BY chat_id
) cc ON cc.chat_id = c.chat_id
LEFT JOIN (
  SELECT chat_id, COUNT(*)::int AS sent_alerts, MAX(sent_at)::text AS last_sent_at
  FROM telegram_sent_alerts
  GROUP BY chat_id
) sa ON sa.chat_id = c.chat_id
ORDER BY c.chat_id
`);
  return result.rows;
}

function contentTypeFor(pathname: string): string | undefined {
  switch (extname(pathname).toLowerCase()) {
    case ".html":
      return "text/html; charset=utf-8";
    case ".js":
      return "application/javascript; charset=utf-8";
    case ".css":
      return "text/css; charset=utf-8";
    case ".json":
      return "application/json; charset=utf-8";
    case ".svg":
      return "image/svg+xml";
    case ".png":
      return "image/png";
    case ".jpg":
    case ".jpeg":
      return "image/jpeg";
    case ".webp":
      return "image/webp";
    default:
      return undefined;
  }
}

async function serveFrontend(pathname: string): Promise<Response | null> {
  if (pathname.startsWith("/api/")) return null;

  let rel = pathname;
  if (rel === "/" || rel === "") rel = "/index.html";
  const candidate = resolve(frontendDist, "." + rel);
  if (!candidate.startsWith(frontendDist)) {
    return new Response("forbidden", { status: 403 });
  }

  try {
    const s = await stat(candidate);
    if (s.isFile()) {
      const ct = contentTypeFor(candidate);
      return new Response(Bun.file(candidate), ct ? { headers: { "Content-Type": ct } } : undefined);
    }
  } catch {
    // Fall through to SPA index fallback.
  }

  const indexPath = resolve(frontendDist, "index.html");
  try {
    const s = await stat(indexPath);
    if (s.isFile()) {
      return new Response(Bun.file(indexPath), { headers: { "Content-Type": "text/html; charset=utf-8" } });
    }
  } catch {
    return null;
  }
  return null;
}

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

    if (url.pathname === "/api/chat-ids" && req.method === "GET") {
      try {
        const chats = await listChats();
        return json({ ok: true, chats, databaseConnected: Boolean(pool) }, {}, origin);
      } catch (err) {
        return json(
          { ok: false, error: err instanceof Error ? err.message : String(err), databaseConnected: Boolean(pool) },
          { status: 500 },
          origin
        );
      }
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

    if (req.method === "GET" || req.method === "HEAD") {
      const staticResp = await serveFrontend(url.pathname);
      if (staticResp) return staticResp;
    }

    return json({ ok: false, error: "not found" }, { status: 404 }, origin);
  },
});

console.log(`admin-backend listening on http://localhost:${port}`);
