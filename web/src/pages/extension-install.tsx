import { useEffect, useRef, useState, type ReactNode } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { useTranslation } from "react-i18next";
import { ArrowLeft, FileArchive, Upload } from "lucide-react";
import { api, type AssetSpec, type Delivery, type ExtDiagnostic, type ExtensionInspect, type ExtFormParam, type ExtText, type Scope } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useStore } from "../lib/store";
import { useAction, useApi } from "../lib/use-api";
import { allScope, assetPath } from "../lib/hub";
import { ErrorNote, LoadState } from "../components/api-ui";
import { ScopeEditor } from "../components/scope-editor";
import { useScopeOptions } from "../components/scope-dialog";
import { Badge, Button, Card, cx, Field, inputCls, Notice, PageHeader, PasswordInput, Segmented, Spinner, backLinkCls } from "../components/ui";
import { PageLink } from "../lib/menus";

const MANIFEST = "dsh-ee.extension.json";

/** 清单里给人看的文字：按界面语言取，没有就取另一种。 */
export function extText(x: ExtText | undefined, lang: string): string {
  if (!x) return "";
  if (typeof x === "string") return x;
  return (lang.startsWith("zh") ? x.zh || x.en : x.en || x.zh) ?? "";
}

async function toBase64(f: File): Promise<string> {
  const bytes = new Uint8Array(await f.arrayBuffer());
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(bin);
}

/** 把参数的值写成输入框里的文字。 */
function valueText(v: unknown): string {
  if (v === undefined || v === null) return "";
  return typeof v === "string" ? v : JSON.stringify(v);
}

/** 把输入框里的文字按参数类型变回值；填得不对返回 undefined。 */
function parseValue(p: ExtFormParam, text: string): { ok: true; value: unknown } | { ok: false } {
  if (p.choices?.length) {
    const hit = p.choices.find((c) => valueText(c) === text);
    return hit === undefined ? { ok: false } : { ok: true, value: hit };
  }
  switch (p.type) {
    case "number":
    case "integer": {
      const n = Number(text);
      return text.trim() === "" || !Number.isFinite(n) || (p.type === "integer" && !Number.isInteger(n)) ? { ok: false } : { ok: true, value: n };
    }
    case "boolean":
      return text === "true" || text === "false" ? { ok: true, value: text === "true" } : { ok: false };
    case "array":
    case "object":
      try {
        return { ok: true, value: JSON.parse(text) };
      } catch {
        return { ok: false };
      }
    default:
      return { ok: true, value: text };
  }
}

function Section({ title, aside, children }: { title: string; aside?: ReactNode; children: ReactNode }) {
  return (
    <Card className="space-y-5 p-6">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-[15px] leading-6 font-semibold text-neutral-900">{title}</h2>
        {aside}
      </div>
      {children}
    </Card>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid gap-1 sm:grid-cols-[9rem_minmax(0,1fr)] sm:gap-4">
      <dt className="text-[13px] text-neutral-500">{label}</dt>
      <dd className="min-w-0 text-sm leading-6 text-neutral-900">{children}</dd>
    </div>
  );
}

function Diagnostics({ items }: { items: ExtDiagnostic[] }) {
  const { t } = useTranslation();
  const tone = { error: "red", warning: "amber", info: "slate" } as const;
  return (
    <ul className="space-y-3">
      {items.map((d, i) => (
        <li key={i} className="text-sm leading-6">
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={tone[d.severity]}>{t(`extension.sev.${d.severity}`)}</Badge>
            <span className="font-mono text-xs text-neutral-500">{d.code}</span>
          </div>
          <div className="mt-1 text-neutral-900">{d.message}</div>
          {d.fix && <div className="text-neutral-600">{t("extension.fix", { fix: d.fix })}</div>}
          <div className="font-mono text-xs break-all text-neutral-500">{d.where}</div>
        </li>
      ))}
    </ul>
  );
}

/**
 * 安装声明式扩展（/extensions/install），或给一个扩展装出来的连接升级、改设置（?id=<连接>）。
 * 先让管理员看清楚：请求发到哪里、用谁的身份、有哪些工具、哪些会改数据，再填配置、确认。
 */
