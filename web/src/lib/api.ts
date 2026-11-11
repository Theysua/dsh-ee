/** 与 server/ 对接的接口层。类型与 server/internal/store 的 JSON 字段一一对应。 */
import i18n from "../i18n";
import type { DshClient, DshRelease, Plugin, PluginKind, PluginStatus, Settings } from "./types";

export interface RoleRef {
  id: number;
  code: string;
  name: string;
  is_super: boolean;
}

export interface User {
  id: number;
  username: string;
  display_name: string;
  department: string;
  roles: RoleRef[] | null;
  disabled: boolean;
  created_at: string;
  /** 用户级月度额度：null = 不限额，0 = 未分配，>0 = 每月 token 上限。 */
  monthly_token_limit: number | null;
  allowed_models: string[] | null;
  quota_expires_at: string | null;
  /** 本月（UTC）已用 token，管理端列表查询时填充。 */
  month_tokens: number;
  /** local：本地密码 / ldap：由企业目录认证，资料在每次登录时同步。 */
  auth_source: "local" | "ldap" | "oidc";
  account_type: "human" | "bot";
  owner_id: number | null;
  email: string;
  external_dn?: string;
  last_login_at: string | null;
}

/** 当前登录用户：带有效权限。前端据此决定显示什么，后端每个接口仍会自行校验。 */
/** 公开演示站的账号与重置周期；正常部署为 null。 */
export interface DemoSite {
  username: string;
  password: string;
  reset?: string;
  /** 下次重置的时间点（RFC 3339）；服务端没配重置间隔时没有 */
  next_reset?: string;
  /** 登录页的场景卡片：以哪位示例员工的身份、打开哪个应用、问什么 */
  personas?: DemoPersona[];
  /** 有介绍视频的语言（zh / en），首页播放 /demo/intro/<语言> */
  videos?: string[];
}

export interface DemoPersona {
  username: string;
  display_name: string;
  department: string;
  title: string;
  app: string;
  app_title: string;
  try: string;
}

export interface Me extends User {
  is_super: boolean;
  permissions: string[];
  /** 从 DSH 直接打开的网页会话：只带员工权限，「我的令牌」和管理功能要输入密码登录 */
  limited?: boolean;
  /** 账号本身有管理权限，输入密码登录后可用 */
  reauth?: boolean;
}

export interface Role {
  id: number;
  code: string;
  name: string;
  description: string;
  is_super: boolean;
  builtin: boolean;
  permissions: string[];
  user_count: number;
}

export interface Permission {
  code: string;
  name: string;
  group: string;
  description: string;
  builtin: boolean;
}

export type MenuKind = "group" | "page" | "link";

/** 菜单即页面配置：前端只认 page_key 对应的组件，其余都可在线修改。 */
export interface Menu {
  id: number;
  parent_id: number | null;
  kind: MenuKind;
  title: string;
  path: string;
  page_key: string;
  icon: string;
  permission: string;
  sort: number;
  visible: boolean;
}

export type MenuInput = Omit<Menu, "id">;

/** 员工自己的额度视图，所有 key 共享。 */
export interface MyQuota {
  monthly_token_limit: number | null;
  month_tokens: number;
  allowed_models: string[] | null;
  expires_at: string | null;
  active_keys: number;
  max_keys: number;
}

export interface Provider {
  id: number;
  name: string;
  vendor: string;
  base_url: string;
  api_key_hint: string;
  /** 同一对外模型挂在多个上游时，数字小的先用。 */
  priority: number;
  /** 这个上游上启用中的对外模型名。 */
  models: string[];
  enabled: boolean;
  last_check_at: string | null;
  last_check_ok: boolean | null;
  last_check_msg: string;
  created_at: string;
  updated_at: string;
}

/** 对外模型：员工调用时写 name，转发给上游时换成 upstream_model。 */
export interface Model {
  id: number;
  provider_id: number;
  provider_name: string;
  name: string;
  upstream_model: string;
  display_name: string;
  enabled: boolean;
}

export interface GetStarted {
  code: string;
  expires_at: string;
  /** 贴进 DSH 的第一段话：装插件、引导登录。 */
  prompt: string;
  /** 登录（与管理模式）之后接着说的话。 */
  next_prompt: string;
  tarball_url: string;
  package: string;
  version: string;
  login_command: string;
}

export interface GetStartedStatus {
  stage: "waiting" | "pending" | "approved" | "connected" | "denied";
  expires_at: string;
  user_code?: string;
  client_name?: string;
  client_os?: string;
  client_ip?: string;
  token_id?: number;
  admin_live?: boolean;
}

export interface CheckResult {
  ok: boolean;
  status: number;
  latency_ms: number;
  message: string;
  /** 上游 /models 返回的模型 id。 */
  models: string[];
}

export interface ApiKey {
  id: number;
  user_id: number;
  username: string;
  name: string;
  prefix: string;
  monthly_token_limit: number | null;
  allowed_models: string[] | null;
  expires_at: string | null;
  revoked_at: string | null;
  last_used_at: string | null;
  created_by: number;
  created_at: string;
  month_tokens: number;
}

// ---- npm 源与能力 ----

/** 自动检查的一项：key 与 params 交给 i18n 渲染（registry.scan.*）。 */
export interface ScanItem {
  key: string;
  level: "pass" | "warn" | "fail";
  params?: Record<string, string | number>;
}

/** 发布前预检的结果（POST /api/me/packages/check），不落库。 */
export interface PackageCheck {
  name: string;
  version: string;
  is_plugin: boolean;
  can_submit: boolean;
  will_publish: boolean;
  requires_review: boolean;
  scan: ScanItem[];
  conflicts: { key: string; params?: Record<string, string | number> }[];
}

/** 能力列表里的一项（server/internal/api/catalog.go 的 catalogItem）。 */
export interface CatalogPlugin {
  name: string;
  title: string;
  description: string;
  author: string;
  icon: string;
  homepage: string;
  repository: string;
  category: string;
  kind: string;
  featured: boolean;
  is_plugin: boolean;
  origin: "internal" | "upstream";
  stars: number | null;
  score: number | null;
  version: string;
  published_at: string | null;
  installs: number;
  dep_count: number | null;
  size: number;
  closure_size: number | null;
  offline_ready: boolean | null;
  has_install_script: boolean;
  status: PluginStatus;
  pending: number;
  owner: string;
  owner_name: string;
  department: string;
  updated_at: string;
}

export interface PackageVersion {
  name: string;
  version: string;
  integrity: string;
  shasum: string;
  size: number;
  is_plugin: boolean;
  has_install_script: boolean;
  status: PluginStatus;
  scan: ScanItem[] | null;
  publisher_id: number | null;
  publisher?: string;
  review_note: string;
  reviewed_by: number | null;
  reviewer?: string;
  reviewed_at: string | null;
  deprecated: string | null;
  created_at: string;
  published_at: string | null;
}

export interface PackageDetail extends CatalogPlugin {
  versions: PackageVersion[];
  dist_tags: Record<string, string>;
  scan: ScanItem[] | null;
  readme: string;
  engines: Record<string, string> | null;
  peer_dependencies: Record<string, string> | null;
  dependencies: Record<string, string> | null;
  deprecated: string | null;
}

export interface ReviewItem extends PackageVersion {
  package: CatalogPlugin;
  publisher: string;
  publisher_name: string;
  department: string;
  previous: string;
  engines: Record<string, string> | null;
  dependencies: Record<string, string> | null;
  peer_dependencies: Record<string, string> | null;
  scripts: Record<string, string> | null;
}

export type ReviewAction = "approve" | "reject" | "unpublish" | "restore";

export interface MyPackage extends CatalogPlugin {
  versions: PackageVersion[];
}

export interface NpmToken {
  id: number;
  user_id: number;
  username: string;
  name: string;
  prefix: string;
  access: "read" | "publish";
  last_used_at: string | null;
  created_at: string;
}

export interface RegistryOverview {
  stats: {
    hosted_packages: number;
    upstream_packages: number;
    upstream_plugins: number;
    versions: number;
    pending: number;
    storage_bytes: number;
    downloads_30d: number;
  };
  scopes: string[];
  upstream: string;
  proxying: boolean;
  registry_url: string;
  index_url: string;
}

