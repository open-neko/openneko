import { createHash, randomBytes, randomUUID } from "node:crypto";
import type { PoolClient } from "pg";
import { canonicalHash, packCredentialSchema, type PackConnector } from "@neko/packs";
import { maybeDecryptSecret, maybeEncryptSecret } from "@neko/secret-crypt";
import { runPackConnector } from "./connector-runner.js";

const hash = (value: string) => createHash("sha256").update(value).digest("base64url");
const encode = (value: unknown) => maybeEncryptSecret(JSON.stringify(value));
const decode = (value: string) => JSON.parse(maybeDecryptSecret(value));
const text = (value: unknown, limit = 4096): string => {
  if (typeof value !== "string" || !value || value.length > limit) throw new Error("Invalid connection input");
  return value;
};
export type PackConnectionContext = { sql: PoolClient; installationId: string; connector: PackConnector; owner: string };

// Caller holds the existing pack lifecycle lock for the full operation. This also
// serializes refresh rotation across workers; do not add an in-memory token cache.
export async function packConnection(ctx: PackConnectionContext, action: string, input: Record<string, unknown>) {
  const { sql, installationId, connector, owner } = ctx;
  if (!owner || !connector.auth) throw new Error("Pack account owner and authentication declaration are required");
  const key = [installationId, connector.id];
  const authHash = canonicalHash(connector.auth);
  const { rows: clients } = await sql.query("select * from pack_connection_client where pack_install_id=$1 and connector_id=$2", key);
  const client = clients[0];
  if (action === "configure") {
    const settings = { clientId: text(input.clientId), clientSecret: text(input.clientSecret) };
    // Only the admin route can request this action. Never return the client secret.
    if (client?.auth_hash === authHash && canonicalHash(decode(client.value_enc)) === canonicalHash(settings)) return { configured: true };
    await sql.query("BEGIN");
    try {
      await sql.query("delete from pack_connection_client where pack_install_id=$1 and connector_id=$2", key);
      await sql.query("insert into pack_connection_client(pack_install_id,connector_id,auth_hash,value_enc) values($1,$2,$3,$4)", [...key, authHash, encode(settings)]);
      await sql.query("COMMIT");
    } catch (error) { await sql.query("ROLLBACK"); throw error; }
    return { configured: true };
  }
  if (action === "list") {
    const { rows } = await sql.query("select id,label,status from pack_account where pack_install_id=$1 and connector_id=$2 and owner_id=$3 and status <> 'pending' order by updated_at", [...key, owner]);
    return { configured: client?.auth_hash === authHash, accounts: rows.map(row => ({ ...row, status: client?.auth_hash === authHash ? row.status : "reconnect_required" })) };
  }
  if (!client || client.auth_hash !== authHash) throw new Error("An administrator must configure this pack connection");
  const settings = decode(client.value_enc);
  const invoke = (connection: "authorize" | "exchange" | "refresh" | "revoke", data: Record<string, unknown>) => runPackConnector(connector, { connection, input: { ...data, client: settings } });
  const account = async (id: unknown) => {
    const { rows } = await sql.query("select * from pack_account where id=$1 and pack_install_id=$2 and connector_id=$3 and owner_id=$4 and status <> 'pending'", [text(id, 36), ...key, owner]);
    if (!rows[0]) throw new Error("Pack account is not available to this user");
    return rows[0];
  };
  if (action === "start") {
    const redirectUri = text(input.redirectUri);
    const callback = new URL(redirectUri);
    if (callback.protocol !== "https:" && !(callback.protocol === "http:" && ["localhost", "127.0.0.1"].includes(callback.hostname))) throw new Error("Invalid callback URI");
    const reconnect = input.accountId ? await account(input.accountId) : undefined;
    const id = randomUUID();
    const state = `${id}.${randomBytes(32).toString("base64url")}`;
    const verifier = randomBytes(32).toString("base64url");
    const challenge = hash(verifier);
    const value = await invoke("authorize", { redirectUri, state, codeChallenge: challenge, scopes: connector.auth.scopes });
    const authorizationUrl = text((value as { authorizationUrl?: unknown })?.authorizationUrl, 16384);
    const url = new URL(authorizationUrl);
    if (url.origin !== connector.auth.authorizationOrigin || url.username || url.password || url.hash ||
      url.searchParams.get("state") !== state || url.searchParams.get("redirect_uri") !== redirectUri ||
      url.searchParams.get("client_id") !== settings.clientId ||
      canonicalHash((url.searchParams.get("scope") ?? "").split(/\s+/).filter(Boolean).sort()) !== canonicalHash([...new Set(connector.auth.scopes)].sort()) ||
      url.searchParams.get("response_type") !== "code" || url.searchParams.get("code_challenge") !== challenge ||
      url.searchParams.get("code_challenge_method") !== "S256") throw new Error("Connector returned an invalid authorization URL");
    await sql.query("delete from pack_account where pack_install_id=$1 and connector_id=$2 and owner_id=$3 and status='pending'", [...key, owner]);
    await sql.query("insert into pack_account(id,pack_install_id,connector_id,owner_id,status,value_enc) values($1,$2,$3,$4,'pending',$5)",
      [id, ...key, owner, encode({ stateHash: hash(state), verifier, redirectUri, expiresAt: Date.now() + 600_000, reconnectId: reconnect?.id, authHash, clientHash: hash(client.value_enc) })]);
    return { authorizationUrl, state };
  }
  if (action === "callback") {
    const state = text(input.state);
    const id = state.split(".")[0];
    if (!/^[a-f0-9-]{36}$/.test(id!)) throw new Error("Invalid connection state");
    const { rows } = await sql.query("select * from pack_account where id=$1 and pack_install_id=$2 and connector_id=$3 and owner_id=$4 and status='pending'", [id, ...key, owner]);
    if (!rows[0]) throw new Error("Connection state is expired or already used");
    const pending = decode(rows[0].value_enc);
    if (pending.stateHash !== hash(state) || pending.redirectUri !== input.redirectUri || pending.expiresAt <= Date.now() || pending.authHash !== authHash || pending.clientHash !== hash(client.value_enc)) throw new Error("Invalid or expired connection state");
    // Consume before the remote exchange. Failure requires a fresh browser flow.
    await sql.query("delete from pack_account where id=$1", [id]);
    if (input.error) throw new Error("Account connection was not authorized");
    const credential = packCredentialSchema.parse(await invoke("exchange", { code: text(input.code), redirectUri: pending.redirectUri, codeVerifier: pending.verifier, scopes: connector.auth.scopes }));
    if (!connector.auth.scopes.every(scope => credential.scopes.includes(scope))) {
      await invoke("revoke", { credential }).catch(() => {});
      throw new Error("Required permissions were not granted. Connect again and grant all required permissions");
    }
    if (pending.reconnectId) {
      const previous = await account(pending.reconnectId);
      if (decode(previous.value_enc).accountId !== credential.accountId) {
        await invoke("revoke", { credential }).catch(() => {});
        throw new Error("Reconnect must use the same provider account");
      }
      await sql.query("update pack_account set value_enc=$2,label=$3,status='connected',updated_at=now() where id=$1", [previous.id, encode(credential), credential.label]);
      return { accountId: previous.id };
    }
    await sql.query("insert into pack_account(id,pack_install_id,connector_id,owner_id,status,value_enc,label) values($1,$2,$3,$4,'connected',$5,$6)", [id, ...key, owner, encode(credential), credential.label]);
    return { accountId: id };
  }
  const row = await account(input.accountId);
  if (action === "disconnect") {
    // Keep the credential for a retry if remote revocation fails.
    const result = await invoke("revoke", { credential: decode(row.value_enc) });
    if ((result as { revoked?: unknown })?.revoked !== true) throw new Error("Account revocation was not confirmed. Try again");
    await sql.query("delete from pack_account where id=$1", [row.id]);
    return { disconnected: true };
  }
  if (action === "credential") {
    if (row.status !== "connected") throw new Error("Reconnect this pack account");
    let credential = packCredentialSchema.parse(decode(row.value_enc));
    if (credential.expiresAt <= Date.now() + 60_000) {
      // Persist before the remote call. A crash or ambiguous rotation must not
      // retry an old refresh token. A successful refresh restores connected.
      await sql.query("update pack_account set status='reconnect_required' where id=$1", [row.id]);
      const refreshed = packCredentialSchema.parse(await invoke("refresh", { credential }));
      if (refreshed.accountId !== credential.accountId || !connector.auth.scopes.every(scope => refreshed.scopes.includes(scope)) || refreshed.expiresAt <= Date.now() + 60_000) throw new Error("Reconnect this pack account");
      credential = refreshed;
      await sql.query("update pack_account set value_enc=$2,status='connected',updated_at=now() where id=$1", [row.id, encode(credential)]);
    }
    return credential;
  }
  throw new Error("Unknown pack connection action");
}
