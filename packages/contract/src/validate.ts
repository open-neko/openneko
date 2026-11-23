export interface Schema {
  $ref?: string;
  type?: string | string[];
  properties?: Record<string, Schema>;
  required?: string[];
  additionalProperties?: boolean | Schema;
  items?: Schema;
  enum?: unknown[];
  oneOf?: Schema[];
  anyOf?: Schema[];
  description?: string;
  format?: string;
}

function typeOf(value: unknown): string {
  if (value === null) return "null";
  if (Array.isArray(value)) return "array";
  if (typeof value === "number") return Number.isInteger(value) ? "integer" : "number";
  return typeof value;
}

function typeFits(expected: string, actual: string): boolean {
  return expected === actual || (expected === "number" && actual === "integer");
}

export function validate(
  value: unknown,
  schema: Schema,
  components: Record<string, Schema> = {},
  at = "$",
): string[] {
  if (schema.$ref) {
    const name = schema.$ref.replace("#/components/schemas/", "");
    const target = components[name];
    if (!target) return [`${at}: unknown schema ${schema.$ref}`];
    return validate(value, target, components, at);
  }
  if (schema.oneOf || schema.anyOf) {
    const options = (schema.oneOf ?? schema.anyOf)!;
    const results = options.map((option) => validate(value, option, components, at));
    const passing = results.filter((r) => r.length === 0).length;
    if (schema.oneOf ? passing !== 1 : passing === 0) {
      return [`${at}: matches ${passing} of ${options.length} alternatives`];
    }
  }
  const actual = typeOf(value);
  if (schema.type) {
    const types = Array.isArray(schema.type) ? schema.type : [schema.type];
    if (!types.some((t) => typeFits(t, actual))) return [`${at}: expected ${types.join(" or ")}, got ${actual}`];
  }
  if (schema.enum && !schema.enum.some((e) => e === value)) {
    return [`${at}: ${JSON.stringify(value)} is not one of ${JSON.stringify(schema.enum)}`];
  }
  const errors: string[] = [];
  if (actual === "object") {
    const record = value as Record<string, unknown>;
    for (const name of schema.required ?? []) {
      if (!(name in record)) errors.push(`${at}: missing required property ${name}`);
    }
    for (const [name, child] of Object.entries(record)) {
      const declared = schema.properties?.[name];
      if (declared) errors.push(...validate(child, declared, components, `${at}.${name}`));
      else if (schema.additionalProperties === false) errors.push(`${at}: property ${name} is not in the contract`);
      else if (typeof schema.additionalProperties === "object") {
        errors.push(...validate(child, schema.additionalProperties, components, `${at}.${name}`));
      }
    }
  }
  if (actual === "array" && schema.items) {
    (value as unknown[]).forEach((item, i) => errors.push(...validate(item, schema.items!, components, `${at}[${i}]`)));
  }
  return errors;
}