export interface SyncJob {
  id: number;
  kind: "preload" | "request" | "update" | "import";
  status: "running" | "succeeded" | "partial" | "failed" | "interrupted";
  total: number;
  done: number;
  failed: number;
  bytes: number;
  log: string;
  created_at: string;
  finished_at: string | null;
}

export interface FeedPlugin {
  name: string;
  title?: string;
  description?: string;
  author?: string;
  icon?: string;
  homepage?: string;
  repository?: string;
  category?: string;
  featured?: boolean;
  stars?: number;
  score?: number;
  version?: string;
  published_at?: number;
  local_version: string;
  local_status: PluginStatus | "";
  dep_count: number | null;
  closure_size: number | null;
  offline_ready: boolean | null;
}

// ---- 统计与审计 ----

export interface DailyStat {
  date: string;
  installs: number;
  install_users: number;
  downloads: number;
  calls: number;
  call_errors: number;
  call_users: number;
  tokens: number;
}

export interface Stats {
  from: string;
  daily: DailyStat[];
  totals: { installs: number; downloads: number; calls: number; call_errors: number; tokens: number; users: number; active_users: number };
  top_plugins: { name: string; title: string; origin: "hosted" | "upstream"; installs: number; users: number }[];
  top_models: { model: string; calls: number; users: number; tokens: number }[];
  departments: { department: string; users: number; active_users: number; installs: number; calls: number; tokens: number }[];
  platforms: { os: string; arch: string; users: number; installs: number }[];
  storage: { origin: "hosted" | "upstream"; versions: number; bytes: number }[];
  token_stats?: { id: number; name: string; username: string; account_type: "human" | "bot"; tokens: number }[];
}

export type TokenScope = "models" | "registry:read" | "registry:publish" | "hub" | "connections" | "agent" | "admin";
export type TokenSource = "manual" | "device" | "issued" | "legacy";

export interface TokenView {
  id: string;
  kind: "access" | "npm" | "key";
  user_id?: number;
  username?: string;
  department?: string;
  account_type?: "human" | "bot";
  name: string;
  prefix: string;
  source: TokenSource;
  scopes: TokenScope[];
  client_name: string;
  client_os: string;
  monthly_token_limit: number | null;
  month_used: number;
  allowed_models: string[];
  expires_at: string | null;
  last_used_at: string | null;
  last_ip: string;
  revoked_at: string | null;
  created_at: string;
  /** 管理模式的到期时间；没开启或已到期时为空。 */
  admin_until?: string | null;
}

export interface TokenInput {
  name: string;
  scopes: TokenScope[];
  monthly_token_limit?: number | null;
  allowed_models?: string[];
  expires_in_days?: number;
  user_id?: number;
}

export interface DeviceInfo {
  user_code: string;
  client_name: string;
  client_os: string;
  client_version: string;
  client_ip: string;
  scopes: TokenScope[];
  expires_at: string;
  replace_existing: boolean;
}

export interface DeviceCodeResponse {
  device_code: string;
  user_code: string;
  verification_uri: string;
  verification_uri_complete: string;
  interval: number;
  expires_in: number;
}

export interface MyOverview {
  installed: { name: string; version: string; installed_at: string; title: string; latest: string; has_update: boolean }[];
  updates: number;
  picks: { name: string; users: number; title: string; description: string; kind: string; category: string; origin: "internal" | "upstream" }[];
  /** department：同部门同事在用；company：部门里没有可推荐的，按全公司算。 */
  picks_scope: "department" | "company";
  contributions: { name: string; title: string; version: string; status: PluginStatus; pending: number; users: number }[];
  /** 装过我分享的任一能力的同事人数。 */
  reach: number;
}

export interface AuditLog {
  id: number;
  actor_id: number | null;
  actor_name: string;
  action: string;
  target: string;
  detail: Record<string, unknown> | null;
  ip: string;
  via: "web" | "token" | "agent" | "system";
  token_id: number | null;
  client: string;
  created_at: string;
  actor_display_name: string;
  actor_department: string;
}

export interface AuditQuery {
  before?: number;
  limit?: number;
  action?: string;
  q?: string;
  since?: string;
  until?: string;
  via?: string;
}

// ---- LDAP ----

export interface LdapConfig {
  enabled: boolean;
  url: string;
  startTls: boolean;
  insecureSkipVerify: boolean;
  caCert: string;
  bindDn: string;
  baseDn: string;
  userFilter: string;
  usernameAttr: string;
  displayNameAttr: string;
  departmentAttr: string;
  emailAttr: string;
  groupMode: "memberOf" | "search" | "none";
  groupBaseDn: string;
  groupFilter: string;
  groupNameAttr: string;
  roleMappings: { group: string; roleId: number }[];
  autoCreate: boolean;
  syncDisable: boolean;
  timeoutSeconds: number;
}

export interface LdapSyncResult {
  at: string;
  ok: boolean;
  error?: string;
  checked: number;
  updated: number;
  disabled: string[];
  missing: string[];
}

export interface LdapView extends LdapConfig {
  bindPasswordSet: boolean;
  lastSync: LdapSyncResult | null;
}

/** 提交时 bindPassword 不传表示沿用已存的，传空串表示清除。 */
export type LdapInput = LdapConfig & { bindPassword?: string };

export interface OidcConfig {
  enabled: boolean;
  preset: "keycloak" | "authing" | "azure" | "custom";
  name: string;
  issuer: string;
  clientId: string;
  scopes: string;
  usernameClaim: string;
  nameClaim: string;
  emailClaim: string;
  departmentClaim: string;
  groupsClaim: string;
  roleMappings: { group: string; roleId: number }[];
  autoCreate: boolean;
}

export interface OidcView extends OidcConfig {
  clientSecretSet: boolean;
  redirectUri: string;
}

export type OidcInput = OidcConfig & { clientSecret?: string };

export interface OidcTestResult {
  ok: boolean;
  error?: string;
  provider?: { issuer: string; authorization_endpoint: string; token_endpoint: string; jwks_uri: string; userinfo_endpoint: string };
  redirectUri?: string;
}

export interface ReportUsage {
  id: number;
  kind: string;
  slug: string;
  title: string;
  department: string;
  runs: number;
  users: number;
}

export interface MonthlyReport {
  month: string;
  org_name: string;
  report: {
    users: number;
    active_users: number;
    app_runs: number;
    app_users: number;
    skill_runs: number;
    tool_calls: number;
    tool_errors: number;
    kb_searches: number;
    kb_hits: number;
    model_calls: number;
    tokens: number;
    apps: ReportUsage[];
    capabilities: ReportUsage[];
    questions: { query: string; count: number; users: number; misses: number }[];
    departments: { department: string; users: number; active_users: number; app_runs: number; tokens: number }[];
    days: { date: string; active_users: number; app_runs: number }[];
  };
  previous: { users: number; active_users: number; app_runs: number; tool_calls: number; kb_searches: number; tokens: number };
  params: ReportParams;
  estimate: { app_hours: number; tool_hours: number; kb_hours: number; hours: number; cost: number };
  satisfaction: number | null;
  generated_at: string;
}

export interface ReportParams {
  minutesPerAppRun: number;
  minutesPerToolCall: number;
  minutesPerKbHit: number;
  hourlyCost: number;
}

export interface LdapTestResult {
  ok: boolean;
  steps: { step: "connect" | "search" | "verify"; ok: boolean; error?: string }[];
  entry: { dn: string; username: string; display_name: string; department: string; email: string; groups: string[]; disabled: boolean } | null;
  roles: RoleRef[];
  username: string;
  /** local：与已有本地账号重名，不能用目录登录；invalid：用户名不符合规则。 */
  conflict: "" | "local" | "invalid";
}

// ---- 能力中心 ----

export type AssetKind = "skill" | "prompt" | "app" | "connection";
export type Delivery = "auto" | "optional";

export interface Scope {
  all: boolean;
  departments: string[];
  roles: number[];
  users: number[];
}

/** 资产版本的内容，按 kind 取用其中的字段（与 server/internal/api/hub.go 的 assetSpec 对应）。 */
// 微应用在 DSH 右侧栏里的业务工作台，结构见 docs/workbench-panels.md；服务端负责完整校验
export interface WorkbenchAction {
  label: string;
  prompt: string;
  /** 表单提交到应用自带的表：直接插入一行，不经过 AI */
  table?: string;
}

