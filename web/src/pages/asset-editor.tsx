import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Navigate, useNavigate, useParams, useSearchParams } from "react-router";
import { useTranslation } from "react-i18next";
import { AppWindow, ArrowLeft, Upload, X } from "lucide-react";
import { api, type AssetKind, type AssetSpec, type ConnTestResult, type Delivery, type Scope } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useStore } from "../lib/store";
import { useAction, useApi } from "../lib/use-api";
import { allScope, assetPath, KIND_ICONS, selfScope } from "../lib/hub";
import { ErrorNote, LoadState } from "../components/api-ui";
import { ScopeEditor } from "../components/scope-editor";
import { useScopeOptions } from "../components/scope-dialog";
import { Combobox } from "../components/pickers";
import { Badge, Button, Card, cx, Field, inputBase, inputCls, PageHeader, PasswordInput, Segmented, ToggleRow, Notice, backLinkCls } from "../components/ui";
import { PageLink } from "../lib/menus";
import { parseWorkbench, WorkbenchField, workbenchText } from "../components/workbench-editor";
import { parseTables, TablesField, tablesText as tablesToText } from "../components/app-data";
import { LivePreview } from "../components/live-preview";
import { AppAgentGuide } from "../components/app-agent-guide";
import { UserAuthFields } from "../components/connection-identity";

const KINDS: AssetKind[] = ["skill", "prompt", "app", "connection"];
const textareaCls = "w-full rounded-md border border-neutral-300 bg-card px-3 py-2 text-sm outline-none placeholder:text-neutral-500 focus:border-brand-500 focus:ring-2 focus:ring-brand-100";

function emptySpec(kind: AssetKind): AssetSpec {
  switch (kind) {
    case "skill":
      return { title: "", description: "", when_to_use: "", body: "", model_invocable: true };
    case "prompt":
      return { title: "", description: "", body: "" };
    case "app":
      return { title: "", description: "", persona: "", starters: [], icon: "", launch: "new", skills: [], connections: [], plugins: [] };
    case "connection":
      return { title: "", description: "", type: "mcp", url: "", auth: { type: "none" }, instructions: "", openapi: "", timeout_seconds: 30 };
  }
}

/** 已选项以小标签列出，下拉里挑新的。 */
function MultiPick({ label, values, options, onChange }: { label: string; values: string[]; options: { value: string; label: string; hint?: string }[]; onChange: (v: string[]) => void }) {
  const { t } = useTranslation();
  const name = (v: string) => options.find((o) => o.value === v)?.label ?? v;
  return (
    <Field label={label}>
      <div className="flex flex-wrap items-center gap-1.5">
        {values.map((v) => (
          <span key={v} className="inline-flex items-center gap-1 rounded bg-neutral-100 px-2 py-0.5 text-sm text-neutral-800">
            {name(v)}
            <button type="button" aria-label={t("scopeEditor.remove", { name: name(v) })} onClick={() => onChange(values.filter((x) => x !== v))} className="text-neutral-500 hover:text-neutral-900">
              <X size={12} />
            </button>
          </span>
        ))}
        <Combobox
          className="w-64"
          value=""
          aria-label={label}
          placeholder={t("editor.pick")}
          options={options.filter((o) => !values.includes(o.value))}
          onChange={(v) => v && onChange([...values, v])}
        />
      </div>
    </Field>
  );
}

function Section({ title, children }: { title?: string; children: ReactNode }) {
  return (
    <Card className="space-y-5 p-6">
      {title && <h2 className="text-neutral-900 text-[15px] leading-6 font-semibold">{title}</h2>}
      {children}
    </Card>
  );
}

