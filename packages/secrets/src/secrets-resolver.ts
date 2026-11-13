import {
  readFullSecretsFileSoft,
  type FullSecretsFile,
} from "./secrets-store";

export interface SecretsResolver {
  /** Full secrets view: deployment env bags + per-operator credentials. */
  resolveFullSecrets(warn?: (line: string) => void): Promise<FullSecretsFile>;
}

export class FileSecretsResolver implements SecretsResolver {
  constructor(private readonly overrideDir?: string) {}

  resolveFullSecrets(warn?: (line: string) => void): Promise<FullSecretsFile> {
    return readFullSecretsFileSoft(this.overrideDir, warn);
  }
}
