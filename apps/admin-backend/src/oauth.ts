import { randomBytes } from "node:crypto";

const CODEX_CLIENT_ID = "app_EMoamEEZ73f0CkXaXp7hrann";
const CODEX_AUTHORIZE_URL = "https://auth.openai.com/oauth/authorize";
const CODEX_TOKEN_URL = "https://auth.openai.com/oauth/token";
const CODEX_REDIRECT_URI = "http://localhost:1455/auth/callback";
const CODEX_SCOPE = "openid profile email offline_access";

const pendingFlows = new Map<string, { verifier: string; createdAt: number }>();
const flowTTLms = 10 * 60 * 1000;

function base64urlEncode(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=/g, "");
}

export async function generatePKCE(): Promise<{ verifier: string; challenge: string }> {
  const verifierBytes = new Uint8Array(32);
  crypto.getRandomValues(verifierBytes);
  const verifier = base64urlEncode(verifierBytes);

  const data = new TextEncoder().encode(verifier);
  const hash = await crypto.subtle.digest("SHA-256", data);
  const challenge = base64urlEncode(new Uint8Array(hash));
  return { verifier, challenge };
}

function cleanupFlows(): void {
  const now = Date.now();
  for (const [state, entry] of pendingFlows.entries()) {
    if (now - entry.createdAt > flowTTLms) pendingFlows.delete(state);
  }
}

export async function startCodexOAuthFlow(): Promise<{ url: string; state: string }> {
  cleanupFlows();
  const { verifier, challenge } = await generatePKCE();
  const state = randomBytes(16).toString("hex");

  const url = new URL(CODEX_AUTHORIZE_URL);
  url.searchParams.set("response_type", "code");
  url.searchParams.set("client_id", CODEX_CLIENT_ID);
  url.searchParams.set("redirect_uri", CODEX_REDIRECT_URI);
  url.searchParams.set("scope", CODEX_SCOPE);
  url.searchParams.set("code_challenge", challenge);
  url.searchParams.set("code_challenge_method", "S256");
  url.searchParams.set("state", state);
  url.searchParams.set("id_token_add_organizations", "true");
  url.searchParams.set("codex_cli_simplified_flow", "true");
  url.searchParams.set("originator", "pi");

  pendingFlows.set(state, { verifier, createdAt: Date.now() });
  return { url: url.toString(), state };
}

export type ExchangeInput = {
  redirectUrl?: string;
  code?: string;
  state?: string;
};

export type ExchangeOutput = {
  access_token: string;
  refresh_token: string;
  id_token?: string;
  expires_at?: number;
};

export async function exchangeCodexCode(input: ExchangeInput): Promise<ExchangeOutput> {
  let code = input.code;
  let state = input.state;

  if (input.redirectUrl) {
    try {
      const parsed = new URL(input.redirectUrl);
      code = parsed.searchParams.get("code") ?? code;
      state = parsed.searchParams.get("state") ?? state;
    } catch {
      const params = new URLSearchParams(input.redirectUrl);
      code = params.get("code") ?? code;
      state = params.get("state") ?? state;
    }
  }

  if (!code || !state) {
    throw new Error("Missing authorization code or state. Paste full redirect URL.");
  }

  const entry = pendingFlows.get(state);
  if (!entry) {
    throw new Error("Invalid or expired OAuth state. Start login again.");
  }
  pendingFlows.delete(state);

  const tokenRes = await fetch(CODEX_TOKEN_URL, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "authorization_code",
      client_id: CODEX_CLIENT_ID,
      code,
      code_verifier: entry.verifier,
      redirect_uri: CODEX_REDIRECT_URI,
    }),
  });

  if (!tokenRes.ok) {
    const text = await tokenRes.text().catch(() => "");
    throw new Error(`Token exchange failed (${tokenRes.status}): ${text}`);
  }

  const tokenJson = (await tokenRes.json()) as {
    access_token?: string;
    refresh_token?: string;
    id_token?: string;
    expires_in?: number;
  };

  if (!tokenJson.access_token || !tokenJson.refresh_token) {
    throw new Error("Invalid token response: missing access_token or refresh_token");
  }

  return {
    access_token: tokenJson.access_token,
    refresh_token: tokenJson.refresh_token,
    id_token: tokenJson.id_token,
    expires_at: tokenJson.expires_in ? Date.now() + tokenJson.expires_in * 1000 : undefined,
  };
}
