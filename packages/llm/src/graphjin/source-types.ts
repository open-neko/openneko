export const DATABASE_SOURCE_TYPES = ["postgres", "mysql", "mariadb"] as const;

export const SOURCE_TABLE_NAME = /^[A-Za-z_][A-Za-z0-9_$]*(\.[A-Za-z_][A-Za-z0-9_$]*)?$/;

export const PASSWORD_REQUIRED_MESSAGE =
  "OpenNeko needs a login with a password. Save the password for this login first.";

export class PasswordRequiredError extends Error {
  readonly field = "secretRef";

  constructor() {
    super(PASSWORD_REQUIRED_MESSAGE);
    this.name = "PasswordRequiredError";
  }
}