export interface WorkbenchWidget {
  type: "metric" | "table" | "chart" | "checklist" | "form" | "actions" | "markdown" | "heading" | "board" | "kb_scope";
  title?: string;
  text?: string;
  /** 写 table 是应用自带的表，和 connection / tool 二选一 */
  source?: { connection?: string; tool?: string; table?: string; dataset?: string; args?: Record<string, unknown> };
  field?: string;
  format?: string;
  delta_field?: string;
  rows?: string;
  columns?: { field: string; title: string; format?: string }[];
  row_actions?: WorkbenchAction[];
  detail?: { field: string; title: string; format?: string }[];
  chart?: "line" | "bar" | "pie";
  x?: string;
  y?: { field: string; title: string; format?: string }[];
  items?: { id: string; text: string }[];
  item_field?: string;
  id_field?: string;
  empty?: { text: string; action?: WorkbenchAction };
  fields?: { name: string; label: string; kind: string; required?: boolean; options?: string[] }[];
  submit?: WorkbenchAction;
  actions?: WorkbenchAction[];
}

export interface WorkbenchPane {
  id: string;
  type: "panel" | "web" | "kb_doc";
  title: string;
  url?: string;
  widgets?: WorkbenchWidget[];
}

export interface AppWorkbench {
  panes: WorkbenchPane[];
}

/** 应用自带的数据表（M13）。own：各自填写；shared：共同维护；author：作者发布。 */
export interface AppTable {
  name: string;
  title: string;
  visibility?: "own" | "shared" | "author";
  fields: AppField[];
}

export interface AppField {
  name: string;
  title: string;
  kind: "text" | "number" | "money" | "date" | "select" | "bool" | "user";
  options?: string[];
  required?: boolean;
  hidden?: boolean;
}

/** GET /api/apps/{slug}/data 里的一张表。rows 只有作者和管理员能看到。 */
export interface AppTableInfo extends AppTable {
  can_insert: boolean;
  rows?: number;
}

/** 数据中台（M15）：上传生成的表。 */
export interface DataTable {
  name: string;
  title: string;
  description: string;
  columns: { name: string; type: "number" | "date" | "text" }[];
  row_count: number;
  updated_at: string;
}

export interface DataParam {
  name: string;
  title: string;
  default?: string;
  auto?: boolean;
}

/** 数据中台的数据集：带名字、可带参数的只读查询。 */
export interface DataSet {
  id: number;
  slug: string;
  title: string;
  description: string;
  sql: string;
  params: DataParam[];
  scope: Scope;
  updated_at: string;
}

export interface DataResult {
  columns: string[];
  items: Record<string, unknown>[];
  total: number;
  truncated?: boolean;
}

export interface DataOverview {
  enabled: boolean;
  error: string;
  database?: string;
  read_error?: string;
  tables: DataTable[];
  datasets: DataSet[];
  auto_params: Record<string, string>;
}

/** 网页版应用（M14）：GET /api/me/apps/{slug}/web。 */
export interface WebApp {
  slug: string;
  asset_id: number;
  title: string;
  description: string;
  icon?: string;
  starters: string[];
  highlights?: string[] | null;
  workbench?: AppWorkbench | null;
  tables: boolean;
  web: boolean;
  admin: boolean;
}

/** 表里的一行：字段值加上系统字段。 */
export type AppRow = Record<string, unknown> & { id: number; owner: string; mine: boolean; created_at: string; updated_at: string };

export interface AssetSpec {
  title: string;
  description: string;
  when_to_use?: string;
  body?: string;
  model_invocable?: boolean;
  persona?: string;
  starters?: string[];
  icon?: string;
  launch?: "new" | "resume";
  workbench?: AppWorkbench;
  /** 微应用：自带的数据表 */
  tables?: AppTable[];
  /** 微应用：开放网页版（/a/<标识>） */
  web?: boolean;
  /** 微应用：「它能帮你做什么」的要点，最多 3 条 */
  highlights?: string[];
  /** 微应用：样板，出现在「造 → 从样板复制」里（管理员设）。 */
  template?: boolean;
  knowledge?: string[];
  skills?: string[];
  connections?: string[];
  plugins?: string[];
  type?: "mcp" | "openapi";
  url?: string;
  auth?: { type: "none" | "bearer" | "header"; header?: string };
  /** 连接：员工身份，和共享凭据并存 */
  user_auth?: UserAuth;
  openapi?: string;
  instructions?: string;
  timeout_seconds?: number;
  /** 连接：由扩展清单装出来的（声明式扩展）。地址、凭据、工具都由清单决定 */
  extension?: ConnExtension;
}

/** 连接上的扩展信息。清单原文和管理员填的值只有管理连接的人拿得到。 */
export interface ConnExtension {
  id?: string;
  version?: string;
  digest?: string;
  manifest?: string;
  values?: Record<string, unknown>;
}

/** 清单里给人看的文字：一个字符串，或分语言写。 */
export type ExtText = string | { zh?: string; en?: string };

export interface ExtDiagnostic {
  code: string;
  severity: "error" | "warning" | "info";
  where: string;
  message: string;
  fix?: string;
}

/** 安装时要人填的一个参数（清单里 fill 是 admin 或 app 的）。 */
export interface ExtFormParam {
  key: string;
  fill: "admin" | "app";
  label: ExtText;
  description?: ExtText;
  type?: string;
  choices?: unknown[];
  default?: unknown;
  required: boolean;
  tools: string[];
}

export interface ExtToolView {
  name: string;
  description: string;
  method: string;
  path: string;
  read_only: boolean;
  fixed?: { name: string; key: string; fill: "admin" | "app"; required?: boolean }[];
  keep?: string[];
}

/** 检查一个扩展包的结果：安装前给管理员看的全部内容。 */
export interface ExtensionInspect {
  check: {
    ok: boolean;
    identity?: string;
    kind?: string;
    digest?: string;
    servers?: string[];
    credential?: { mode: "shared" | "user_secret" | "ee_jwt" | "oauth"; header?: string; audience?: string; claims?: string[]; sharedHeader?: string };
    errors: number;
    warnings: number;
    results: ExtDiagnostic[];
  };
  manifest?: { id: string; version: string; name: ExtText; description?: ExtText; repo?: string; kind: string; minVersion?: string };
  installable?: boolean;
  /** 不能装的原因（检查通过了但这个版本装不了） */
  blocked?: string;
  tools?: ExtToolView[];
  params?: ExtFormParam[];
  /** 安装时原样交回去的清单和接口文档 */
  package?: { manifest: string; openapi: string };
  slug?: string;
  /** 清单里写了、但这个版本安装时不处理的部分 */
  ignored?: ("config" | "apps")[];
  /** 升级时：和线上版本的差别 */
  change?: { from: string; new_writes?: string[]; new_reads?: string[]; removed?: string[]; credential?: string[]; new_servers?: string[] };
  /** 升级时：现在填的参数值 */
  values?: Record<string, unknown>;
}

export interface HubAsset {
  id: number;
  kind: AssetKind;
  slug: string;
  title: string;
  description: string;
  category: string;
  owner_id: number | null;
  scope: Scope;
  delivery: Delivery;
  status: "active" | "unpublished";
  secret_hint?: string;
  created_at: string;
  updated_at: string;
  live_version: number;
  pending: number;
  spec: AssetSpec;
  owner: string;
  owner_name: string;
  department: string;
  available: boolean;
  enabled: boolean;
  subscribed: boolean;
  uses: number;
  users: number;
  tools: number;
  tools_total: number;
  can_manage: boolean;
  /** 申请扩大到的范围，审核通过前不生效。 */
  pending_scope: Scope | null;
  scope_requested_at: string | null;
  /** 上一次范围申请被驳回的原因。 */
  scope_note: string;
}

export interface AssetVersion {
  asset_id: number;
  version: number;
  spec: AssetSpec;
  status: PluginStatus;
  review_note: string;
  created_by: number | null;
  reviewed_by: number | null;
  reviewed_at: string | null;
  created_at: string;
  publisher: string;
  reviewer: string;
}

export interface ConnectionTool {
  asset_id: number;
  name: string;
  description: string;
  input_schema: unknown;
  read_only: boolean;
  enabled: boolean;
  roles: number[] | null;
  updated_at: string;
}

