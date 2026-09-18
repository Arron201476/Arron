import type { ChangeEvent } from "react";
import type { JSONSchema } from "../../types";

type JSONObject = Record<string, unknown>;

export function schemaRequiresMaterial(schema: JSONSchema): boolean {
  if (schema.required?.some((name) => ["assets", "artifact_versions", "asset_set"].includes(name))) return true;
  if (schema.allOf?.some(schemaRequiresMaterial)) return true;
  return [schema.anyOf, schema.oneOf].some((alternatives) => Boolean(alternatives?.length && alternatives.every(schemaRequiresMaterial)));
}

type JSONSchemaFormProps = {
  schema: JSONSchema;
  value: JSONObject;
  onChange: (value: JSONObject) => void;
  disabled?: boolean;
};

function schemaType(schema: JSONSchema): string {
  if (Array.isArray(schema.type)) return schema.type.find((item) => item !== "null") ?? "";
  if (schema.type) return schema.type;
  if (schema.properties) return "object";
  if (schema.enum?.length) return typeof schema.enum[0];
  return "";
}

function cloneJSON<T>(value: T): T {
  if (value === undefined) return value;
  return JSON.parse(JSON.stringify(value)) as T;
}

function defaultValue(schema: JSONSchema, required = false): unknown {
  if (schema.default !== undefined) return cloneJSON(schema.default);
  if (schema.const !== undefined) return cloneJSON(schema.const);
  const type = schemaType(schema);
  if (type === "object") {
    const result: JSONObject = {};
    for (const [name, child] of Object.entries(schema.properties ?? {})) {
      const value = defaultValue(child, schema.required?.includes(name));
      if (value !== undefined) result[name] = value;
    }
    return Object.keys(result).length > 0 || required ? result : undefined;
  }
  if (type === "boolean" && required) return false;
  return undefined;
}

function mergeObjects(base: JSONObject, override: JSONObject): JSONObject {
  const result = { ...base };
  for (const [key, value] of Object.entries(override)) {
    if (
      value && typeof value === "object" && !Array.isArray(value) &&
      result[key] && typeof result[key] === "object" && !Array.isArray(result[key])
    ) {
      result[key] = mergeObjects(result[key] as JSONObject, value as JSONObject);
    } else {
      result[key] = cloneJSON(value);
    }
  }
  return result;
}

export function schemaDefaults(schema: JSONSchema, saved: JSONObject = {}): JSONObject {
  const defaults = defaultValue(schema, true);
  const base = defaults && typeof defaults === "object" && !Array.isArray(defaults)
    ? defaults as JSONObject
    : {};
  return mergeObjects(base, saved);
}

export function schemaCanUseForm(schema: JSONSchema): boolean {
  if (schema.anyOf || schema.oneOf || schema.not || schema.if || schema.then || schema.else || schema.allOf) return false;
  if (schema.const !== undefined || schema.enum?.length) return true;
  const type = schemaType(schema);
  if (["string", "number", "integer", "boolean"].includes(type)) return true;
  if (type === "object") {
    const properties = schema.properties ?? {};
    return (Object.keys(properties).length > 0 || schema.additionalProperties === false) &&
      Object.values(properties).every(schemaCanUseForm);
  }
  if (type === "array") {
    if (!schema.items) return false;
    const itemType = schemaType(schema.items);
    return !schema.items.anyOf && !schema.items.oneOf && ["string", "number", "integer"].includes(itemType);
  }
  return false;
}

function valueMatchesSchema(schema: JSONSchema, value: unknown, required: boolean): boolean {
  if (value === undefined || value === null) return !required || (value === null && Array.isArray(schema.type) && schema.type.includes("null"));
  if (schema.const !== undefined && JSON.stringify(value) !== JSON.stringify(schema.const)) return false;
  if (schema.enum && !schema.enum.some((item) => JSON.stringify(item) === JSON.stringify(value))) return false;
  const type = schemaType(schema);
  if (type === "string") {
    if (typeof value !== "string") return false;
    if (schema.minLength !== undefined && value.length < schema.minLength) return false;
    if (schema.maxLength !== undefined && value.length > schema.maxLength) return false;
    if (schema.pattern) {
      try { if (!new RegExp(schema.pattern).test(value)) return false; } catch { return false; }
    }
    return true;
  }
  if (type === "number" || type === "integer") {
    if (typeof value !== "number" || !Number.isFinite(value)) return false;
    if (type === "integer" && !Number.isInteger(value)) return false;
    if (schema.minimum !== undefined && value < schema.minimum) return false;
    if (schema.maximum !== undefined && value > schema.maximum) return false;
    return true;
  }
  if (type === "boolean") return typeof value === "boolean";
  if (type === "array") {
    if (!Array.isArray(value)) return false;
    if (schema.minItems !== undefined && value.length < schema.minItems) return false;
    if (schema.maxItems !== undefined && value.length > schema.maxItems) return false;
    return !schema.items || value.every((item) => valueMatchesSchema(schema.items!, item, true));
  }
  if (type === "object") {
    if (typeof value !== "object" || Array.isArray(value)) return false;
    const object = value as JSONObject;
    if (schema.additionalProperties === false && Object.keys(object).some((key) => !(key in (schema.properties ?? {})))) return false;
    return Object.entries(schema.properties ?? {}).every(([name, child]) =>
      valueMatchesSchema(child, object[name], Boolean(schema.required?.includes(name))),
    );
  }
  return false;
}