/** 连接的配置：方式、地址、接口文档、认证与凭据、说明。 */
function ConnectionFields({ spec, set, secret, setSecret, secretHint, slug }: { spec: AssetSpec; set: (p: Partial<AssetSpec>) => void; secret: string; setSecret: (v: string) => void; secretHint?: string; slug: string }) {
  const { t } = useTranslation();
  const file = useRef<HTMLInputElement>(null);
  const auth = spec.auth ?? { type: "none" };
  return (
    <>
      <Field label={t("connections.type")}>
        <Segmented
          value={spec.type ?? "mcp"}
          onChange={(v) => set({ type: v })}
          options={[
            { value: "mcp", label: t("connections.typeMcp") },
            { value: "openapi", label: t("connections.typeOpenapi") },
          ]}
        />
      </Field>
      {spec.type === "openapi" ? (
        <>
          <Field label={t("connections.openapi")} hint={t("connections.writeOff")}>
            <textarea className={cx(textareaCls, "h-48 font-mono text-xs")} value={spec.openapi ?? ""} onChange={(e) => set({ openapi: e.target.value })} placeholder='openapi: 3.0.0' />
          </Field>
          <div>
            <input
              ref={file}
              type="file"
              accept=".json,.yaml,.yml"
              className="hidden"
              onChange={async (e) => {
                const f = e.target.files?.[0];
                if (f) set({ openapi: await f.text() });
                e.target.value = "";
              }}
            />
            <Button type="button" size="sm" variant="secondary" onClick={() => file.current?.click()}>
              <Upload size={14} /> {t("connections.openapiUpload")}
            </Button>
          </div>
          <Field label={t("connections.apiBase")} hint={t("connections.apiBaseHint")}>
            <input className={cx(inputCls, "font-mono")} value={spec.url ?? ""} onChange={(e) => set({ url: e.target.value })} placeholder="https://oa.acme.internal/api" />
          </Field>
        </>
      ) : (
        <Field label={t("connections.url")} hint={t("connections.urlHint")}>
          <input className={cx(inputCls, "font-mono")} value={spec.url ?? ""} onChange={(e) => set({ url: e.target.value })} placeholder="https://crm.acme.internal/mcp" />
        </Field>
      )}
      <Field label={t("connections.auth")}>
        <Segmented
          value={auth.type}
          onChange={(v) => set({ auth: { type: v, header: v === "header" ? auth.header || "X-API-Key" : undefined } })}
          options={[
            { value: "none", label: t("connections.authNone") },
            { value: "bearer", label: t("connections.authBearer") },
            { value: "header", label: t("connections.authHeader") },
          ]}
        />
      </Field>
      {auth.type !== "none" && (
        <div className="grid gap-4 sm:grid-cols-2">
          {auth.type === "header" && (
            <Field label={t("connections.authHeaderName")}>
              <input className={cx(inputCls, "font-mono")} value={auth.header ?? ""} onChange={(e) => set({ auth: { type: "header", header: e.target.value } })} />
            </Field>
          )}
          <Field label={t("connections.secret")} hint={t("connections.secretHint")}>
            <PasswordInput
              label={t("connections.secret")}
              className={inputCls}
              autoComplete="new-password"
              value={secret}
              onChange={(e) => setSecret(e.target.value)}
              placeholder={secretHint ? t("connections.secretSet", { hint: secretHint }) : ""}
            />
          </Field>
        </div>
      )}
      <UserAuthFields spec={spec} set={set} slug={slug} />
      <Field label={t("connections.instructions")} hint={t("connections.instructionsHint")}>
        <textarea className={cx(textareaCls, "h-20")} value={spec.instructions ?? ""} onChange={(e) => set({ instructions: e.target.value })} />
      </Field>
      <Field label={t("connections.timeout")}>
        <input className={cx(inputBase, "w-24")} type="number" min={1} max={120} value={spec.timeout_seconds ?? 30} onChange={(e) => set({ timeout_seconds: Number(e.target.value) })} />
      </Field>
    </>
  );
}