export default function ExtensionInstall() {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const sep = t("extension.sep");
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const id = Number(params.get("id")) || 0;
  const { can } = useAuth();
  const { notify } = useStore();
  const opts = useScopeOptions();
  const existing = useApi(() => (id ? api.hubAsset(id) : Promise.resolve(null)));
  const file = useRef<HTMLInputElement>(null);
  const { busy, run } = useAction();

  const [picked, setPicked] = useState("");
  const [ins, setIns] = useState<ExtensionInspect | null>(null);
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [over, setOver] = useState(false);

  const [server, setServer] = useState("");
  const [values, setValues] = useState<Record<string, string>>({});
  const [secret, setSecret] = useState("");
  const [slug, setSlug] = useState("");
  const [title, setTitle] = useState("");
  const [scope, setScope] = useState<Scope>(allScope);
  const [delivery, setDelivery] = useState<Delivery>("optional");

  const current = existing.data?.asset;

  const inspect = async (body: { zip?: string; manifest?: string; openapi?: string }, name: string) => {
    setError(null);
    setChecking(true);
    try {
      const res = await api.inspectExtension({ ...body, ...(id ? { id } : {}) });
      setIns(res);
      setPicked(name);
      const m = res.manifest;
      const cur = current?.spec;
      const servers = res.check.servers ?? [];
      setServer(cur?.url && servers.includes(cur.url) ? cur.url : (servers[0] ?? ""));
      const filled: Record<string, string> = {};
      for (const p of res.params ?? []) filled[p.key] = valueText(res.values?.[p.key] ?? p.default);
      setValues(filled);
      if (!id) {
        setSlug(res.slug ?? "");
        setTitle(extText(m?.name, lang).slice(0, 64));
      }
    } catch (e) {
      setIns(null);
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setChecking(false);
    }
  };

  // 升级 / 改设置：先按现在装的清单显示，不用重新上传
  const loaded = useRef(false);
  useEffect(() => {
    const sp = current?.spec;
    if (!id || loaded.current || !sp) return;
    loaded.current = true;
    setTitle(sp.title);
    setSlug(current.slug);
    if (sp.extension?.manifest) void inspect({ manifest: sp.extension.manifest, openapi: sp.openapi ?? "" }, "");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [current, id]);

  const choose = async (list: FileList | null) => {
    const files = Array.from(list ?? []);
    if (files.length === 0) return;
    const zip = files.find((f) => /\.zip$/i.test(f.name));
    if (zip) return inspect({ zip: await toBase64(zip) }, zip.name);
    const manifest = files.find((f) => f.name === MANIFEST);
    if (!manifest) return setError(t("extension.needManifest", { file: MANIFEST }));
    const text = await manifest.text();
    let doc = "";
    let want = "";
    try {
      want = String((JSON.parse(text) as { openapi?: string }).openapi ?? "");
    } catch {
      // 清单不是合法的 JSON：交给服务端检查，那里会说清楚错在哪
    }
    if (want) {
      const base = want.split("/").pop();
      const f = files.find((x) => x.name === base) ?? (files.length === 2 ? files.find((x) => x !== manifest) : undefined);
      if (!f) return setError(t("extension.needOpenapi", { file: want }));
      doc = await f.text();
    }
    return inspect({ manifest: text, openapi: doc }, files.map((f) => f.name).join(sep));
  };

  const m = ins?.manifest;
  const ok = !!ins?.installable;
  const cred = ins?.check.credential;
  // 要管理员填的共享凭据：shared 模式，或员工身份之外另带一份
  const needSecret = cred?.mode === "shared" || !!cred?.sharedHeader;
  const sameExtension = !!current?.spec.extension?.id && current.spec.extension.id === m?.id;
  const upgrading = !!id && sameExtension && current?.spec.extension?.version !== m?.version;
  const hasSecret = !!current?.secret_hint;
  const writes = (ins?.tools ?? []).filter((x) => !x.read_only);

  const submit = async () => {
    if (!ins?.package || !m) return;
    setError(null);
    const vals: Record<string, unknown> = {};
    for (const p of ins.params ?? []) {
      const text = values[p.key] ?? "";
      if (text === "") {
        if (p.required) return setError(t("extension.paramRequired", { name: extText(p.label, lang) || p.key }));
        continue;
      }
      const v = parseValue(p, text);
      if (!v.ok) return setError(t("extension.paramBad", { name: extText(p.label, lang) || p.key, type: p.type ?? "string" }));
      vals[p.key] = v.value;
    }
    if (needSecret && !secret && !hasSecret) return setError(t("extension.secretRequired"));
    const spec: AssetSpec = {
      title: title.trim(),
      description: extText(m.description, lang),
      url: server,
      openapi: ins.package.openapi,
      extension: { manifest: ins.package.manifest, values: vals },
    };
    const r = await run(async () => {
      if (id) {
        const res = await api.newAssetVersion(id, spec, secret || undefined);
        return { id, tools: res.tools, sync_error: res.sync_error };
      }
      const res = await api.createAsset({ kind: "connection", slug: slug.trim(), spec, scope, delivery, ...(secret ? { secret } : {}) });
      return { id: res.asset.id, tools: res.tools, sync_error: res.sync_error };
    });
    if (!r.ok) return setError(r.error);
    if (r.value.sync_error) notify(t("connections.savedSyncFail", { error: r.value.sync_error }));
    else if (id) notify(t(upgrading ? "extension.upgraded" : "extension.saved", { name: title.trim(), version: m.version }));
    else notify(t(writes.length > 0 ? "extension.installedWrites" : "extension.installed", { name: title.trim(), count: r.value.tools ?? 0, writes: writes.length }));
    navigate(assetPath(r.value.id));
  };

  if (!can("connections.manage")) return <Notice tone="warn">{t("extension.noPermission")}</Notice>;

  const pageTitle = id ? t("extension.manageTitle", { name: current?.spec.title ?? "" }) : t("extension.installTitle");
  const credText = () => {
    if (!cred) return t("extension.credNone");
    const where = cred.header || "Authorization: Bearer";
    if (cred.mode === "shared") return t("extension.credShared", { header: cred.sharedHeader });
    return t(cred.mode === "ee_jwt" ? "extension.credEeJwt" : "extension.credUserSecret", { header: where });
  };

  return (
    <div className="max-w-3xl">
      <PageLink page="admin-connections" className={backLinkCls}>
        <ArrowLeft size={14} /> {t("extension.back")}
      </PageLink>
      <PageHeader title={pageTitle} desc={id ? t("extension.manageDesc") : t("extension.installDesc")} />
      <LoadState loading={!!id && existing.loading && !existing.data} error={existing.data || !id ? null : existing.error} onRetry={existing.reload}>
        <div className="space-y-4">
          <Section title={id ? t("extension.pickUpgrade") : t("extension.pick")}>
            <div
              onDragOver={(e) => {
                e.preventDefault();
                setOver(true);
              }}
              onDragLeave={() => setOver(false)}
              onDrop={(e) => {
                e.preventDefault();
                setOver(false);
                void choose(e.dataTransfer.files);
              }}
              className={cx(
                "flex flex-col items-center gap-3 rounded-lg border border-dashed px-4 py-7 text-center transition-colors",
                over ? "border-brand-500 bg-brand-50" : "border-neutral-300 bg-neutral-50",
              )}
            >
              <FileArchive size={26} strokeWidth={1.5} className="text-neutral-400" aria-hidden />
              <p className="max-w-md text-sm leading-6 text-neutral-600">{t("extension.pickHint", { file: MANIFEST })}</p>
              <input
                ref={file}
                type="file"
                multiple
                accept=".zip,.json,.yaml,.yml"
                className="hidden"
                onChange={(e) => {
                  void choose(e.target.files);
                  e.target.value = "";
                }}
              />
              <Button type="button" variant="secondary" disabled={checking} onClick={() => file.current?.click()}>
                {checking ? <Spinner size={14} /> : <Upload size={14} />} {picked || ins ? t("extension.pickAgain") : t("extension.pickButton")}
              </Button>
              {picked && <p className="font-mono text-xs break-all text-neutral-500">{picked}</p>}
            </div>
            {id > 0 && !picked && ins && <p className="text-sm text-neutral-600">{t("extension.showingCurrent")}</p>}
          </Section>

          {ins && !ok && (
            <Section title={t("extension.failedTitle")}>
              <Notice tone="danger">{ins.blocked || t("extension.failedDesc", { count: ins.check.errors })}</Notice>
              {ins.check.results.length > 0 && <Diagnostics items={ins.check.results} />}
              <p className="text-sm text-neutral-600">{t("extension.failedHint")}</p>
            </Section>
          )}

          {ins && ok && m && (
            <>
              {ins.change && (upgrading || !sameExtension) && (
                <Notice tone={ins.change.new_writes?.length || ins.change.credential?.length || ins.change.new_servers?.length ? "warn" : "info"}>
                  <div className="font-medium">{t("extension.changeTitle", { from: ins.change.from, to: `${m.id}@${m.version}` })}</div>
                  <ul className="mt-1 list-disc space-y-0.5 pl-5">
                    {!!ins.change.new_writes?.length && <li>{t("extension.changeWrites", { tools: ins.change.new_writes.join(sep) })}</li>}
                    {!!ins.change.credential?.length && (
                      <li>{t("extension.changeCred", { from: ins.change.credential[0] || t("extension.modeNone"), to: ins.change.credential[1] || t("extension.modeNone") })}</li>
                    )}
                    {!!ins.change.new_servers?.length && <li>{t("extension.changeServers", { servers: ins.change.new_servers.join(sep) })}</li>}
                    {!!ins.change.new_reads?.length && <li>{t("extension.changeReads", { tools: ins.change.new_reads.join(sep) })}</li>}
                    {!!ins.change.removed?.length && <li>{t("extension.changeRemoved", { tools: ins.change.removed.join(sep) })}</li>}
                    {!ins.change.new_writes?.length && !ins.change.credential?.length && !ins.change.new_servers?.length && !ins.change.new_reads?.length && !ins.change.removed?.length && (
                      <li>{t("extension.changeNone")}</li>
                    )}
                  </ul>
                </Notice>
              )}

              <Section title={t("extension.reviewTitle")} aside={<Badge tone="green">{t("extension.checked")}</Badge>}>
                <dl className="space-y-3">
                  <Row label={t("extension.name")}>
                    <div className="font-medium">{extText(m.name, lang)}</div>
                    {extText(m.description, lang) && <div className="text-neutral-600">{extText(m.description, lang)}</div>}
                  </Row>
                  <Row label={t("extension.identity")}>
                    <span className="font-mono text-[13px]">
                      {m.id}@{m.version}
                    </span>
                    <span className="ml-2 font-mono text-xs text-neutral-500" title={ins.check.digest}>
                      sha256:{ins.check.digest?.slice(0, 12)}
                    </span>
                  </Row>
                  <Row label={t("extension.kind")}>{t("extension.kindDeclarative")}</Row>
                  <Row label={t("extension.servers")}>
                    {(ins.check.servers ?? []).length > 1 ? (
                      <select className={cx(inputCls, "font-mono")} aria-label={t("extension.servers")} value={server} onChange={(e) => setServer(e.target.value)}>
                        {ins.check.servers!.map((s) => (
                          <option key={s} value={s}>
                            {s}
                          </option>
                        ))}
                      </select>
                    ) : (
                      <span className="font-mono text-[13px] break-all">{server}</span>
                    )}
                    <div className="text-xs leading-5 text-neutral-500">{t("extension.serversHint")}</div>
                  </Row>
                  <Row label={t("extension.cred")}>
                    {credText()}
                    {cred?.sharedHeader && cred.mode !== "shared" && <div className="text-neutral-600">{t("extension.credPlusShared", { header: cred.sharedHeader })}</div>}
                    {cred?.mode === "ee_jwt" && !!cred.claims?.length && <div className="text-neutral-600">{t("extension.credClaims", { claims: cred.claims.join(sep) })}</div>}
                  </Row>
                </dl>

                <div>
                  <div className="mb-2 flex flex-wrap items-baseline justify-between gap-2">
                    <h3 className="text-[13px] font-medium text-neutral-700">{t("extension.tools", { count: ins.tools?.length ?? 0 })}</h3>
                    {writes.length > 0 && !id && <span className="text-xs text-neutral-500">{t("extension.writesOff", { count: writes.length })}</span>}
                  </div>
                  <ul className="divide-y divide-neutral-100 rounded-md border border-neutral-200">
                    {(ins.tools ?? []).map((tool) => (
                      <li key={tool.name} className="px-3.5 py-2.5 text-sm">
                        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                          <span className="font-mono text-[13px] text-neutral-900">{tool.name}</span>
                          <Badge tone={tool.read_only ? "slate" : "amber"}>{t(tool.read_only ? "extension.readOnly" : "extension.write")}</Badge>
                          <span className="font-mono text-xs break-all text-neutral-500">
                            {tool.method} {tool.path}
                          </span>
                        </div>
                        <div className="mt-0.5 leading-6 text-neutral-600">{tool.description}</div>
                        {!!tool.fixed?.length && <div className="text-xs leading-5 text-neutral-500">{t("extension.toolFixed", { params: tool.fixed.map((f) => f.key).join(sep) })}</div>}
                        {!!tool.keep?.length && <div className="text-xs leading-5 text-neutral-500">{t("extension.toolKeep", { fields: tool.keep.join(sep) })}</div>}
                      </li>
                    ))}
                  </ul>
                </div>

                {!!ins.ignored?.length && <Notice tone="info">{t("extension.ignored", { what: ins.ignored.map((x) => t(`extension.ignoredPart.${x}`)).join(sep) })}</Notice>}

                {ins.check.results.length > 0 && (
                  <details className="text-sm">
                    <summary className="cursor-pointer text-neutral-700">{t("extension.notes", { count: ins.check.results.length })}</summary>
                    <div className="mt-3">
                      <Diagnostics items={ins.check.results} />
                    </div>
                  </details>
                )}
              </Section>

              <Section title={t("extension.configTitle")}>
                {(ins.params ?? []).map((p) => {
                  const label = (extText(p.label, lang) || p.key) + (p.required ? " *" : "");
                  const hint = [extText(p.description, lang), t("extension.paramUsedBy", { tools: p.tools.join(sep) }), p.fill === "app" ? t("extension.paramApp") : ""].filter(Boolean).join(" ");
                  const set = (v: string) => setValues((x) => ({ ...x, [p.key]: v }));
                  const value = values[p.key] ?? "";
                  return (
                    <Field key={p.key} label={label} hint={hint}>
                      {p.choices?.length || p.type === "boolean" ? (
                        <select className={inputCls} value={value} onChange={(e) => set(e.target.value)}>
                          {!p.required || value === "" ? <option value="">{t("extension.paramUnset")}</option> : null}
                          {(p.choices?.length ? p.choices.map(valueText) : ["true", "false"]).map((c) => (
                            <option key={c} value={c}>
                              {c}
                            </option>
                          ))}
                        </select>
                      ) : (
                        <input
                          className={cx(inputCls, p.type === "array" || p.type === "object" ? "font-mono" : "")}
                          inputMode={p.type === "number" || p.type === "integer" ? "decimal" : undefined}
                          value={value}
                          onChange={(e) => set(e.target.value)}
                        />
                      )}
                    </Field>
                  );
                })}

                {needSecret && (
                  <>
                    <Field label={t("extension.sharedSecret")} hint={t("extension.sharedSecretHint", { header: cred?.sharedHeader })}>
                      <PasswordInput
                        label={t("extension.sharedSecret")}
                        className={inputCls}
                        autoComplete="new-password"
                        value={secret}
                        onChange={(e) => setSecret(e.target.value)}
                        placeholder={hasSecret ? t("connections.secretSet", { hint: current!.secret_hint }) : ""}
                      />
                    </Field>
                  </>
                )}

                <Field label={t("extension.connName")} hint={t("extension.connNameHint", { slug: slug || "oa" })}>
                  <input className={cx(inputCls, "font-mono")} value={slug} disabled={!!id} onChange={(e) => setSlug(e.target.value.toLowerCase())} placeholder="oa" />
                </Field>
                <Field label={t("editor.title")}>
                  <input className={inputCls} value={title} maxLength={64} onChange={(e) => setTitle(e.target.value)} />
                </Field>

                {!id && (
                  <>
                    <Field label={t("editor.scope")}>
                      <ScopeEditor value={scope} onChange={setScope} roles={opts.roles} users={opts.users} departments={opts.departments} />
                    </Field>
                    <Field label={t("editor.delivery")} hint={delivery === "auto" ? t("editor.deliveryAutoHint") : t("editor.deliveryOptionalHint")}>
                      <Segmented
                        value={delivery}
                        onChange={setDelivery}
                        options={[
                          { value: "optional", label: t("asset.deliveryOptional") },
                          { value: "auto", label: t("asset.deliveryAuto") },
                        ]}
                      />
                    </Field>
                  </>
                )}
              </Section>
            </>
          )}

          <ErrorNote>{error}</ErrorNote>
          <div className="flex gap-2">
            {ins && ok && (
              <Button disabled={busy || !title.trim() || !slug.trim()} onClick={submit}>
                {id ? (upgrading ? t("extension.upgrade", { version: m?.version }) : t("extension.save")) : t("extension.install")}
              </Button>
            )}
            <Button variant="ghost" onClick={() => navigate(-1)}>
              {t("common.cancel")}
            </Button>
          </div>
        </div>
      </LoadState>
    </div>
  );
}
