export type PasskeySummary = {
  id: string;
  credentialId: string;
  name: string;
  createdAt: string;
  lastUsedAt: string | null;
};

export type PasskeyAssertionPayload = {
  credentialId: string;
  clientDataJSON: string;
  authenticatorData: string;
  signature: string;
};

type ChallengeResponse = {
  purpose: "register" | "approve";
  challenge: string;
  rp: { id: string; name: string };
  user: { id: string; name: string; displayName: string };
  pubKeyCredParams: Array<{ type: "public-key"; alg: number }>;
  excludeCredentials: Array<{ id: string; type: "public-key" }>;
  allowCredentials: Array<{ id: string; type: "public-key" }>;
  userVerification: UserVerificationRequirement;
};

export function toBase64Url(buffer: ArrayBuffer | Uint8Array): string {
  const bytes = buffer instanceof Uint8Array ? buffer : new Uint8Array(buffer);
  let binary = "";
  for (let i = 0; i < bytes.byteLength; i++) {
    binary += String.fromCharCode(bytes[i]!);
  }
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
}

export function fromBase64Url(value: string): Uint8Array<ArrayBuffer> {
  const normalized = value.replace(/-/g, "+").replace(/_/g, "/");
  const pad = normalized.length % 4 === 0 ? "" : "=".repeat(4 - (normalized.length % 4));
  const binary = atob(normalized + pad);
  const buffer = new ArrayBuffer(binary.length);
  const bytes = new Uint8Array(buffer);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

export function isPasskeySupported(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.PublicKeyCredential !== "undefined" &&
    typeof navigator !== "undefined" &&
    typeof navigator.credentials?.create === "function" &&
    typeof navigator.credentials?.get === "function"
  );
}

async function requestChallenge(
  purpose: "register" | "approve",
  actionRequestId?: string,
): Promise<ChallengeResponse> {
  const res = await fetch("/api/v1/my/passkeys/challenge", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(
      purpose === "approve" ? { purpose, actionRequestId } : { purpose },
    ),
  });
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as { error?: string } | null;
    throw new Error(body?.error ?? `Could not start passkey check (${res.status}).`);
  }
  return (await res.json()) as ChallengeResponse;
}

export async function listPasskeys(): Promise<PasskeySummary[]> {
  const res = await fetch("/api/v1/my/passkeys", { cache: "no-store" });
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as { error?: string } | null;
    throw new Error(body?.error ?? `Could not load passkeys (${res.status}).`);
  }
  const body = (await res.json()) as { passkeys?: PasskeySummary[] };
  return body.passkeys ?? [];
}

export async function registerPasskey(name = "This device"): Promise<PasskeySummary> {
  if (!isPasskeySupported()) {
    throw new Error("Passkeys are not supported in this browser.");
  }
  const challenge = await requestChallenge("register");
  const cred = (await navigator.credentials.create({
    publicKey: {
      challenge: fromBase64Url(challenge.challenge),
      rp: challenge.rp,
      user: {
        id: fromBase64Url(challenge.user.id),
        name: challenge.user.name,
        displayName: challenge.user.displayName,
      },
      pubKeyCredParams: challenge.pubKeyCredParams,
      excludeCredentials: challenge.excludeCredentials.map((d) => ({
        id: fromBase64Url(d.id),
        type: "public-key" as const,
      })),
      authenticatorSelection: {
        userVerification: challenge.userVerification,
        residentKey: "preferred",
      },
    },
  })) as PublicKeyCredential | null;
  if (!cred) {
    throw new Error("Passkey registration was cancelled.");
  }
  const response = cred.response as AuthenticatorAttestationResponse;
  const res = await fetch("/api/v1/my/passkeys", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      credentialId: toBase64Url(cred.rawId),
      clientDataJSON: toBase64Url(response.clientDataJSON),
      attestationObject: toBase64Url(response.attestationObject),
      name: name.trim() || "This device",
    }),
  });
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as { error?: string } | null;
    throw new Error(body?.error ?? `Could not save passkey (${res.status}).`);
  }
  const body = (await res.json()) as { passkey: PasskeySummary };
  return body.passkey;
}

export async function deletePasskey(passkeyId: string): Promise<void> {
  const res = await fetch(`/api/v1/my/passkeys/${encodeURIComponent(passkeyId)}`, {
    method: "DELETE",
  });
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as { error?: string } | null;
    throw new Error(body?.error ?? `Could not remove passkey (${res.status}).`);
  }
}

export async function createApprovalPasskeyAssertion(
  actionRequestId: string,
): Promise<PasskeyAssertionPayload> {
  if (!isPasskeySupported()) {
    throw new Error("Passkeys are not supported in this browser.");
  }
  let challenge = await requestChallenge("approve", actionRequestId);
  if (challenge.allowCredentials.length === 0) {
    await registerPasskey("This device");
    challenge = await requestChallenge("approve", actionRequestId);
  }
  const cred = (await navigator.credentials.get({
    publicKey: {
      challenge: fromBase64Url(challenge.challenge),
      rpId: challenge.rp.id,
      allowCredentials: challenge.allowCredentials.map((d) => ({
        id: fromBase64Url(d.id),
        type: "public-key" as const,
      })),
      userVerification: challenge.userVerification,
    },
  })) as PublicKeyCredential | null;
  if (!cred) {
    throw new Error("Passkey approval was cancelled.");
  }
  const response = cred.response as AuthenticatorAssertionResponse;
  return {
    credentialId: toBase64Url(cred.rawId),
    clientDataJSON: toBase64Url(response.clientDataJSON),
    authenticatorData: toBase64Url(response.authenticatorData),
    signature: toBase64Url(response.signature),
  };
}
