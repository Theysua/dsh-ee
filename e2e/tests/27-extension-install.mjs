// 声明式扩展：管理员上传清单和接口文档 → 安装前看清楚 → 装上 → 经 /mcp 调用（管理员设置的参数模型改不了，
// 返回只留清单指定的字段）→ 上传新版本升级（从只读变成写操作的工具先不启用）。对方系统就用 dsh-ee 自己的 /healthz。
import { ADMIN_PW, BASE, api, assert, assertEq, login, newPage, shot, step, tag } from "../lib.mjs";

const name = `health-${tag}`;

const openapi = `openapi: 3.0.3
info: { title: 健康检查, version: "1" }
paths:
  /healthz:
    get:
      operationId: get_health
      summary: 服务是否正常
      parameters:
        - { name: probe, in: query, required: true, schema: { type: string } }
        - { name: verbose, in: query, schema: { type: boolean } }
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: { type: object, properties: { status: { type: string }, version: { type: string } } }
    post:
      operationId: restart
      summary: 重启服务
`;

function manifest(version, readOnly) {
  return JSON.stringify(
    {
      id: `e2e/${name}`,
      version,
      name: `E2E 健康检查扩展 ${tag}`,
      description: "看 dsh-ee 是否正常",
      kind: "declarative",
      openapi: "openapi.yaml",
      servers: [BASE],
      tools: [
        {
          name: "get_health",
          ...(readOnly ? { readOnly: true } : {}),
          description: "看服务是否正常",
          params: [{ name: "probe", fill: "admin", label: "探针名", default: "e2e" }],
          keep: ["status"],
        },
      ],
      ...(readOnly ? {} : { checkIgnore: [{ code: "tool.readonly_unmarked", tool: "get_health", reason: "新版本里这个接口会记一次探测" }] }),
    },
    null,
    2,
  );
}

const upload = (page, version, readOnly) =>
  page.locator('input[type="file"]').setInputFiles([
    { name: "dsh-ee.extension.json", mimeType: "application/json", buffer: Buffer.from(manifest(version, readOnly)) },
    { name: "openapi.yaml", mimeType: "text/yaml", buffer: Buffer.from(openapi) },
  ]);

async function mcp(token, method, params) {
  const r = await fetch(BASE + "/mcp", {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: 1, method, ...(params ? { params } : {}) }),
  });
  return r.json();
}

