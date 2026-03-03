import { mkdir, readFile, stat, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

export type AdminConfig = {
  provider: "openai-codex";
  defaultModel: "openai-codex/gpt-5.3-codex-spark" | "openai-codex/gpt-5.3-codex";
  codex: {
    enabled: boolean;
    authPath: string;
    linkedAt?: string;
  };
};

export type CodexAccount = {
  access_token: string;
  refresh_token: string;
  id_token?: string;
  expires_at?: number;
};

const configPath = resolve(process.cwd(), "apps/admin-backend/data/config.json");
const codexPath = resolve(process.cwd(), "apps/admin-backend/data/codex_account.json");

const defaultConfig: AdminConfig = {
  provider: "openai-codex",
  defaultModel: "openai-codex/gpt-5.3-codex-spark",
  codex: {
    enabled: false,
    authPath: "apps/admin-backend/data/codex_account.json",
  },
};

export async function ensureDataFiles(): Promise<void> {
  await mkdir(dirname(configPath), { recursive: true });
  try {
    await stat(configPath);
  } catch {
    await writeFile(configPath, JSON.stringify(defaultConfig, null, 2), "utf8");
  }
}

export async function loadConfig(): Promise<AdminConfig> {
  await ensureDataFiles();
  try {
    const raw = await readFile(configPath, "utf8");
    const parsed = JSON.parse(raw) as Partial<AdminConfig>;
    return {
      ...defaultConfig,
      ...parsed,
      codex: {
        ...defaultConfig.codex,
        ...(parsed.codex ?? {}),
      },
    };
  } catch {
    return defaultConfig;
  }
}

export async function saveConfig(next: AdminConfig): Promise<void> {
  await ensureDataFiles();
  await writeFile(configPath, JSON.stringify(next, null, 2), "utf8");
}

export async function saveCodexAccount(account: CodexAccount): Promise<void> {
  await ensureDataFiles();
  await writeFile(codexPath, JSON.stringify(account, null, 2), "utf8");
}

export async function hasCodexAccount(): Promise<boolean> {
  try {
    await stat(codexPath);
    return true;
  } catch {
    return false;
  }
}

export function getCodexAccountPath(): string {
  return codexPath;
}
