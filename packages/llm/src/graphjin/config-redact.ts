export const REDACTED = "[REDACTED]";

const SENSITIVE_PARTS = [
  "password",
  "secret",
  "token",
  "passphrase",
  "private_key",
  "client_key",
  "api_key",
  "apikey",
  "authorization",
  "cookie",
];

const SENSITIVE_KEYS = new Set(["connection_string", "key_value", "dsn"]);

export function isSensitiveConfigKey(key: string): boolean {
  const k = key.toLowerCase().replaceAll("-", "_");
  return SENSITIVE_KEYS.has(k) || SENSITIVE_PARTS.some((part) => k.includes(part));
}

function withoutUserinfo(value: string): string {
  return value.replace(/\b([a-z][a-z0-9+.-]*:\/\/)[^/\s@]*@/gi, `$1${REDACTED}@`);
}

/** GraphJin's gj_config redaction rules, applied again on OpenNeko's side. */
export function redactSourceConfig(value: unknown): unknown {
  if (typeof value === "string") return withoutUserinfo(value);
  if (Array.isArray(value)) return value.map(redactSourceConfig);
  if (value && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value as Record<string, unknown>).map(([key, item]) => [
        key,
        isSensitiveConfigKey(key) ? REDACTED : redactSourceConfig(item),
      ]),
    );
  }
  return value;
}
