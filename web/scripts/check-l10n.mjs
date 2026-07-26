import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const webRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const projectRoot = path.resolve(webRoot, "..");
const failures = [];

function readJSON(file) {
  return JSON.parse(fs.readFileSync(file, "utf8"));
}

function flatten(value, prefix = "") {
  const entries = [];
  for (const [key, child] of Object.entries(value)) {
    const fullKey = prefix ? `${prefix}.${key}` : key;
    if (child && typeof child === "object" && !Array.isArray(child)) {
      entries.push(...flatten(child, fullKey));
    } else {
      entries.push([fullKey, child]);
    }
  }
  return entries;
}

function parameters(value) {
  return [...String(value).matchAll(/{{\s*\.?([A-Za-z0-9_]+)\s*}}/g)]
    .map((match) => match[1])
    .sort();
}

function compareCatalogs(label, leftEntries, rightEntries) {
  const left = new Map(leftEntries);
  const right = new Map(rightEntries);
  for (const [key, value] of left) {
    if (!right.has(key)) failures.push(`${label}: zh-CN 缺少键 ${key}`);
    if (typeof value !== "string" || value.trim() === "") failures.push(`${label}: en-US 的 ${key} 为空`);
  }
  for (const [key, value] of right) {
    if (!left.has(key)) failures.push(`${label}: en-US 缺少键 ${key}`);
    if (typeof value !== "string" || value.trim() === "") failures.push(`${label}: zh-CN 的 ${key} 为空`);
  }
  for (const key of new Set([...left.keys(), ...right.keys()])) {
    if (!left.has(key) || !right.has(key)) continue;
    const leftParams = parameters(left.get(key));
    const rightParams = parameters(right.get(key));
    if (JSON.stringify(leftParams) !== JSON.stringify(rightParams)) {
      failures.push(`${label}: ${key} 的插值参数不一致 (${leftParams.join(",")} / ${rightParams.join(",")})`);
    }
  }
}

const localeRoot = path.join(webRoot, "src", "locales");
const englishNamespaces = fs.readdirSync(path.join(localeRoot, "en-US")).filter((name) => name.endsWith(".json")).sort();
const chineseNamespaces = fs.readdirSync(path.join(localeRoot, "zh-CN")).filter((name) => name.endsWith(".json")).sort();
if (JSON.stringify(englishNamespaces) !== JSON.stringify(chineseNamespaces)) {
  failures.push(`前端命名空间不一致 (${englishNamespaces.join(",")} / ${chineseNamespaces.join(",")})`);
}
for (const namespace of new Set([...englishNamespaces, ...chineseNamespaces])) {
  const englishFile = path.join(localeRoot, "en-US", namespace);
  const chineseFile = path.join(localeRoot, "zh-CN", namespace);
  if (!fs.existsSync(englishFile) || !fs.existsSync(chineseFile)) continue;
  compareCatalogs(`前端 ${namespace}`, flatten(readJSON(englishFile)), flatten(readJSON(chineseFile)));
}

function backendEntries(locale) {
  const file = path.join(projectRoot, "internal", "l10n", "locales", `active.${locale}.json`);
  const catalog = readJSON(file);
  const seen = new Set();
  const entries = [];
  for (const message of catalog) {
    if (!message || typeof message.id !== "string") {
      failures.push(`后端 ${locale}: 消息格式无效`);
      continue;
    }
    if (seen.has(message.id)) failures.push(`后端 ${locale}: 重复消息 ID ${message.id}`);
    seen.add(message.id);
    entries.push([message.id, message.translation]);
  }
  return entries;
}

compareCatalogs("后端", backendEntries("en-US"), backendEntries("zh-CN"));

if (failures.length) {
  console.error(`本地化检查失败（${failures.length} 项）：`);
  for (const failure of failures) console.error(`- ${failure}`);
  process.exit(1);
}
console.log(`本地化检查通过：${englishNamespaces.length} 个前端命名空间，后端双语消息完整。`);