export function schemaValueIsValid(schema: JSONSchema, value: JSONObject): boolean {
  return valueMatchesSchema(schema, value, true);
}

function fieldLabel(name: string, schema: JSONSchema): string {
  return schema.title?.trim() || name.replaceAll("_", " ");
}

function SchemaField({ name, path, schema, value, required, disabled, onChange }: {
  name: string;
  path: string;
  schema: JSONSchema;
  value: unknown;
  required: boolean;
  disabled: boolean;
  onChange: (value: unknown) => void;
}) {
  const label = fieldLabel(name, schema);
  const id = `schema-field-${path.replace(/[^a-zA-Z0-9_-]/g, "-")}`;
  const description = schema.description?.trim();
  if (schema.const !== undefined) {
    return <div className="schema-constant"><span>{label}</span><code>{String(schema.const)}</code></div>;
  }
  if (schema.enum?.length) {
    const selected = value === undefined ? "" : JSON.stringify(value);
    return <label htmlFor={id}><span>{label}{required ? " *" : ""}</span><select id={id} value={selected} required={required} disabled={disabled} onChange={(event) => onChange(event.target.value === "" ? undefined : JSON.parse(event.target.value))}><option value="">请选择</option>{schema.enum.map((option) => <option key={JSON.stringify(option)} value={JSON.stringify(option)}>{String(option)}</option>)}</select>{description && <small>{description}</small>}</label>;
  }
  const type = schemaType(schema);
  if (type === "boolean") {
    return <label className="schema-checkbox" htmlFor={id}><input id={id} type="checkbox" checked={value === true} disabled={disabled} onChange={(event) => onChange(event.target.checked)} /><span>{label}{required ? " *" : ""}</span>{description && <small>{description}</small>}</label>;
  }
  if (type === "number" || type === "integer") {
    return <label htmlFor={id}><span>{label}{required ? " *" : ""}</span><input id={id} type="number" value={typeof value === "number" ? String(value) : ""} required={required} min={schema.minimum} max={schema.maximum} step={schema.multipleOf ?? (type === "integer" ? 1 : "any")} disabled={disabled} onChange={(event) => onChange(event.target.value === "" ? undefined : Number(event.target.value))} />{description && <small>{description}</small>}</label>;
  }
  if (type === "string") {
    const common = { id, value: typeof value === "string" ? value : "", required, disabled, minLength: schema.minLength, maxLength: schema.maxLength, onChange: (event: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => onChange(event.target.value) };
    return <label htmlFor={id}><span>{label}{required ? " *" : ""}</span>{(schema.maxLength ?? 0) > 160 || schema.format === "multiline" ? <textarea {...common} /> : <input {...common} type="text" />}{description && <small>{description}</small>}</label>;
  }
  if (type === "array" && schema.items) {
    const itemType = schemaType(schema.items);
    const text = Array.isArray(value) ? value.map(String).join("\n") : "";
    return <label htmlFor={id}><span>{label}{required ? " *" : ""}</span><textarea id={id} value={text} required={required} disabled={disabled} onChange={(event) => {
      const items = event.target.value.split(/[\n,]/).map((item) => item.trim()).filter(Boolean);
      onChange(itemType === "string" ? items : items.map(Number).filter(Number.isFinite));
    }} />{description && <small>{description}</small>}</label>;
  }
  if (type === "object") {
    const object = value && typeof value === "object" && !Array.isArray(value) ? value as JSONObject : {};
    return <fieldset className="schema-object"><legend>{label}{required ? " *" : ""}</legend>{description && <p>{description}</p>}<SchemaObjectFields schema={schema} value={object} disabled={disabled} onChange={onChange} path={path} /></fieldset>;
  }
  return null;
}

function SchemaObjectFields({ schema, value, disabled, onChange, path }: {
  schema: JSONSchema;
  value: JSONObject;
  disabled: boolean;
  onChange: (value: JSONObject) => void;
  path: string;
}) {
  return <>{Object.entries(schema.properties ?? {}).map(([name, child]) => <SchemaField key={name} name={name} path={`${path}-${name}`} schema={child} value={value[name]} required={Boolean(schema.required?.includes(name))} disabled={disabled} onChange={(next) => {
    const updated = { ...value };
    if (next === undefined) delete updated[name]; else updated[name] = next;
    onChange(updated);
  }} />)}</>;
}

export function JsonSchemaForm({ schema, value, onChange, disabled = false }: JSONSchemaFormProps) {
  return <div className="schema-form"><SchemaObjectFields schema={schema} value={value} disabled={disabled} onChange={onChange} path="config" /></div>;
}
