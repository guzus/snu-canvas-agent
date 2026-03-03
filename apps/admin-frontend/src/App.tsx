import { useEffect, useMemo, useState } from "react";

type Config = {
  provider: "openai-codex";
  defaultModel: "openai-codex/gpt-5.3-codex-spark" | "openai-codex/gpt-5.3-codex";
  codex: {
    enabled: boolean;
    linkedAt?: string;
  };
};

type ChatRow = {
  chat_id: string;
  lang: string;
  bindings: number;
  subscriptions: number;
  sent_alerts: number;
  last_sent_at: string | null;
};

const apiBase = import.meta.env.VITE_ADMIN_BACKEND_URL || window.location.origin;

export function App() {
  const [config, setConfig] = useState<Config | null>(null);
  const [linked, setLinked] = useState(false);
  const [chats, setChats] = useState<ChatRow[]>([]);
  const [oauthUrl, setOauthUrl] = useState("");
  const [redirectUrl, setRedirectUrl] = useState("");
  const [message, setMessage] = useState("");
  const [loading, setLoading] = useState(false);

  const modelOptions = useMemo(
    () => [
      { value: "openai-codex/gpt-5.3-codex-spark", label: "GPT-5.3 Codex Spark (default)" },
      { value: "openai-codex/gpt-5.3-codex", label: "GPT-5.3 Codex" },
    ],
    []
  );

  async function load() {
    const [cfgRes, chatsRes] = await Promise.all([
      fetch(`${apiBase}/api/config`),
      fetch(`${apiBase}/api/chat-ids`),
    ]);
    const cfg = (await cfgRes.json()) as { config: Config; linked: boolean };
    setConfig(cfg.config);
    setLinked(Boolean(cfg.linked));
    if (chatsRes.ok) {
      const chatsPayload = (await chatsRes.json()) as { chats?: ChatRow[] };
      setChats(Array.isArray(chatsPayload.chats) ? chatsPayload.chats : []);
    } else {
      setChats([]);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  async function startOauth() {
    setLoading(true);
    setMessage("");
    try {
      const res = await fetch(`${apiBase}/api/providers/openai-codex/oauth/start`, { method: "POST" });
      const data = (await res.json()) as { ok: boolean; url?: string; error?: string };
      if (!data.ok || !data.url) throw new Error(data.error || "Failed to start OAuth");
      setOauthUrl(data.url);
      window.open(data.url, "_blank", "noopener,noreferrer");
      setMessage("Opened ChatGPT login in a new tab. Paste the callback URL below.");
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }

  async function completeOauth() {
    if (!redirectUrl.trim()) {
      setMessage("Paste callback URL first.");
      return;
    }

    setLoading(true);
    setMessage("");
    try {
      const res = await fetch(`${apiBase}/api/providers/openai-codex/oauth/callback`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ redirectUrl }),
      });
      const data = (await res.json()) as { ok: boolean; error?: string };
      if (!data.ok) throw new Error(data.error || "OAuth callback failed");
      setRedirectUrl("");
      setMessage("ChatGPT account connected.");
      await load();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }

  async function saveModel(defaultModel: Config["defaultModel"]) {
    if (!config) return;
    setLoading(true);
    setMessage("");
    try {
      const res = await fetch(`${apiBase}/api/config`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ defaultModel }),
      });
      const data = (await res.json()) as { ok: boolean; error?: string; config?: Config };
      if (!data.ok || !data.config) throw new Error(data.error || "Save failed");
      setConfig(data.config);
      setMessage("Default model saved.");
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }

  if (!config) {
    return <div style={container}>Loading...</div>;
  }

  return (
    <div style={container}>
      <h1 style={{ marginTop: 0 }}>lx-agent Admin Dashboard</h1>
      <p style={{ color: "#666" }}>TypeScript + Bun dashboard with ChatGPT login (Office-style flow).</p>

      <section style={card}>
        <h2 style={h2}>ChatGPT Login</h2>
        <p>Status: {linked ? "Connected" : "Not connected"}</p>
        <button onClick={startOauth} disabled={loading} style={btn}>
          Login with ChatGPT
        </button>

        {oauthUrl ? (
          <p style={{ fontSize: 12, wordBreak: "break-all", color: "#666" }}>
            OAuth URL: {oauthUrl}
          </p>
        ) : null}

        <textarea
          placeholder="Paste full callback URL here"
          value={redirectUrl}
          onChange={(e) => setRedirectUrl(e.target.value)}
          style={textarea}
        />
        <button onClick={completeOauth} disabled={loading} style={btnAlt}>
          Complete Login
        </button>
      </section>

      <section style={card}>
        <h2 style={h2}>Model</h2>
        <label>
          Default model:&nbsp;
          <select
            value={config.defaultModel}
            onChange={(e) => void saveModel(e.target.value as Config["defaultModel"])}
            disabled={loading}
          >
            {modelOptions.map((m) => (
              <option key={m.value} value={m.value}>
                {m.label}
              </option>
            ))}
          </select>
        </label>
      </section>

      <section style={card}>
        <h2 style={h2}>Telegram Chats</h2>
        {chats.length === 0 ? (
          <p style={{ margin: 0, color: "#666" }}>No chat IDs found.</p>
        ) : (
          <div style={{ overflowX: "auto" }}>
            <table style={table}>
              <thead>
                <tr>
                  <th style={th}>Chat ID</th>
                  <th style={th}>Lang</th>
                  <th style={th}>Bindings</th>
                  <th style={th}>Subscriptions</th>
                  <th style={th}>Sent Alerts</th>
                  <th style={th}>Last Alert</th>
                </tr>
              </thead>
              <tbody>
                {chats.map((c) => (
                  <tr key={c.chat_id}>
                    <td style={td}>{c.chat_id}</td>
                    <td style={td}>{c.lang}</td>
                    <td style={td}>{c.bindings}</td>
                    <td style={td}>{c.subscriptions}</td>
                    <td style={td}>{c.sent_alerts}</td>
                    <td style={td}>{c.last_sent_at ? new Date(c.last_sent_at).toLocaleString() : "-"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {message ? <p style={{ color: "#0b6", whiteSpace: "pre-wrap" }}>{message}</p> : null}
    </div>
  );
}

const container: React.CSSProperties = {
  maxWidth: 900,
  margin: "2rem auto",
  padding: "0 1rem",
  fontFamily: "ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Helvetica, Arial",
};

const card: React.CSSProperties = {
  border: "1px solid #ddd",
  borderRadius: 10,
  padding: "1rem",
  marginBottom: "1rem",
  background: "#fff",
};

const h2: React.CSSProperties = {
  marginTop: 0,
};

const btn: React.CSSProperties = {
  border: "none",
  borderRadius: 8,
  padding: "0.6rem 1rem",
  background: "#111",
  color: "#fff",
  cursor: "pointer",
};

const btnAlt: React.CSSProperties = {
  ...btn,
  background: "#333",
  marginTop: "0.5rem",
};

const textarea: React.CSSProperties = {
  display: "block",
  width: "100%",
  minHeight: 90,
  marginTop: "0.75rem",
  borderRadius: 8,
  border: "1px solid #ccc",
  padding: "0.5rem",
};

const table: React.CSSProperties = {
  width: "100%",
  borderCollapse: "collapse",
  fontSize: 14,
};

const th: React.CSSProperties = {
  textAlign: "left",
  borderBottom: "1px solid #ddd",
  padding: "0.5rem",
  whiteSpace: "nowrap",
};

const td: React.CSSProperties = {
  borderBottom: "1px solid #eee",
  padding: "0.5rem",
  whiteSpace: "nowrap",
};