export interface ConnectionCall {
  tool: string;
  username: string;
  ok: boolean;
  error: string;
  latency_ms: number;
  /** 这次调用带的员工身份：没有（只有共享凭据）/ ee_jwt / user_secret */
  auth_mode?: "ee_jwt" | "user_secret";
  /** 连接是扩展装出来的：当时的「身份@版本」和清单哈希 */
  extension?: string;
  digest?: string;
  created_at: string;
}

/** 连接上的员工身份设置：ee_jwt 由 dsh-ee 每次现签，user_secret 由员工自己填。 */
export interface UserAuth {
  mode: "ee_jwt" | "user_secret";
  header?: string;
  audience?: string;
  claims?: string[];
  hint?: string;
}

/** 员工在一个连接上看到的自己的凭据状态。 */
export interface MyCredential {
  mode: "ee_jwt" | "user_secret";
  hint?: string;
  filled: boolean;
  tail?: string;
  updated_at?: string;
}

export interface CredentialHolder {
  user_id: number;
  username: string;
  display_name: string;
  department: string;
  updated_at: string;
}

export interface EeJwtInfo {
  issuer: string;
  jwks_url: string;
  lifetime_seconds: number;
  claims: string[];
  rotate_delay_seconds: number;
  keys: { kid: string; created_at: string; active_at: string; state: "active" | "pending" | "retired" }[];
}

/** 应用依赖的一项与当前员工的状态（M7）。 */
export interface AppDep {
  kind: "skill" | "connection" | "knowledge" | "plugin";
  slug: string;
  title: string;
  id?: number;
  status: "ok" | "off" | "missing" | "partial" | "gone";
  tools?: number;
  tools_total?: number;
  request?: AccessRequest;
}

/** 一条权限申请：连接、技能或知识库。 */
export interface AccessRequest {
  id: number;
  kind: "connection" | "skill" | "knowledge";
  user_id: number;
  username: string;
  user_name: string;
  department: string;
  asset_id: number;
  slug: string;
  title: string;
  app_id: number;
  app_slug: string;
  app_title: string;
  reason: string;
  status: "pending" | "approved" | "rejected";
  note: string;
  handled_at: string | null;
  created_at: string;
}

export interface AccessTarget {
  kind: AccessRequest["kind"];
  slug: string;
  title: string;
  description: string;
  allowed: boolean;
  id?: number;
  request?: AccessRequest;
  app?: { slug: string; title: string };
}

/** 用到某项原料的应用（M5）。 */
export interface UsedByApp {
  id: number;
  slug: string;
  title: string;
  icon?: string;
}

/** 导入应用文件的报告：每项依赖是新建、沿用还是缺少。 */
export interface ImportReport {
  asset: { id: number; slug: string };
  report: { kind: string; slug: string; title?: string; result: "created" | "reused" | "missing" | "skipped"; note?: string }[];
  removed_widgets: number;
}

export interface AssetDetail {
  asset: HubAsset;
  deps?: AppDep[];
  used_by?: UsedByApp[];
  versions?: AssetVersion[];
  tools?: ConnectionTool[];
  calls?: ConnectionCall[];
  server_name?: string;
  my_credential?: MyCredential;
  feedback?: { up: number; down: number; up_30d: number };
  questions?: { id: number; signal: string; question: string; count: number; last_at: string }[];
}

export interface PluginPolicy {
  name: string;
  scope: Scope;
  delivery: Delivery;
}

export interface AssetReviewItem {
  asset: HubAsset;
  version: number;
  spec: AssetSpec;
  previous: AssetSpec | null;
  previous_version: number;
  publisher: string;
  publisher_name: string;
  department: string;
  created_at: string;
}

export interface ScopeReviewItem {
  asset: HubAsset;
  spec: AssetSpec;
  scope: Scope;
  requested: Scope;
  publisher: string;
  publisher_name: string;
  department: string;
  created_at: string | null;
}

export interface AssetCreate {
  kind: AssetKind;
  slug: string;
  category?: string;
  spec: AssetSpec;
  scope?: Scope;
  delivery?: Delivery;
  secret?: string;
}

export interface ConnTestResult {
  ok: boolean;
  error?: string;
  tools?: ConnectionTool[];
  instructions?: string;
  base_url?: string;
  latency_ms?: number;
}

export interface ConnectInfo {
  package: string;
  version: string;
  ready: boolean;
  enabled: boolean;
  token_env: string;
  profile: string;
  server_name: string;
  tarball_url?: string;
  error?: string;
}

export interface ConnectState {
  package: string;
  version: string;
  hash: string;
  base_url: string;
  published_at: string;
  error?: string;
}

export interface SetupStep {
  id: string;
  title: string;
  done: boolean;
  optional?: boolean;
  detail?: string;
  how?: string;
}

/** 示例内容里的一项：created 这次新建 / exists 已经有了 / missing 还没有 / failed 没建成。 */
export interface DemoItem {
  kind: "kb" | "connection" | "skill" | "prompt" | "app" | "user";
  slug: string;
  title: string;
  result: "created" | "exists" | "missing" | "failed";
  error?: string;
}

export interface DemoGuideStep {
  username: string;
  display_name: string;
  department: string;
  title: string;
  app: string;
  app_title: string;
  try: string;
}

export interface DemoStatus {
  items: DemoItem[];
  imported: boolean;
  /** 还有没有可清除的示例数据 */
  present: boolean;
  guide: DemoGuideStep[];
}

export interface DemoImport {
  items: DemoItem[];
  accounts: { username: string; display_name: string; department: string; title: string; password?: string }[];
  usage_rows: number;
  guide: DemoGuideStep[];
}

export interface SetupStatus {
  steps: SetupStep[];
  required_done: boolean;
  next: string;
  install_command?: string;
  onboarding?: string;
}

export interface ImportResult {
  packages: number;
  added: number;
  existing: number;
  pending: number;
  skipped: string[];
}

const kinds: PluginKind[] = ["skill", "connector", "knowledge", "plugin"];

/** 接口里的能力 → 页面组件用的 Plugin。 */
export function toPlugin(c: CatalogPlugin): Plugin {
  const t = c.published_at ?? c.updated_at;
  return {
    name: c.name,
    title: c.title || c.name,
    description: c.description,
    author: c.origin === "internal" ? c.owner_name || c.owner || c.author : c.author,
    icon: c.icon || null,
    homepage: c.homepage || null,
    repository: c.repository || null,
    category: c.category,
    kind: kinds.includes(c.kind as PluginKind) ? (c.kind as PluginKind) : undefined,
    featured: c.featured,
    stars: c.stars ?? 0,
    score: c.score,
    version: c.version,
    publishedAt: t ? Math.floor(new Date(t).getTime() / 1000) : 0,
    origin: c.origin,
    installs: c.installs,
    depCount: c.dep_count ?? 0,
    sizeKb: Math.round((c.closure_size ?? c.size) / 1024),
    offlineReady: c.offline_ready ?? !c.has_install_script,
    hasInstallScript: c.has_install_script,
    status: c.status,
    department: c.department || undefined,
    owner: c.owner || undefined,
    pending: c.pending,
  };
}

const encName = (name: string) => encodeURIComponent(name);

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