export default function AssetEditorRoute({ forceKind, tabs }: { forceKind?: AssetKind; tabs?: ReactNode }) {
  const { id } = useParams();
  const [params] = useSearchParams();
  return <AssetEditor key={`${id ?? "new"}:${forceKind ?? params.get("kind") ?? ""}:${params.get("from") ?? ""}:${params.get("knowledge") ?? ""}:${params.get("connection") ?? ""}`} forceKind={forceKind} tabs={tabs} />;
}

type NewMode = "agent" | "manual";

/**
 * 「造 → 新建应用」：默认是「让 AI 帮我做」，给一段发到 DSH 的提示词；「手动配置」是固定为应用的编辑器。
 * ?from=<id> 从样板复制，?knowledge=<库>、?connection=<连接> 先生成一份草稿，这几种直接进手动配置；?mode= 记住所在页签。
 */
export function AppNew() {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();
  const seeded = params.has("from") || params.has("knowledge") || params.has("connection");
  const mode: NewMode = params.get("mode") === "manual" || params.get("mode") === "agent" ? (params.get("mode") as NewMode) : seeded ? "manual" : "agent";
  const setMode = (m: NewMode) =>
    setParams(
      (p) => {
        const n = new URLSearchParams(p);
        n.set("mode", m);
        return n;
      },
      { replace: true },
    );
  const tabs = (
    <div className="mb-5">
      <Segmented value={mode} onChange={setMode} options={[{ value: "agent", label: t("appAgent.tabAgent") }, { value: "manual", label: t("appAgent.tabManual") }]} />
    </div>
  );
  // 两个页签都挂着，来回切换不丢已经填的内容
  return (
    <>
    <div hidden={mode !== "manual"}>
      <AssetEditorRoute forceKind="app" tabs={tabs} />
    </div>
    <div hidden={mode !== "agent"} className="max-w-[1400px]">
      <PageLink page="build" className={backLinkCls}>
        <ArrowLeft size={14} /> {t("asset.back")}
      </PageLink>
      <PageHeader title={t("editor.newApp")} desc={t("hub.tabHints.app")} actions={<AppWindow size={28} strokeWidth={1.4} className="text-neutral-400" />} />
      {tabs}
      <AppAgentGuide onManual={() => setMode("manual")} />
    </div>
    </>
  );
}