export default async function (t) {
  const admin = await newPage(t);
  await login(admin, "admin", ADMIN_PW);

  // ---- 安装前：管理员看得到请求发往哪里、哪些是写操作、哪些参数由他设置 ----
  await admin.goto(`${BASE}/admin/connections`);
  await admin.getByRole("button", { name: "安装扩展" }).click();
  await admin.waitForURL(/\/extensions\/install$/);
  await upload(admin, "1.0.0", true);
  await admin.getByText("已通过检查").waitFor();
  await admin.getByText(`e2e/${name}@1.0.0`).waitFor();
  await admin.getByText("不带凭据").waitFor();
  await admin.getByText("POST /healthz").waitFor();
  await admin.getByText("不由模型填、由你统一设置的参数：probe").waitFor();
  await admin.getByText("返回给模型时只保留：status").waitFor();
  assertEq(await admin.getByLabel("连接名").inputValue(), name, "连接名默认取 id 里的名字");
  assertEq(await admin.getByLabel("探针名").inputValue(), "e2e", "管理员参数预先填好清单里的值");
  await admin.getByLabel("探针名").fill("from-admin");
  step("安装前列出地址、凭据方式、工具、写操作和由管理员设置的参数");
  await shot(admin, "extension-install");

  await admin.getByRole("button", { name: "安装", exact: true }).click();
  await admin.waitForURL(/\/assets\/\d+$/);
  const id = Number(admin.url().split("/").pop());
  await admin.getByText(`e2e/${name}@1.0.0`).waitFor();
  let d = await api(admin, "GET", `/api/hub/${id}`);
  const tool = (n) => d.data.tools.find((x) => x.name === n);
  assert(tool("get_health").enabled && tool("get_health").read_only && !tool("restart").enabled, "只读工具默认启用，写操作默认不启用：" + JSON.stringify(d.data.tools));
  assertEq(d.data.asset.spec.url, BASE, "接口根地址是清单里的服务器地址");
  step("装好后是一个企业连接：只读工具可用，写操作等管理员打开");
  await shot(admin, "extension-installed");

  // ---- 调用：管理员设置的参数不在工具定义里，模型带了也不算；返回只留 status ----
  const tok = await api(admin, "POST", "/api/me/tokens", { name: "ext", scopes: ["hub", "connections"] });
  assertEq(tok.status, 201, "接入令牌");
  const token = tok.data.token;
  const list = await mcp(token, "tools/list");
  const def = list.result.tools.find((x) => x.name === `${name}__get_health`);
  assert(def && !("probe" in def.inputSchema.properties) && "verbose" in def.inputSchema.properties, "给模型的定义里不该有 probe：" + JSON.stringify(def));
  assert(!list.result.tools.some((x) => x.name === `${name}__restart`), "没启用的写操作不该下发");
  const call = await mcp(token, "tools/call", { name: `${name}__get_health`, arguments: { probe: "from-model" } });
  assertEq(call.result.content[0].text, '{"status":"ok"}', "返回只留清单指定的字段");
  d = await api(admin, "GET", `/api/hub/${id}`);
  assertEq(d.data.calls[0].extension, `e2e/${name}@1.0.0`, "调用记录写明扩展版本");
  step("经 /mcp 调用：返回只留 status，调用记录里有扩展的版本");

  // ---- 升级：get_health 不再是只读，升级后要管理员重新打开 ----
  await admin.goto(`${BASE}/assets/${id}`);
  await admin.getByRole("button", { name: "扩展设置 / 升级" }).click();
  await admin.waitForURL(/\/extensions\/install\?id=\d+$/);
  await admin.getByText("下面是现在装的版本").waitFor();
  assertEq(await admin.getByLabel("探针名").inputValue(), "from-admin", "带出现在设置的参数值");
  await upload(admin, "1.1.0", false);
  await admin.getByText(`从 e2e/${name}@1.0.0 换成 e2e/${name}@1.1.0`).waitFor();
  await admin.getByText("升级后先不启用，要你重新打开：get_health").waitFor();
  await shot(admin, "extension-upgrade");
  await admin.getByRole("button", { name: "升级到 1.1.0" }).click();
  await admin.waitForURL(/\/assets\/\d+$/);
  await admin.getByText(`e2e/${name}@1.1.0`).waitFor();
  d = await api(admin, "GET", `/api/hub/${id}`);
  assert(!tool("get_health").enabled && !tool("get_health").read_only, "变成写操作的工具升级后先不启用：" + JSON.stringify(d.data.tools));
  const after = await mcp(token, "tools/call", { name: `${name}__get_health`, arguments: {} });
  assert(after.error, "升级后还没重新打开的工具不该能调：" + JSON.stringify(after));
  step("升级时指出变成写操作的工具，升级后它先不启用");

  // ---- 没通过检查的扩展装不了，问题和改法都列出来 ----
  await admin.goto(`${BASE}/extensions/install`);
  await admin.locator('input[type="file"]').setInputFiles([
    { name: "dsh-ee.extension.json", mimeType: "application/json", buffer: Buffer.from(manifest("1.0.0", true).replace(`e2e/${name}`, "E2E Health")) },
    { name: "openapi.yaml", mimeType: "text/yaml", buffer: Buffer.from(openapi) },
  ]);
  await admin.getByText("这个扩展不能安装").waitFor();
  await admin.getByText("id.invalid").waitFor();
  assertEq(await admin.getByRole("button", { name: "安装", exact: true }).count(), 0, "没通过检查时没有安装按钮");
  step("没通过检查的扩展不能安装，页面列出问题和改法");
  await shot(admin, "extension-failed");
}
