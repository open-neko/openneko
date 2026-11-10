export const DATABASE_SOURCE_TYPES = ["postgres", "mysql", "mariadb"] as const;

export const SOURCE_TABLE_NAME = /^[A-Za-z_][A-Za-z0-9_$]*(\.[A-Za-z_][A-Za-z0-9_$]*)?$/;
