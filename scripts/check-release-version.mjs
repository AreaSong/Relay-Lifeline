import fs from "node:fs";

const packageJSON = JSON.parse(fs.readFileSync(new URL("../web/package.json", import.meta.url), "utf8"));
const lockJSON = JSON.parse(fs.readFileSync(new URL("../web/package-lock.json", import.meta.url), "utf8"));
const changelog = fs.readFileSync(new URL("../CHANGELOG.md", import.meta.url), "utf8");
const buildInfo = fs.readFileSync(new URL("../internal/buildinfo/info.go", import.meta.url), "utf8");
const configSource = fs.readFileSync(new URL("../internal/config/config.go", import.meta.url), "utf8");
const ref = process.argv[2] || process.env.GITHUB_REF_NAME || "";
const version = ref.startsWith("v") ? ref.slice(1) : packageJSON.version;
const adminAPIVersion = buildInfo.match(/const AdminAPIVersion = "([^"]+)"/)?.[1];
const configSchemaVersion = Number(configSource.match(/const CurrentSchemaVersion = (\d+)/)?.[1]);

if (packageJSON.version !== lockJSON.packages?.[""]?.version) {
  throw new Error(`web package.json (${packageJSON.version}) and package-lock.json (${lockJSON.packages?.[""]?.version}) differ`);
}
if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(version)) {
  throw new Error(`invalid release version: ${version}`);
}
if (ref.startsWith("v") && packageJSON.version !== version) {
  throw new Error(`tag ${ref} does not match web/package.json ${packageJSON.version}`);
}
if (!changelog.includes(`## [${version}]`)) {
  throw new Error(`CHANGELOG.md has no section for ${version}`);
}
if (adminAPIVersion !== "3") {
  throw new Error(`Admin API version must be 3, received ${adminAPIVersion || "missing"}`);
}
if (configSchemaVersion !== 5) {
	throw new Error(`config schema version must be 5, received ${configSchemaVersion || "missing"}`);
}
console.log(`release version verified: ${version}, Admin API v${adminAPIVersion}, config schema v${configSchemaVersion}`);
