export interface BeginConnectParams {
  operatorId: string;
  redirectUri: string;
  state: string;
  scopes?: string[];
  codeVerifier?: string | null;
}

export interface CompleteConnectParams {
  operatorId: string;
  code: string;
  redirectUri: string;
  state: string;
  scopes?: string[];
  codeVerifier?: string | null;
  /** Opaque in-flight state returned by begin, echoed back by the web. */
  oauthState?: string;
}

/** Sign-in values a provider reads from its secrets-store section. */
export interface SignInSetting {
  key: string;
  required: boolean;
  secret: boolean;
  /** The host generates the value (an HMAC key nobody types). */
  autogenerate?: boolean;
}