/** 会话失效（401）时通知 AuthProvider 回到登录页。 */
let onUnauthorized: (() => void) | null = null;
export function setUnauthorizedHandler(fn: (() => void) | null) {
  onUnauthorized = fn;
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  opts: { silent401?: boolean; raw?: { body: Blob | ArrayBuffer | FormData; type?: string } } = {},
): Promise<T> {
  let res: Response;
  try {
    res = await fetch(path, {
      method,
      credentials: "same-origin",
      headers: opts.raw ? (opts.raw.type ? { "Content-Type": opts.raw.type } : undefined) : body === undefined ? undefined : { "Content-Type": "application/json" },
      body: opts.raw ? opts.raw.body : body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    throw new ApiError(0, i18n.t("api.unreachable"));
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: unknown = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    // 代理到不了后端时 Vite 返回的是纯文本
  }
  if (!res.ok) {
    if (res.status === 401 && !opts.silent401) onUnauthorized?.();
    const msg =
      (data as { error?: string } | null)?.error ??
      (res.status >= 500 ? i18n.t("api.serverError") : i18n.t("api.requestFailed", { status: res.status }));
    throw new ApiError(res.status, msg);
  }
  return data as T;
}

/** 知识库：谁能查（可用范围）与谁来维护。 */
export interface KBMaintainers {
  users: number[];
  roles: number[];
}

export interface KBase {
  id: number;
  slug: string;
  title: string;
  description: string;
  scope: Scope;
  maintainers: KBMaintainers;
  revision: string;
  created_by: number | null;
  created_at: string;
  updated_at: string;
  docs: number;
  can_edit: boolean;
  can_manage: boolean;
  readable: boolean;
  /** 列表里才有：最近更新的几篇 */
  recent?: { id: number; title: string; updated_at: string }[];
  /** 维护者才有：待补文档有几条 */
  gaps?: number;
}

/** 待补文档：员工（经 AI）想查却没查到的检索词。 */
export interface KBGap {
  key: string;
  query: string;
  count: number;
  users: number;
  last_at: string;
}

export interface KBQASettings {
  qaEnabled: boolean;
  qaTitle: string;
  qaIcon: string;
  qaStarters: string[];
  qaExtra: string;
}

export interface KDoc {
  id: number;
  kb_id: number;
  title: string;
  folder: string;
  source_name: string;
  mime: string;
  size: number;
  version: number;
  lines: number;
  updated_by: number | null;
  updated_by_name?: string;
  created_at: string;
  updated_at: string;
  has_original: boolean;
  /** 转换体检；网页新建的文档没有 */
  convert_info?: KDocConvert;
}

/** 转换体检：跟员工能不能查到有关的事（服务端 kb.Info）。 */
export interface KDocConvert {
  format: "md" | "txt" | "docx" | "pdf" | "xlsx" | "csv" | "pptx" | string;
  pages?: number;
  skipped?: { page: number; reason: "scanned" | "garbled" | string }[];
  header_footer?: number;
  numbering?: number;
  tables?: number;
  sheets?: number;
  hidden_sheets?: number;
  rows?: number;
  header?: "first" | "letters" | "mixed";
  encoding?: string;
  notes?: number;
  /** 维护者选的表头设置：first / none，空表示自动判断 */
  header_option?: "" | "first" | "none";
  /** 正文在网页上改过 */
  edited?: boolean;
}

export interface KDocVersion {
  doc_id: number;
  version: number;
  note: string;
  created_by: number | null;
  created_by_name: string;
  created_at: string;
}

export interface KDocDetail {
  doc: KDoc;
  kb: KBase;
  version: number;
  markdown: string;
  outline: { level: number; title: string; line: number }[] | null;
  versions: KDocVersion[];
  /** 文档格式，阅读视图按它显示分页线、工作表、每页卡片 */
  format: string;
  /** 维护者才有：近 30 天被 AI 读过几次 */
  reads_30d?: number;
}

export interface KBHit {
  kb_id: number;
  doc: number;
  title: string;
  folder?: string;
  version: number;
  heading: string;
  from: number;
  to: number;
  line: number;
  snippet: string;
  matched: string[];
  score: number;
}

export interface KBSearchResult {
  hits: KBHit[];
  term_hits: Record<string, number>;
  total: number;
  truncated?: boolean;
  /** 标题对得上的文档 */
  docs?: { kb_id: number; doc: number; title: string; folder?: string; version: number; lines: number }[];
  note?: string;
}

export interface KBQuery {
  id: number;
  user_id: number;
  user: string;
  tool: "search" | "read" | "browse";
  query: string;
  kb_ids: number[];
  docs: number[];
  hits: number;
  created_at: string;
}

export interface KBUploadResult {
  file: string;
  doc?: KDoc;
  error?: string;
  /** 同目录下有同名文档，更新成了它的新版本 */
  updated?: boolean;
}

export const api = {
  /** 演示站：一键以示例员工的身份登录 */
  demoLogin: (username: string) => request<Me>("POST", "/api/auth/demo-login", { username }),
  login: (username: string, password: string) =>
    request<User>("POST", "/api/auth/login", { username, password }, { silent401: true }),
  logout: () => request<void>("POST", "/api/auth/logout"),
  me: () => request<Me>("GET", "/api/auth/me", undefined, { silent401: true }),
  myMenus: () => request<{ menus: Menu[] }>("GET", "/api/me/menus").then((r) => r.menus),
  changePassword: (old_password: string, new_password: string) =>
    request<void>("POST", "/api/auth/password", { old_password, new_password }),
  createDeviceCode: (input: { client_name: string; client_os: string; client_version: string }) =>
    request<DeviceCodeResponse>("POST", "/api/device/code", input),
  pollDeviceToken: (device_code: string) => request<{ access_token: string; scopes: string[]; user: { username: string; department: string }; expires_at: string }>("POST", "/api/device/token", { device_code }),
  deviceInfo: (code: string) => request<DeviceInfo>("GET", `/api/device/${encodeURIComponent(code)}`),
  approveDevice: (code: string, approve: boolean) => request<{ status: string }>("POST", `/api/device/${encodeURIComponent(code)}`, { approve }),

  myKeys: () => request<{ keys: ApiKey[]; quota: MyQuota }>("GET", "/api/me/keys"),
  createMyKey: (k: { name: string; expires_at?: string }) =>
    request<{ key: string; api_key: ApiKey }>("POST", "/api/me/keys", k),
  revokeMyKey: (id: number) => request<void>("DELETE", `/api/me/keys/${id}`),
  myTokens: () => request<{ tokens: TokenView[] }>("GET", "/api/me/tokens").then((r) => r.tokens),
  createMyToken: (input: TokenInput) => request<{ token: string; info: TokenView }>("POST", "/api/me/tokens", input),
  revokeMyToken: (id: string) => request<void>("DELETE", `/api/me/tokens/${id}`),
  elevateToken: (id: string, password: string, hours: 1 | 8) => request<{ admin_until: string }>("POST", `/api/me/tokens/${id}/elevate`, { password, hours }),
  endElevation: (id: string) => request<void>("DELETE", `/api/me/tokens/${id}/elevate`),

  users: () => request<{ users: User[] }>("GET", "/api/admin/users").then((r) => r.users),
  /** role_ids 不传时给默认的「员工」角色；auth_source 为 ldap 时不设密码。 */
  createUser: (u: {
    username: string;
    password?: string;
    display_name: string;
    department: string;
    role_ids?: number[];
    auth_source?: "local" | "ldap" | "oidc";
    account_type?: "human" | "bot";
    owner_id?: number;
  }) => request<User>("POST", "/api/admin/users", u),
  updateUser: (id: number, patch: Partial<{ display_name: string; department: string; disabled: boolean; password: string; unbind_identity: boolean }>) =>
    request<User>("PATCH", `/api/admin/users/${id}`, patch),
  setUserRoles: (id: number, role_ids: number[]) =>
    request<{ roles: RoleRef[] }>("PUT", `/api/admin/users/${id}/roles`, { role_ids }).then((r) => r.roles),

  permissions: () => request<{ permissions: Permission[] }>("GET", "/api/admin/permissions").then((r) => r.permissions),
  createPermission: (p: { code: string; name: string; group: string; description: string }) =>
    request<Permission>("POST", "/api/admin/permissions", p),
  deletePermission: (code: string) => request<void>("DELETE", `/api/admin/permissions/${encodeURIComponent(code)}`),
  roles: () => request<{ roles: Role[] }>("GET", "/api/admin/roles").then((r) => r.roles),
  createRole: (r: { code: string; name: string; description: string; permissions: string[] }) =>
    request<Role>("POST", "/api/admin/roles", r),
  updateRole: (id: number, patch: Partial<{ name: string; description: string; permissions: string[] }>) =>
    request<Role>("PATCH", `/api/admin/roles/${id}`, patch),
  deleteRole: (id: number) => request<void>("DELETE", `/api/admin/roles/${id}`),

  menus: () => request<{ menus: Menu[] }>("GET", "/api/admin/menus").then((r) => r.menus),
  createMenu: (m: MenuInput) => request<Menu>("POST", "/api/admin/menus", m),
  updateMenu: (id: number, m: MenuInput) => request<Menu>("PATCH", `/api/admin/menus/${id}`, m),
  deleteMenu: (id: number) => request<void>("DELETE", `/api/admin/menus/${id}`),
  reorderMenus: (items: { id: number; parent_id: number | null; sort: number }[]) =>
    request<void>("PUT", "/api/admin/menus/order", { items }),
  resetMenus: () => request<{ menus: Menu[] }>("POST", "/api/admin/menus/reset").then((r) => r.menus),
  /** 整份替换额度：显式 null 表示不限额 / 全部模型 / 永不过期。 */
  updateUserQuota: (
    id: number,
    patch: { monthly_token_limit?: number | null; allowed_models?: string[] | null; quota_expires_at?: string | null },
  ) => request<User>("PATCH", `/api/admin/users/${id}/quota`, patch),

  providers: () => request<{ providers: Provider[] }>("GET", "/api/admin/providers").then((r) => r.providers),
  createProvider: (p: { name: string; vendor: string; base_url: string; api_key: string; priority: number; models?: string[] }) =>
    request<Provider>("POST", "/api/admin/providers", p),
  updateProvider: (
    id: number,
    patch: Partial<{ vendor: string; base_url: string; api_key: string; priority: number; enabled: boolean }>,
  ) => request<Provider>("PATCH", `/api/admin/providers/${id}`, patch),
  deleteProvider: (id: number) => request<void>("DELETE", `/api/admin/providers/${id}`),
  checkProvider: (id: number) => request<CheckResult>("POST", `/api/admin/providers/${id}/check`),

  models: () => request<{ models: Model[] }>("GET", "/api/admin/models").then((r) => r.models),
  createModels: (provider_id: number, items: { upstream_model: string; name?: string; display_name?: string }[]) =>
    request<{ created: Model[]; skipped: string[] }>("POST", "/api/admin/models", { provider_id, items }),
  updateModel: (id: number, patch: Partial<{ name: string; upstream_model: string; display_name: string; enabled: boolean }>) =>
    request<Model>("PATCH", `/api/admin/models/${id}`, patch),
  deleteModel: (id: number) => request<void>("DELETE", `/api/admin/models/${id}`),

  keys: () => request<{ keys: ApiKey[] }>("GET", "/api/admin/keys").then((r) => r.keys),
  issueKey: (k: { user_id: number; name: string; expires_at?: string }) =>
    request<{ key: string; api_key: ApiKey }>("POST", "/api/admin/keys", k),
  revokeKey: (id: number) => request<void>("DELETE", `/api/admin/keys/${id}`),
  adminTokens: (query = "") => request<{ tokens: TokenView[] }>(`GET`, `/api/admin/tokens${query}`).then((r) => r.tokens),
  issueToken: (input: TokenInput) => request<{ token: string; info: TokenView }>("POST", "/api/admin/tokens", input),
  revokeTokens: (input: { ids?: string[]; user_id?: number; client_name?: string; department?: string }) =>
    request<{ revoked: number }>("POST", "/api/admin/tokens/revoke", input),

  authMethods: () => request<{ ldap: boolean; oidc: { name: string } | null; demo: DemoSite | null }>("GET", "/api/auth/methods", undefined, { silent401: true }),
  oidc: () => request<OidcView>("GET", "/api/admin/auth/oidc"),
  saveOidc: (c: OidcInput) => request<OidcView>("PUT", "/api/admin/auth/oidc", c),
  testOidc: (c: OidcInput) => request<OidcTestResult>("POST", "/api/admin/auth/oidc/test", c),
  monthlyReport: (month: string) =>
    request<MonthlyReport>("GET", `/api/admin/report/monthly?month=${encodeURIComponent(month)}&tz=${-new Date().getTimezoneOffset()}`),
  saveReportParams: (p: ReportParams) => request<ReportParams>("PUT", "/api/admin/report/params", p),
  ldap: () => request<LdapView>("GET", "/api/admin/auth/ldap"),
  saveLdap: (c: LdapInput) => request<LdapView>("PUT", "/api/admin/auth/ldap", c),
  testLdap: (c: LdapInput & { testUsername?: string; testPassword?: string }) =>
    request<LdapTestResult>("POST", "/api/admin/auth/ldap/test", c),
  syncLdap: () => request<LdapSyncResult>("POST", "/api/admin/auth/ldap/sync"),

  /** tz 为本地时区相对 UTC 的分钟数（东八区 480），按本地日期分天。 */
  stats: (days: number, groupBy?: "token") => request<Stats>("GET", `/api/admin/stats?days=${days}&tz=${-new Date().getTimezoneOffset()}${groupBy ? `&group_by=${groupBy}` : ""}`),
  myOverview: () => request<MyOverview>("GET", "/api/me/overview"),
  audit: (q: AuditQuery = {}) => {
    const params = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== "") params.set(k, String(v));
    return request<{ logs: AuditLog[] }>("GET", `/api/admin/audit?${params}`).then((r) => r.logs);
  },

  knowledge: () => request<{ items: KBase[]; can_manage: boolean; formats: string[]; qa: KBQASettings }>("GET", "/api/kb"),
  createKB: (b: { slug: string; title: string; description: string; scope?: Scope; maintainers?: KBMaintainers }) => request<KBase>("POST", "/api/kb", b),
  kb: (id: number) => request<{ kb: KBase; docs: KDoc[]; used_by?: UsedByApp[] }>("GET", `/api/kb/${id}`),
  patchKB: (id: number, p: Partial<{ title: string; description: string; scope: Scope; maintainers: KBMaintainers }>) => request<KBase>("PATCH", `/api/kb/${id}`, p),
  deleteKB: (id: number) => request<void>("DELETE", `/api/kb/${id}`),
  saveKBSettings: (s: KBQASettings) => request<KBQASettings>("PUT", "/api/kb/settings", s),
  /** asNew：同名时另存为新文档的文件名；其它同名文件更新成新版本 */
  uploadKBDocs: (id: number, files: File[], folder: string, opts: { asNew?: string[]; header?: string } = {}) => {
    const form = new FormData();
    form.set("folder", folder);
    if (opts.header) form.set("header", opts.header);
    for (const n of opts.asNew ?? []) form.append("new", n);
    for (const f of files) form.append("file", f, f.name);
    return request<{ results: KBUploadResult[] }>("POST", `/api/kb/${id}/docs`, undefined, { raw: { body: form } });
  },
  kbGaps: (id: number) =>
    request<{ gaps: KBGap[]; popular: { doc: number; title: string; reads: number; users: number }[]; days: number }>("GET", `/api/kb/${id}/gaps`),
  ignoreKBGap: (id: number, key: string) => request<void>("POST", `/api/kb/${id}/gaps/ignore`, { key }),
  checkKBUpload: (id: number, folder: string, files: string[]) =>
    request<{ items: { file: string; doc: KDoc }[] }>("POST", `/api/kb/${id}/docs/check`, { folder, files }),
  reconvertKBDoc: (id: number, header: "" | "first" | "none") => request<KDoc>("POST", `/api/kb-docs/${id}/reconvert`, { header }),
  createKBDoc: (id: number, title: string, markdown: string, folder: string) => {
    const form = new FormData();
    form.set("folder", folder);
    form.set("title", title);
    form.set("markdown", markdown);
    return request<{ results: KBUploadResult[] }>("POST", `/api/kb/${id}/docs`, undefined, { raw: { body: form } });
  },
  kbDoc: (id: number, version?: number) => request<KDocDetail>("GET", `/api/kb-docs/${id}${version ? `?version=${version}` : ""}`),
  patchKBDoc: (id: number, p: Partial<{ title: string; folder: string; markdown: string; note: string }>) => request<KDoc>("PATCH", `/api/kb-docs/${id}`, p),
  replaceKBDoc: (id: number, file: File) => {
    const form = new FormData();
    form.set("file", file, file.name);
    return request<KDoc>("POST", `/api/kb-docs/${id}/file`, undefined, { raw: { body: form } });
  },
  deleteKBDoc: (id: number) => request<void>("DELETE", `/api/kb-docs/${id}`),
  kbOriginalURL: (id: number) => `/api/kb-docs/${id}/original`,
  searchKB: (q: { query: string; mode?: string; kb_ids?: number[]; folder?: string }) => request<KBSearchResult>("POST", "/api/kb/search", q),
  kbQueries: (q: { kb?: number; all?: boolean; tool?: string } = {}) => {
    const params = new URLSearchParams();
    if (q.kb) params.set("kb", String(q.kb));
    if (q.all) params.set("all", "1");
    if (q.tool) params.set("tool", q.tool);
    return request<{ items: KBQuery[] }>("GET", `/api/kb/queries?${params}`).then((r) => r.items);
  },

  hub: () => request<{ assets: HubAsset[]; plugin_policies: Record<string, PluginPolicy> }>("GET", "/api/hub"),
  hubAsset: (id: number) => request<AssetDetail>("GET", `/api/hub/${id}`),
  appTables: (slug: string) => request<{ tables: AppTableInfo[]; admin: boolean; row_limit: number }>("GET", `/api/apps/${encodeURIComponent(slug)}/data`),
  appRows: (slug: string, table: string, params: { q?: string; limit?: number; offset?: number } = {}) => {
    const q = new URLSearchParams();
    if (params.q) q.set("q", params.q);
    if (params.limit) q.set("limit", String(params.limit));
    if (params.offset) q.set("offset", String(params.offset));
    return request<{ total: number; items: AppRow[] }>("GET", `/api/apps/${encodeURIComponent(slug)}/data/${encodeURIComponent(table)}?${q}`);
  },
  appInsertRow: (slug: string, table: string, row: Record<string, unknown>) =>
    request<{ items: AppRow[] }>("POST", `/api/apps/${encodeURIComponent(slug)}/data/${encodeURIComponent(table)}`, { row }),
  appUpdateRow: (slug: string, table: string, id: number, row: Record<string, unknown>) =>
    request<{ item: AppRow }>("PATCH", `/api/apps/${encodeURIComponent(slug)}/data/${encodeURIComponent(table)}/${id}`, { row }),
  appDeleteRow: (slug: string, table: string, id: number) =>
    request<void>("DELETE", `/api/apps/${encodeURIComponent(slug)}/data/${encodeURIComponent(table)}/${id}`),
  dataOverview: () => request<DataOverview>("GET", "/api/admin/data"),
  dataUpload: (f: { file: File; name: string; title: string; description?: string; mode: "create" | "replace" | "append" }) => {
    const form = new FormData();
    form.set("file", f.file);
    form.set("name", f.name);
    form.set("title", f.title);
    form.set("description", f.description ?? "");
    form.set("mode", f.mode);
    return request<{ table: DataTable; added: number }>("POST", "/api/admin/data/tables", undefined, { raw: { body: form } });
  },
  dataTablePreview: (name: string) => request<{ table: DataTable; preview: DataResult }>("GET", `/api/admin/data/tables/${encodeURIComponent(name)}`),
  dataTableDelete: (name: string) => request<void>("DELETE", `/api/admin/data/tables/${encodeURIComponent(name)}`),
  dataRun: (sql: string, values: Record<string, unknown>, params: DataParam[]) =>
    request<{ result: DataResult; params: DataParam[] }>("POST", "/api/admin/data/run", { sql, values, params }),
  dataSetSave: (d: { slug: string; title: string; description: string; sql: string; params: DataParam[]; scope: Scope }, update: boolean) =>
    update
      ? request<DataSet>("PUT", `/api/admin/data/datasets/${encodeURIComponent(d.slug)}`, d)
      : request<DataSet>("POST", "/api/admin/data/datasets", d),
  dataSetDelete: (slug: string) => request<void>("DELETE", `/api/admin/data/datasets/${encodeURIComponent(slug)}`),
  myDataSets: () => request<{ items: { slug: string; title: string; description: string; params: DataParam[] }[] }>("GET", "/api/data/datasets"),
  webApp: (slug: string) => request<WebApp>("GET", `/api/me/apps/${encodeURIComponent(slug)}/web`),
  webAppSource: (slug: string, pane: string, widget: number) =>
    request<{ value?: unknown; forbidden?: boolean; error?: string }>("POST", `/api/me/apps/${encodeURIComponent(slug)}/source`, { pane, widget }),
  appExportUrl: (slug: string, table: string) => `/api/apps/${encodeURIComponent(slug)}/data/${encodeURIComponent(table)}/export`,
  inspectExtension: (body: { zip?: string; manifest?: string; openapi?: string; id?: number }) => request<ExtensionInspect>("POST", "/api/admin/extensions/inspect", body),
  createAsset: (a: AssetCreate) =>
    request<{ asset: HubAsset; status: PluginStatus; tools?: number; sync_error?: string }>("POST", "/api/hub", a),
  newAssetVersion: (id: number, spec: AssetSpec, secret?: string) =>
    request<{ version: number; status: PluginStatus; tools?: number; sync_error?: string }>("POST", `/api/hub/${id}/versions`, {
      spec,
      ...(secret !== undefined ? { secret } : {}),
    }),
  patchAsset: (id: number, p: Partial<{ category: string; scope: Scope; delivery: Delivery; status: "active" | "unpublished"; note: string }>) =>
    request<HubAsset>("PATCH", `/api/hub/${id}`, p),
  deleteAsset: (id: number) => request<void>("DELETE", `/api/hub/${id}`),
  workbenchPreview: (app: string, source: unknown) =>
    request<{ value?: unknown; forbidden?: boolean; error?: string; note?: string }>("POST", "/api/me/workbench-preview", { app, source }),
  appDraft: (from: "knowledge" | "connection", slug: string) =>
    request<{ slug: string; spec: AssetSpec; notes: string[] }>("POST", "/api/me/app-drafts", { from, slug }),
  appFileUrl: (id: number, docs: boolean) => `/api/hub/${id}/export${docs ? "?docs=1" : ""}`,
  importApp: (file: unknown, slug?: string) => request<ImportReport>("POST", "/api/hub/import", { file, ...(slug ? { slug } : {}) }),
  accessTarget: (kind: string, slug: string, app?: string) =>
    request<AccessTarget>("GET", `/api/me/access-target?${new URLSearchParams({ kind, slug, ...(app ? { app } : {}) })}`),
  myAccessRequests: () => request<{ mine: AccessRequest[]; to_review: AccessRequest[] }>("GET", "/api/me/access-requests"),
  requestAccess: (r: { kind: string; slug: string; app?: string; reason?: string }) =>
    request<{ id: number; status: string; already_pending: boolean; title: string }>("POST", "/api/me/access-requests", r),
  resolveAccess: (id: number, decision: "approve" | "reject", note = "") =>
    request<{ status: string }>("POST", `/api/me/access-requests/${id}`, { decision, note }),
  subscribe: (id: number) => request<void>("POST", `/api/hub/${id}/subscription`),
  unsubscribe: (id: number) => request<void>("DELETE", `/api/hub/${id}/subscription`),
  hubReviewQueue: () => request<{ items: AssetReviewItem[] }>("GET", "/api/admin/hub/review").then((r) => r.items),
  hubReview: () => request<{ items: AssetReviewItem[]; scopes: ScopeReviewItem[] }>("GET", "/api/admin/hub/review"),
  reviewScope: (r: { asset_id: number; action: "approve" | "reject"; note?: string }) => request<void>("POST", "/api/admin/hub/scope-review", { note: "", ...r }),
  reviewAsset: (r: { asset_id: number; version: number; action: "approve" | "reject"; note?: string }) =>
    request<void>("POST", "/api/admin/hub/review", { note: "", ...r }),
  setPluginPolicy: (name: string, p: { scope: Scope; delivery: Delivery }) =>
    request<PluginPolicy>("PUT", `/api/admin/plugin-policies/${encName(name)}`, p),
  testConnection: (spec: AssetSpec, secret?: string, id?: number) =>
    request<ConnTestResult>("POST", "/api/admin/connections/test", { spec, ...(secret !== undefined ? { secret } : {}), ...(id ? { id } : {}) }),
  syncConnection: (id: number) => request<{ ok: boolean; tools?: number; error?: string }>("POST", `/api/admin/connections/${id}/sync`),
  setConnectionTool: (id: number, name: string, p: { enabled: boolean; roles: number[] }) =>
    request<void>("PATCH", `/api/admin/connections/${id}/tools/${encodeURIComponent(name)}`, p),
  setMyCredential: (id: number, secret: string) => request<void>("PUT", `/api/me/connections/${id}/credential`, { secret }),
  deleteMyCredential: (id: number) => request<void>("DELETE", `/api/me/connections/${id}/credential`),
  connCredentials: (id: number) => request<{ holders: CredentialHolder[] }>("GET", `/api/admin/connections/${id}/credentials`).then((r) => r.holders),
  revokeConnCredential: (id: number, userId: number) => request<void>("DELETE", `/api/admin/connections/${id}/credentials/${userId}`),
  eeJwtInfo: () => request<EeJwtInfo>("GET", "/api/admin/ee-jwt"),
  rotateEeJwt: () => request<EeJwtInfo>("POST", "/api/admin/ee-jwt/rotate"),
  connectInfo: () => request<ConnectInfo>("GET", "/api/connect"),
  adminConnect: () => request<{ state: ConnectState; package?: string; stale?: boolean; patch?: string }>("GET", "/api/admin/connect"),
  publishConnect: (force = false) => request<ConnectState>("POST", "/api/admin/connect/publish", { force }),
  setupStatus: () => request<SetupStatus>("GET", "/api/admin/setup-status"),
  demoStatus: () => request<DemoStatus>("GET", "/api/admin/demo"),
  importDemo: () => request<DemoImport>("POST", "/api/admin/demo"),
  clearDemo: () => request<{ assets: number; kbases: number; users: number }>("DELETE", "/api/admin/demo"),
  welcome: () => request<{ show: boolean; dismissed: boolean; connected: boolean }>("GET", "/api/me/welcome"),
  putWelcome: (dismissed: boolean) => request<{ dismissed: boolean }>("PUT", "/api/me/welcome", { dismissed }),
  getStarted: (admin: boolean) => request<GetStarted>("POST", "/api/me/get-started", { admin }),
  getStartedStatus: (code: string) => request<GetStartedStatus>("GET", `/api/me/get-started/${encodeURIComponent(code)}`),
  dshReleases: () => request<{ enabled: boolean; releases: DshRelease[] }>("GET", "/api/dsh/releases"),
  adminDshReleases: () => request<{ releases: DshRelease[] }>("GET", "/api/admin/dsh-releases").then((r) => r.releases),
  checkDshReleases: () => request<{ ok: boolean }>("POST", "/api/admin/dsh-releases/check"),
  mirrorDshRelease: (id: number) => request<{ ok: boolean }>("POST", `/api/admin/dsh-releases/${id}/mirror`),
  recommendDshRelease: (id: number) => request<{ ok: boolean }>("POST", `/api/admin/dsh-releases/${id}/recommend`),
  deleteDshRelease: (id: number) => request<void>("DELETE", `/api/admin/dsh-releases/${id}`),
  dshClients: () => request<{ clients: DshClient[] }>("GET", "/api/me/dsh-clients").then((r) => r.clients),
  uploadBrandLogo: (file: File) => {
    const form = new FormData();
    form.append("file", file);
    return request<{ logo_rev: string }>("PUT", "/api/admin/brand/logo", undefined, { raw: { body: form } });
  },

  /** 系统设置：未登录时返回 401（静默），调用方保留默认值。 */
  settings: () => request<Settings>("GET", "/api/settings", undefined, { silent401: true }),
  saveSettings: (s: Settings) => request<Settings>("PUT", "/api/admin/settings", s),

  catalog: () => request<{ plugins: CatalogPlugin[] }>("GET", "/api/catalog").then((r) => r.plugins),
  catalogDetail: (name: string) => request<PackageDetail>("GET", `/api/catalog/${encName(name)}`),
  myPackages: () => request<{ packages: MyPackage[] }>("GET", "/api/me/packages").then((r) => r.packages),
  /** 发布前预检：请求体就是 npm pack 生成的 .tgz，只检查不发布。 */
  checkPackage: (file: Blob) =>
    request<PackageCheck>("POST", "/api/me/packages/check", undefined, {
      raw: { body: file, type: "application/octet-stream" },
    }),
  /** 网页发布：请求体就是 npm pack 生成的 .tgz。 */
  uploadPackage: (file: Blob, tag?: string) =>
    request<{ name: string; version: string; status: PluginStatus; created: boolean }>(
      "POST",
      `/api/me/packages${tag ? `?tag=${encodeURIComponent(tag)}` : ""}`,
      undefined,
      { raw: { body: file, type: "application/octet-stream" } },
    ),
  requestImport: (r: { name: string; version?: string; reason: string }) => request<SyncJob>("POST", "/api/me/import-requests", r),

  myNpmTokens: () => request<{ tokens: NpmToken[] }>("GET", "/api/me/npm-tokens").then((r) => r.tokens),
  createNpmToken: (t: { name: string; access: "read" | "publish" }) =>
    request<{ token: string; info: NpmToken }>("POST", "/api/me/npm-tokens", t),
  revokeMyNpmToken: (id: number) => request<void>("DELETE", `/api/me/npm-tokens/${id}`),
  allNpmTokens: () => request<{ tokens: NpmToken[] }>("GET", "/api/admin/npm-tokens").then((r) => r.tokens),
  revokeNpmToken: (id: number) => request<void>("DELETE", `/api/admin/npm-tokens/${id}`),

  adminPackages: () => request<{ plugins: CatalogPlugin[] }>("GET", "/api/admin/packages").then((r) => r.plugins),
  adminPackage: (name: string) => request<PackageDetail>("GET", `/api/admin/packages/${encName(name)}`),
  patchPackage: (name: string, p: Partial<{ title: string; description: string; category: string; kind: string; icon: string; featured: boolean }>) =>
    request<PackageDetail>("PATCH", `/api/admin/packages/${encName(name)}`, p),
  reviewQueue: () => request<{ items: ReviewItem[] }>("GET", "/api/admin/review").then((r) => r.items),
  review: (r: { name: string; version?: string; action: ReviewAction; note?: string }) =>
    request<{ changed: number }>("POST", "/api/admin/review", { version: "", note: "", ...r }),
  tarballUrl: (name: string, version: string) => `/api/admin/tarball?name=${encName(name)}&version=${encodeURIComponent(version)}`,

  registryOverview: () => request<RegistryOverview>("GET", "/api/admin/registry"),
  checkRegistry: () =>
    request<{ ok: boolean; latency_ms?: number; status?: number; message?: string; upstream?: string }>("POST", "/api/admin/registry/check"),

  upstreamFeed: (refresh = false) =>
    request<{ plugins: FeedPlugin[]; fetched_at?: string; error?: string; disabled?: boolean }>(
      "GET",
      `/api/admin/upstream/feed${refresh ? "?refresh=1" : ""}`,
    ),
  syncUpstream: (names: string[], publish: boolean) => request<SyncJob>("POST", "/api/admin/upstream/sync", { names, publish }),
  jobs: () => request<{ jobs: SyncJob[] }>("GET", "/api/admin/upstream/jobs").then((r) => r.jobs),
  job: (id: number) => request<SyncJob>("GET", `/api/admin/upstream/jobs/${id}`),
  checkUpdates: () => request<{ job: SyncJob | null }>("POST", "/api/admin/upstream/check-updates").then((r) => r.job),
  exportUrl: (since?: string) => `/api/admin/upstream/export${since ? `?since=${encodeURIComponent(since)}` : ""}`,
  importBundle: (file: Blob) =>
    request<{ result?: ImportResult; error?: string; job: SyncJob }>("POST", "/api/admin/upstream/import", undefined, {
      raw: { body: file, type: "application/x-tar" },
    }),
};

export function formatBytes(n: number) {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GB`;
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1024) return `${Math.round(n / 1024)} KB`;
  return `${n} B`;
}

export function keyState(k: ApiKey): "active" | "revoked" | "expired" {
  if (k.revoked_at) return "revoked";
  if (k.expires_at && new Date(k.expires_at) <= new Date()) return "expired";
  return "active";
}

export function formatTokens(n: number) {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n % 1_000_000 === 0 ? 0 : 1)}M`;
  if (n >= 10_000) return `${(n / 1000).toFixed(0)}K`;
  return n.toLocaleString();
}

export function formatTime(iso: string | null) {
  if (!iso) return "—";
  const d = new Date(iso);
  const pad = (x: number) => String(x).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