function AssetEditor({ forceKind, tabs }: { forceKind?: AssetKind; tabs?: ReactNode }) {
  const { t } = useTranslation();
  const { id } = useParams();
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const { can, user } = useAuth();
  const { plugins, notify } = useStore();
  const editing = id !== undefined;
  const fromId = !editing ? Number(params.get("from")) || 0 : 0;
  const kbSlug = params.get("knowledge");
  const connSlug = params.get("connection");
  const existing = useApi(() => (editing ? api.hubAsset(Number(id)) : Promise.resolve(null)));
  const source = useApi(() => (fromId ? api.hubAsset(fromId) : Promise.resolve(null)));
  // 原料一键生成草稿（M5）：失败时退回到只带上知识库
  const draft = useApi(() =>
    !editing && !fromId && (kbSlug || connSlug)
      ? api.appDraft(kbSlug ? "knowledge" : "connection", (kbSlug || connSlug)!).catch(() => null)
      : Promise.resolve(null),
  );
  const hub = useApi(api.hub);
  const kb = useApi(api.knowledge);
  const opts = useScopeOptions();
  const queryKind = (forceKind ?? (fromId || kbSlug || connSlug ? "app" : params.get("kind"))) as AssetKind;
  const kind: AssetKind = existing.data?.asset.kind ?? (KINDS.includes(queryKind) ? queryKind : "skill");
  const canManage = kind === "connection" ? can("connections.manage") : can("plugins.manage");
  const [spec, setSpec] = useState<AssetSpec>(() => emptySpec(kind));
  // 工作台在编辑框里是 JSON 文字，提交时再解析
  const [wbText, setWbText] = useState("");
  const [tablesText, setTablesText] = useState("");
  const [slug, setSlug] = useState("");
  // 员工新建默认「自己用」：保存就能在自己的 DSH 里打开，想分享再改范围
  const [scope, setScope] = useState<Scope>(() => (!canManage && user && kind !== "connection" ? selfScope(user.id) : allScope()));
  const [delivery, setDelivery] = useState<Delivery>("optional");
  const [secret, setSecret] = useState("");
  const [preview, setPreview] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [test, setTest] = useState<ConnTestResult | null>(null);
  const { busy, run } = useAction();

  // 新建时按类型给空白内容；编辑时用线上（或最新提交的）版本填好；从样板复制时用样板的内容
  useEffect(() => {
    if (existing.data) {
      const a = existing.data.asset;
      setSpec({ ...emptySpec(a.kind), ...a.spec });
      setWbText(workbenchText(a.spec.workbench));
      setTablesText(tablesToText(a.spec.tables));
      setSlug(a.slug);
    } else if (source.data) {
      const a = source.data.asset;
      setSpec({ ...emptySpec("app"), ...a.spec, title: t("selfScope.copyOf", { title: a.spec.title }).slice(0, 64), template: false });
      setWbText(workbenchText(a.spec.workbench));
      setTablesText(tablesToText(a.spec.tables));
      setSlug(`${a.slug}-copy`.slice(0, 48));
    } else if (draft.data) {
      setSpec({ ...emptySpec("app"), ...draft.data.spec });
      setWbText(workbenchText(draft.data.spec.workbench));
      setTablesText("");
      setSlug(draft.data.slug);
    } else if (!editing && !fromId) {
      setSpec({ ...emptySpec(kind), ...(kind === "app" && kbSlug ? { knowledge: [kbSlug] } : {}) });
      setWbText("");
      setTablesText("");
    }
  }, [existing.data, source.data, draft.data, editing, fromId, kind, kbSlug, t]);

  const set = (p: Partial<AssetSpec>) => setSpec((s) => ({ ...s, ...p }));
  const assets = hub.data?.assets ?? [];
  const skillOptions = useMemo(() => assets.filter((a) => a.kind === "skill").map((a) => ({ value: a.slug, label: a.spec.title, hint: a.slug })), [assets]);
  const connOptions = useMemo(() => assets.filter((a) => a.kind === "connection").map((a) => ({ value: a.slug, label: a.spec.title, hint: a.slug })), [assets]);
  const pluginOptions = useMemo(() => plugins.map((p) => ({ value: p.name, label: p.title, hint: p.name })), [plugins]);
  const kbOptions = useMemo(() => (kb.data?.items ?? []).filter((b) => b.readable).map((b) => ({ value: b.slug, label: b.title, hint: b.slug })), [kb.data]);

  // 扩展装出来的连接不在这里改：地址、凭据、工具由清单决定，设置和升级走扩展页面
  if (editing && existing.data?.asset.spec.extension?.id) return <Navigate to={`/extensions/install?id=${id}`} replace />;
  if (editing && !existing.data) {
    return <LoadState loading={existing.loading} error={existing.error} onRetry={existing.reload}>{null}</LoadState>;
  }
  if (fromId && !source.data) {
    return <LoadState loading={source.loading} error={source.error} onRetry={source.reload}>{null}</LoadState>;
  }
  if ((kbSlug || connSlug) && !editing && !fromId && draft.loading) {
    return <LoadState loading error={null} onRetry={draft.reload}>{null}</LoadState>;
  }
  const Icon = KIND_ICONS[kind];
  const title = editing
    ? kind === "connection"
      ? t("editor.editConnection", { title: existing.data!.asset.spec.title })
      : t("editor.newVersion", { title: existing.data!.asset.spec.title })
    : t(({ skill: "editor.newSkill", prompt: "editor.newPrompt", app: "editor.newApp", connection: "editor.newConnection" } as const)[kind]);

  // 只提交这一类用得到的字段，后端会拒绝其它字段混进来
  const cleanSpec = (): AssetSpec => {
    const base = { title: spec.title, description: spec.description };
    switch (kind) {
      case "skill":
        return { ...base, when_to_use: spec.when_to_use, body: spec.body, model_invocable: spec.model_invocable };
      case "prompt":
        return { ...base, body: spec.body };
      case "app":
        return {
          ...base,
          persona: spec.persona,
          starters: (spec.starters ?? []).filter(Boolean),
          highlights: (spec.highlights ?? []).map((x) => x.trim()).filter(Boolean),
          icon: spec.icon,
          launch: spec.launch,
          workbench: parseWorkbench(wbText).value,
          tables: parseTables(tablesText).value,
          web: !!spec.web,
          skills: spec.skills,
          connections: spec.connections,
          plugins: spec.plugins,
          knowledge: spec.knowledge,
          ...(can("plugins.manage") ? { template: !!spec.template } : {}),
        };
      case "connection":
        return { ...base, type: spec.type, url: spec.url, auth: spec.auth, user_auth: spec.user_auth, instructions: spec.instructions, timeout_seconds: spec.timeout_seconds, ...(spec.type === "openapi" ? { openapi: spec.openapi } : {}) };
    }
  };

  const submit = async () => {
    setError(null);
    if (kind === "app" && parseWorkbench(wbText).error) return setError(t("appWorkbench.badJson", { error: parseWorkbench(wbText).error }));
    const s = cleanSpec();
    const r = await run(async () => {
      if (editing) {
        const res = await api.newAssetVersion(Number(id), s, kind === "connection" && secret ? secret : undefined);
        return { id: Number(id), status: res.status, tools: res.tools, sync_error: res.sync_error };
      }
      const res = await api.createAsset({ kind, slug: slug.trim(), spec: s, scope, delivery, ...(kind === "connection" && secret ? { secret } : {}) });
      return { id: res.asset.id, status: res.status, tools: res.tools, sync_error: res.sync_error };
    });
    if (!r.ok) return setError(r.error);
    if (kind === "connection") {
      notify(r.value.sync_error ? t("connections.savedSyncFail", { error: r.value.sync_error }) : t("connections.saved", { title: s.title, count: r.value.tools ?? 0 }));
    } else {
      notify(r.value.status === "pending" ? t("editor.submittedPending") : t("editor.submittedPublished"));
    }
    navigate(assetPath(r.value.id));
  };

  const runTest = async () => {
    setTest(null);
    const r = await run(() => api.testConnection(cleanSpec(), secret || undefined, editing ? Number(id) : undefined));
    if (r.ok) setTest(r.value);
    else setError(r.error);
  };

  const bodyField = (hint: string) => (
    <Field label={t("editor.body")} hint={hint}>
      <div className="mb-2">
        <Segmented
          value={preview ? "preview" : "edit"}
          onChange={(v) => setPreview(v === "preview")}
          options={[
            { value: "edit", label: t("editor.edit") },
            { value: "preview", label: t("editor.preview") },
          ]}
        />
      </div>
      {preview ? (
        <pre className="min-h-64 rounded-md bg-neutral-50 p-4 text-sm whitespace-pre-wrap text-neutral-800">{spec.body}</pre>
      ) : (
        <textarea className={cx(textareaCls, "h-64 font-mono")} value={spec.body ?? ""} onChange={(e) => set({ body: e.target.value })} />
      )}
    </Field>
  );

  return (
    <div className={kind === "app" ? "max-w-[1400px]" : "max-w-3xl"}>
      <PageLink page={kind === "app" ? "build" : "materials"} className={backLinkCls}>
        <ArrowLeft size={14} /> {t("asset.back")}
      </PageLink>
      <PageHeader title={title} desc={t(`hub.tabHints.${kind}`)} actions={<Icon size={28} strokeWidth={1.4} className="text-neutral-400" />} />
      {!editing && tabs}
      {(draft.data?.notes.length ?? 0) > 0 && (
        <Notice tone="warn" className="mb-4">
          <div className="font-medium">{t("studio.draftNotes")}</div>
          <ul className="mt-1 list-disc pl-5">{draft.data!.notes.map((n) => <li key={n}>{n}</li>)}</ul>
        </Notice>
      )}

      <div className={kind === "app" ? "grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,440px)]" : ""}>
      <div className="space-y-4">
        <Section>
          <Field label={t("editor.slug")} hint={kind === "connection" ? t("editor.connSlugHint") : t("editor.slugHint")}>
            <input
              className={cx(inputCls, "font-mono")}
              value={slug}
              disabled={editing}
              onChange={(e) => setSlug(e.target.value.toLowerCase())}
              placeholder={kind === "connection" ? "crm" : "expense-policy"}
            />
          </Field>
          <Field label={t("editor.title")}>
            <input className={inputCls} value={spec.title} onChange={(e) => set({ title: e.target.value })} />
          </Field>
          <Field label={t("editor.description")} hint={kind === "skill" ? t("editor.skillDescriptionHint") : t("editor.descriptionHint")}>
            <textarea className={cx(textareaCls, "h-20")} value={spec.description} onChange={(e) => set({ description: e.target.value })} />
          </Field>
        </Section>

        {kind === "skill" && (
          <Section>
            <Field label={t("editor.whenToUse")} hint={t("editor.whenToUseHint")}>
              <input className={inputCls} value={spec.when_to_use ?? ""} onChange={(e) => set({ when_to_use: e.target.value })} />
            </Field>
            <ToggleRow label={t("editor.modelInvocable")} desc={t("editor.modelInvocableDesc")} checked={spec.model_invocable !== false} onChange={(v) => set({ model_invocable: v })} />
            {bodyField(t("editor.skillBodyHint"))}
          </Section>
        )}
        {kind === "prompt" && <Section>{bodyField(t("editor.promptBodyHint"))}</Section>}
        {kind === "app" && (
          <Section>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={t("editor.icon")} hint={t("editor.iconHint")}>
                <input className={inputCls} value={spec.icon ?? ""} onChange={(e) => set({ icon: e.target.value })} placeholder="🧾" />
              </Field>
              <Field label={t("editor.launch")}>
                <Segmented
                  value={spec.launch ?? "new"}
                  onChange={(v) => set({ launch: v })}
                  options={[
                    { value: "new", label: t("editor.launchNew") },
                    { value: "resume", label: t("editor.launchResume") },
                  ]}
                />
              </Field>
            </div>
            <Field label={t("editor.persona")} hint={t("editor.personaHint")}>
              <textarea className={cx(textareaCls, "h-48")} value={spec.persona ?? ""} onChange={(e) => set({ persona: e.target.value })} />
            </Field>
            <Field label={t("editor.starters")}>
              <textarea className={cx(textareaCls, "h-24")} value={(spec.starters ?? []).join("\n")} onChange={(e) => set({ starters: e.target.value.split("\n") })} />
            </Field>
            <Field label={t("editor.highlights")} hint={t("editor.highlightsHint")}>
              <textarea className={cx(textareaCls, "h-20")} value={(spec.highlights ?? []).join("\n")} onChange={(e) => set({ highlights: e.target.value.split("\n").slice(0, 3) })} />
            </Field>
            <MultiPick label={t("editor.skills")} values={spec.skills ?? []} options={skillOptions} onChange={(v) => set({ skills: v })} />
            <MultiPick label={t("editor.knowledge")} values={spec.knowledge ?? []} options={kbOptions} onChange={(v) => set({ knowledge: v })} />
            <MultiPick label={t("editor.connections")} values={spec.connections ?? []} options={connOptions} onChange={(v) => set({ connections: v })} />
            {/* 插件需要写代码，只有已经用到插件的应用或管理员才看到这一项 */}
            {(can("plugins.manage") || (spec.plugins?.length ?? 0) > 0) && (
              <MultiPick label={t("editor.plugins")} values={spec.plugins ?? []} options={pluginOptions} onChange={(v) => set({ plugins: v })} />
            )}
            {can("plugins.manage") && (
              <ToggleRow label={t("selfScope.template")} desc={t("selfScope.templateDesc")} checked={!!spec.template} onChange={(v) => set({ template: v })} />
            )}
            <TablesField text={tablesText} onText={setTablesText} />
            <ToggleRow label={t("appWeb.toggle")} desc={t("appWeb.toggleDesc")} checked={!!spec.web} onChange={(v) => set({ web: v })} />
            <WorkbenchField
              text={wbText}
              onText={setWbText}
              connections={spec.connections ?? []}
              connectionIds={(spec.connections ?? []).flatMap((slug) => assets.filter((a) => a.kind === "connection" && a.slug === slug).map((a) => ({ slug, id: a.id })))}
            />
          </Section>
        )}
        {kind === "connection" && (
          <Section>
            <ConnectionFields spec={spec} set={set} secret={secret} setSecret={setSecret} secretHint={existing.data?.asset.secret_hint} slug={slug} />
            <div className="flex flex-wrap items-center gap-3">
              <Button type="button" variant="secondary" disabled={busy} onClick={runTest}>
                {busy ? t("connections.testing") : t("connections.test")}
              </Button>
              {test && (
                <span className={cx("text-sm", test.ok ? "text-emerald-700" : "text-red-700")}>
                  {test.ok ? t("connections.testOk", { count: test.tools?.length ?? 0, ms: test.latency_ms }) : t("connections.testFail", { error: test.error })}
                </span>
              )}
            </div>
            {test?.ok && (test.tools?.length ?? 0) > 0 && (
              <ul className="max-h-64 space-y-1 overflow-auto rounded-md border border-neutral-100 p-3 text-sm">
                {test.tools!.map((tool) => (
                  <li key={tool.name} className="flex items-center gap-2">
                    <span className="font-mono text-neutral-900">{tool.name}</span>
                    <Badge tone={tool.read_only ? "slate" : "amber"}>{tool.read_only ? t("asset.toolReadOnly") : t("asset.toolWrite")}</Badge>
                    <span className="truncate text-neutral-600">{tool.description}</span>
                  </li>
                ))}
              </ul>
            )}
          </Section>
        )}

        {!editing && (
          <Section>
            <Field label={t("editor.scope")}>
              <ScopeEditor
                value={scope}
                onChange={setScope}
                roles={opts.roles}
                users={opts.users}
                departments={opts.departments}
                selfId={kind !== "connection" ? user?.id : undefined}
              />
            </Field>
            <Field
              label={t("editor.delivery")}
              hint={canManage ? (delivery === "auto" ? t("editor.deliveryAutoHint") : t("editor.deliveryOptionalHint")) : t("editor.deliveryAutoNeedsAdmin")}
            >
              <div className={canManage ? "" : "pointer-events-none opacity-50"}>
                <Segmented
                  value={delivery}
                  onChange={setDelivery}
                  options={[
                    { value: "optional", label: t("asset.deliveryOptional") },
                    { value: "auto", label: t("asset.deliveryAuto") },
                  ]}
                />
              </div>
            </Field>
          </Section>
        )}

        <ErrorNote>{error}</ErrorNote>
        <div className="flex gap-2">
          <Button disabled={busy || !spec.title.trim() || (!editing && !slug.trim())} onClick={submit}>
            {t("editor.submit")}
          </Button>
          <Button variant="ghost" onClick={() => navigate(-1)}>
            {t("common.cancel")}
          </Button>
        </div>
      </div>
      {kind === "app" && (
        <aside className="lg:sticky lg:top-4">
          <LivePreview text={wbText} app={editing ? slug : ""} title={spec.title} icon={spec.icon} />
        </aside>
      )}
      </div>
    </div>
  );
}
