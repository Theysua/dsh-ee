import { useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useTranslation } from "react-i18next";
import { ArrowLeft, Download, MoreHorizontal, RefreshCw, Terminal } from "lucide-react";
import { api, formatTime, type ConnectionTool, type HubAsset } from "../lib/api";
import { useAction, useApi } from "../lib/use-api";
import { useStore } from "../lib/store";
import { useAuth } from "../lib/auth";
import { useConfirm } from "../lib/confirm";
import { assetPath, assetState, dshHow, KIND_ICONS, scopeSummary } from "../lib/hub";
import { useLabels } from "../lib/labels";
import { pluginPath } from "../components/plugin-card";
import { LoadState, ErrorNote } from "../components/api-ui";
import { AppDataPanel } from "../components/app-data";
import { AppMap } from "../components/app-map";
import { AppDepsCard } from "../components/access-requests";
import { UsedByCard } from "../components/used-by";
import { CredentialHolders, MyCredentialCard } from "../components/connection-identity";
import { ScopeDialog, useScopeOptions } from "../components/scope-dialog";
import { Badge, Button, Card, CardHeader, cx, inputCls, Toggle, Empty, backLinkCls } from "../components/ui";
import { statusBadge } from "../components/status";
import { PageLink, useMenus } from "../lib/menus";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/shadcn/dropdown-menu";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/shadcn/dialog";

function Info({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <dt className="text-xs text-neutral-500">{label}</dt>
      <dd className="mt-0.5 text-sm text-neutral-900">{children}</dd>
    </div>
  );
}

function Body({ text }: { text?: string }) {
  if (!text) return null;
  return <pre className="max-h-[32rem] overflow-auto rounded-md bg-neutral-50 p-4 text-sm whitespace-pre-wrap text-neutral-800">{text}</pre>;
}

/** 下架要写原因。 */
function UnpublishDialog({ open, onOpenChange, onConfirm }: { open: boolean; onOpenChange: (v: boolean) => void; onConfirm: (note: string) => Promise<void> }) {
  const { t } = useTranslation();
  const [note, setNote] = useState("");
  const { busy, run } = useAction();
  const [error, setError] = useState<string | null>(null);
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("asset.unpublish")}</DialogTitle>
        </DialogHeader>
        <textarea className={cx(inputCls, "h-24 py-2")} placeholder={t("asset.unpublishReason")} value={note} onChange={(e) => setNote(e.target.value)} />
        <ErrorNote>{error}</ErrorNote>
        <DialogFooter>
          <Button variant="secondary" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button
            variant="danger"
            disabled={busy || !note.trim()}
            onClick={async () => {
              const r = await run(() => onConfirm(note.trim()));
              if (r.ok) onOpenChange(false);
              else setError(r.error);
            }}
          >
            {t("asset.unpublish")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** 工具的角色限定。 */
function ToolRolesDialog({
  tool,
  roles,
  onOpenChange,
  onSave,
}: {
  tool: ConnectionTool | null;
  roles: { id: number; name: string; code: string; is_super: boolean }[];
  onOpenChange: (v: boolean) => void;
  onSave: (t: ConnectionTool, roles: number[]) => Promise<void>;
}) {
  const { t } = useTranslation();
  const L = useLabels();
  const [sel, setSel] = useState<number[]>([]);
  const [key, setKey] = useState("");
  if (tool && key !== tool.name) {
    setKey(tool.name);
    setSel(tool.roles ?? []);
  }
  return (
    <Dialog open={!!tool} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t("asset.toolRoles")} · <span className="font-mono">{tool?.name}</span>
          </DialogTitle>
        </DialogHeader>
        <p className="text-sm text-neutral-600">{sel.length === 0 ? t("asset.toolRolesAll") : ""}</p>
        <div className="flex flex-wrap gap-x-5 gap-y-2">
          {roles.map((r) => (
            <label key={r.id} className="flex items-center gap-2 text-sm text-neutral-800">
              <input
                type="checkbox"
                className="h-4 w-4 accent-neutral-900"
                checked={sel.includes(r.id)}
                onChange={(e) => setSel(e.target.checked ? [...sel, r.id] : sel.filter((x) => x !== r.id))}
              />
              {L.role(r)}
            </label>
          ))}
        </div>
        <DialogFooter>
          <Button variant="secondary" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button onClick={() => tool && onSave(tool, sel).then(() => onOpenChange(false))}>{t("common.save")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** 换了 id（比如从微应用点进它依赖的技能）就重新挂载，重新加载数据。 */
export default function AssetDetailRoute() {
  const { id } = useParams();
  return <AssetDetail key={id} />;
}

function AssetDetail() {
  const { t } = useTranslation();
  const { id } = useParams();
  const navigate = useNavigate();
  const confirm = useConfirm();
  const { notify } = useStore();
  const { user, can } = useAuth();
  const { pathOf } = useMenus();
  const detail = useApi(() => api.hubAsset(Number(id)));
  const hub = useApi(api.hub);
  const opts = useScopeOptions();
  const { busy, run } = useAction();
  const [scopeOpen, setScopeOpen] = useState(false);
  const [unpubOpen, setUnpubOpen] = useState(false);
  const [rolesFor, setRolesFor] = useState<ConnectionTool | null>(null);
  const d = detail.data;
  const a = d?.asset;
  const bySlug = useMemo(() => new Map((hub.data?.assets ?? []).map((x) => [x.slug, x])), [hub.data]);

  const act = async (fn: () => Promise<unknown>, done: string) => {
    const r = await run(fn);
    notify(r.ok ? done : r.error);
    if (r.ok) {
      detail.reload();
      hub.reload();
    }
    return r.ok;
  };

  if (!a) {
    return (
      <LoadState loading={detail.loading} error={detail.error} onRetry={detail.reload}>
        <Card><Empty>{t("asset.notFound")}</Empty></Card>
      </LoadState>
    );
  }

  const Icon = KIND_ICONS[a.kind];
  const state = assetState(a);
  const owns = a.owner_id !== null && a.owner_id === user?.id;
  const canEdit = a.can_manage || owns;
  const server = d.server_name ?? "ee";
  // 扩展装出来的连接：地址、凭据、工具由清单决定，改设置和升级走扩展页面
  const editPath = a.kind === "connection" && a.spec.extension?.id ? `/extensions/install?id=${a.id}` : `/assets/${a.id}/edit`;

  const toggleSub = () =>
    act(() => (a.subscribed ? api.unsubscribe(a.id) : api.subscribe(a.id)), t(a.subscribed ? "hub.disabledMsg" : "hub.enabledMsg", { title: a.spec.title }));

  const depList = (slugs: string[] | undefined, kind: "skill" | "connection") =>
    (slugs ?? []).map((slug) => {
      const x: HubAsset | undefined = bySlug.get(slug);
      return (
        <li key={kind + slug} className="flex items-center gap-2 text-sm">
          <Badge>{t(`assetKinds.${kind}`)}</Badge>
          {x ? (
            <Link to={assetPath(x.id)} className="text-neutral-900 hover:underline">
              {x.spec.title} <span className="font-mono text-xs text-neutral-500">{slug}</span>
            </Link>
          ) : (
            <span className="text-neutral-500">
              <span className="font-mono">{slug}</span> · {t("asset.missingDep")}
            </span>
          )}
        </li>
      );
    });

  return (
    <div className="max-w-5xl">
      <PageLink page={a.kind === "app" ? (owns ? "build" : "apps") : "materials"} className={backLinkCls}>
        <ArrowLeft size={14} /> {t("asset.back")}
      </PageLink>

      <div className="mb-6 flex flex-wrap items-start gap-4">
        <span className="flex h-14 w-14 shrink-0 items-center justify-center rounded-lg bg-neutral-100 text-neutral-700">
          {a.kind === "app" && a.spec.icon ? <span className="text-3xl">{a.spec.icon}</span> : <Icon size={28} strokeWidth={1.5} />}
        </span>
        <div className="min-w-0 flex-1 basis-60">
          <h1 className="text-2xl text-neutral-900 leading-8 font-bold tracking-[-0.02em]">{a.spec.title}</h1>
          <div className="font-mono text-sm text-neutral-500">{a.kind === "connection" ? a.slug : `/${a.slug}`}</div>
          <div className="mt-2 flex flex-wrap gap-1.5">
            <Badge>{t(`assetKinds.${a.kind}`)}</Badge>
            {a.live_version > 0 && <Badge>{t("asset.version", { n: a.live_version })}</Badge>}
            {a.enabled && <Badge tone="green">{t("hub.enabledBadge")}</Badge>}
            {a.delivery === "auto" && <Badge tone="brand">{t("hub.autoBadge")}</Badge>}
            {state === "unpublished" && <Badge tone="red">{t("hub.unpublishedBadge")}</Badge>}
            {state === "outOfScope" && <Badge>{t("hub.outOfScopeBadge")}</Badge>}
            {a.pending > 0 && <Badge tone="amber">{t("hub.pendingBadge")}</Badge>}
            {a.pending_scope && <Badge tone="amber">{t("build.stateSharing")}</Badge>}
          </div>
          <p className="mt-3 text-sm text-neutral-700">{a.spec.description}</p>
        </div>
        <div className="flex flex-wrap gap-2">
          {a.available && a.delivery === "optional" && (
            <Button variant={a.subscribed ? "secondary" : "primary"} disabled={busy} onClick={toggleSub}>
              {a.subscribed ? t("hub.disable") : t("hub.enable")}
            </Button>
          )}
          {a.kind === "app" && can("registry.publish") && (
            <Button variant="secondary" onClick={() => navigate(`${pathOf("app-new")}?from=${a.id}`)}>
              {t("studio.copyAsMine")}
            </Button>
          )}
          {a.kind === "connection" && a.available && can("registry.publish") && (
            <Button variant="secondary" onClick={() => navigate(`${pathOf("app-new")}?connection=${encodeURIComponent(a.slug)}`)}>
              {t("studio.makeAppFromConn")}
            </Button>
          )}
          {a.kind === "app" && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="secondary">
                  <Download size={15} /> {t("studio.export")}
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem asChild>
                  <a href={api.appFileUrl(a.id, false)} download>{t("studio.exportApp")}</a>
                </DropdownMenuItem>
                {(a.spec.knowledge?.length ?? 0) > 0 && (
                  <DropdownMenuItem asChild>
                    <a href={api.appFileUrl(a.id, true)} download>{t("studio.exportWithDocs")}</a>
                  </DropdownMenuItem>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
          {canEdit && (
            <Button variant="secondary" onClick={() => navigate(editPath)}>
              {a.kind === "connection" ? t(a.spec.extension?.id ? "extension.manage" : "asset.editConnection") : t("asset.newVersion")}
            </Button>
          )}
          {canEdit && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="secondary" aria-label={t("table.more")}>
                  <MoreHorizontal size={16} />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onSelect={() => setScopeOpen(true)}>{t("asset.editScope")}</DropdownMenuItem>
                {a.can_manage && (
                  <>
                    <DropdownMenuSeparator />
                    {a.status === "active" ? (
                      <DropdownMenuItem variant="destructive" onSelect={() => setUnpubOpen(true)}>
                        {t("asset.unpublish")}
                      </DropdownMenuItem>
                    ) : (
                      <DropdownMenuItem onSelect={() => act(() => api.patchAsset(a.id, { status: "active" }), t("asset.restored", { title: a.spec.title }))}>
                        {t("asset.restore")}
                      </DropdownMenuItem>
                    )}
                    <DropdownMenuItem
                      variant="destructive"
                      onSelect={async () => {
                        if (!(await confirm({ kind: "delete", description: t("asset.deleteConfirm", { title: a.spec.title }) }))) return;
                        const r = await run(() => api.deleteAsset(a.id));
                        notify(r.ok ? t("asset.deleted", { title: a.spec.title }) : r.error);
                        if (r.ok) navigate(-1);
                      }}
                    >
                      {t("asset.delete")}
                    </DropdownMenuItem>
                  </>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>
      </div>

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_300px]">
        <div className="min-w-0 space-y-4">
          <Card className="p-5">
            <h2 className="mb-2 flex items-center gap-2 text-neutral-900 text-[15px] leading-6 font-semibold">
              <Terminal size={16} /> {t("asset.howToUse")}
            </h2>
            <p className="text-sm text-neutral-700">{dshHow(t, a, server)}</p>
            {a.available && !a.enabled && <p className="mt-2 text-sm text-amber-700">{t("asset.notEnabledHint")}</p>}
            {a.pending > 0 && (a.can_manage || owns) && <p className="mt-2 text-sm text-amber-700">{t("asset.pendingHint", { count: a.pending })}</p>}
            {a.pending_scope && (a.can_manage || owns) && (
              <p className="mt-2 text-sm text-amber-700">{t("build.sharingHint", { scope: scopeSummary(t, a.pending_scope, opts.roleName) })}</p>
            )}
            {!a.pending_scope && a.scope_note && owns && <p className="mt-2 text-sm text-red-700">{t("build.shareRejected", { note: a.scope_note })}</p>}
          </Card>

          {(a.kind === "skill" || a.kind === "prompt") && (
            <Card className="p-5">
              <h2 className="mb-3 text-neutral-900 text-[15px] leading-6 font-semibold">{t("asset.content")}</h2>
              {a.spec.when_to_use && (
                <p className="mb-3 text-sm text-neutral-700">
                  <span className="text-neutral-500">{t("asset.whenToUse")}：</span>
                  {a.spec.when_to_use}
                </p>
              )}
              <Body text={a.spec.body} />
            </Card>
          )}

          {a.kind === "app" && (
            <>
              <AppMap a={a} deps={d.deps} bySlug={bySlug} />
              {(a.spec.highlights?.length ?? 0) > 0 && (
                <Card className="p-5">
                  <h2 className="mb-3 text-neutral-900 text-[15px] leading-6 font-semibold">{t("asset.highlights")}</h2>
                  <ul className="list-disc space-y-1 pl-5 text-sm text-neutral-700">
                    {a.spec.highlights!.map((h) => <li key={h}>{h}</li>)}
                  </ul>
                </Card>
              )}
              <Card className="p-5">
                <h2 className="mb-3 text-neutral-900 text-[15px] leading-6 font-semibold">{t("asset.persona")}</h2>
                <Body text={a.spec.persona} />
                {(a.spec.starters?.length ?? 0) > 0 && (
                  <>
                    <h3 className="mt-5 mb-2 text-sm font-semibold text-neutral-900">{t("asset.starters")}</h3>
                    <ul className="list-disc space-y-1 pl-5 text-sm text-neutral-700">
                      {a.spec.starters!.map((s) => (
                        <li key={s}>{s}</li>
                      ))}
                    </ul>
                  </>
                )}
              </Card>
              {d.deps ? (
                <AppDepsCard app={{ slug: a.slug, title: a.spec.title }} deps={d.deps} onChanged={() => { detail.reload(); hub.reload(); }} />
              ) : (
                <Card className="p-5">
                  <h2 className="mb-3 text-neutral-900 text-[15px] leading-6 font-semibold">{t("asset.dependsOn")}</h2>
                  <ul className="space-y-2">
                    {depList(a.spec.skills, "skill")}
                    {depList(a.spec.connections, "connection")}
                    {(a.spec.plugins ?? []).map((p) => (
                      <li key={p} className="flex items-center gap-2 text-sm">
                        <Badge>{t("hub.tabs.plugin")}</Badge>
                        <Link to={pluginPath(p)} className="font-mono text-neutral-900 hover:underline">
                          {p}
                        </Link>
                      </li>
                    ))}
                  </ul>
                  {!a.spec.skills?.length && !a.spec.connections?.length && !a.spec.plugins?.length && <p className="text-sm text-neutral-600">{t("asset.noDeps")}</p>}
                </Card>
              )}
              {a.live_version > 0 && a.spec.workbench && (
                <Card className="flex flex-wrap items-center justify-between gap-3 p-5">
                  <div>
                    <h2 className="text-neutral-900 text-[15px] leading-6 font-semibold">{t("appWeb.toggle")}</h2>
                    <p className="mt-1 text-sm text-neutral-600">{a.spec.web ? `/a/${a.slug}` : t("appWeb.previewOnly")}</p>
                  </div>
                  <Link to={`/a/${a.slug}`} className="text-sm font-medium text-brand-600 dark:text-brand-300 hover:underline">{t("appWeb.open")} →</Link>
                </Card>
              )}
              {(a.spec.tables?.length ?? 0) > 0 && a.live_version > 0 && <AppDataPanel slug={a.slug} />}
            </>
          )}

          {a.kind === "connection" && (
            <Card>
              <CardHeader
                title={t("asset.tools")}
                aside={
                  a.can_manage && (
                    <Button
                      size="sm"
                      variant="secondary"
                      disabled={busy}
                      onClick={async () => {
                        const r = await run(() => api.syncConnection(a.id));
                        if (!r.ok) return notify(r.error);
                        notify(r.value.ok ? t("asset.synced", { count: r.value.tools }) : t("asset.syncFailed", { error: r.value.error }));
                        detail.reload();
                      }}
                    >
                      <RefreshCw size={14} /> {t("asset.sync")}
                    </Button>
                  )
                }
              />
              {a.spec.instructions && <p className="px-5 pb-3 text-sm text-neutral-700">{a.spec.instructions}</p>}
              {(d.tools ?? []).length === 0 ? (
                <p className="px-5 pb-5 text-sm text-neutral-600">{t("asset.noTools")}</p>
              ) : (
                <ul>
                  {d.tools!.map((tool) => (
                    <li key={tool.name} className="flex items-start gap-3 border-t border-neutral-100 px-5 py-3">
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="font-mono text-sm text-neutral-900">{tool.name}</span>
                          <Badge tone={tool.read_only ? "slate" : "amber"}>{tool.read_only ? t("asset.toolReadOnly") : t("asset.toolWrite")}</Badge>
                          {a.can_manage && (tool.roles?.length ?? 0) > 0 && <Badge tone="brand">{tool.roles!.map(opts.roleName).join("、")}</Badge>}
                        </div>
                        <p className="mt-0.5 line-clamp-2 text-sm text-neutral-600">{tool.description}</p>
                      </div>
                      {a.can_manage && (
                        <div className="flex shrink-0 items-center gap-2">
                          {opts.roles && (
                            <Button size="sm" variant="ghost" onClick={() => setRolesFor(tool)}>
                              {t("asset.toolRoles")}
                            </Button>
                          )}
                          <Toggle
                            checked={tool.enabled}
                            onChange={(v) =>
                              act(() => api.setConnectionTool(a.id, tool.name, { enabled: v, roles: tool.roles ?? [] }), t("asset.toolUpdated", { name: tool.name }))
                            }
                          />
                        </div>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </Card>
          )}

          {a.kind === "connection" && d.my_credential && a.live_version > 0 && <MyCredentialCard assetId={a.id} cred={d.my_credential} reload={detail.reload} />}
          {a.kind === "connection" && a.can_manage && a.spec.user_auth?.mode === "user_secret" && <CredentialHolders key={d.my_credential?.updated_at ?? ""} assetId={a.id} />}

          {a.kind === "connection" && d.calls && (
            <Card>
              <CardHeader title={t("asset.calls")} />
              {d.calls.length === 0 ? (
                <p className="px-5 pb-5 text-sm text-neutral-600">{t("asset.noCalls")}</p>
              ) : (
                <div className="overflow-x-auto"><table className="tbl">
                  <tbody>
                    {d.calls.map((c, i) => (
                      <tr key={i} className="border-t border-neutral-100">
                        <td className="px-5 py-2 whitespace-nowrap text-neutral-600 tabular-nums">{formatTime(c.created_at)}</td>
                        <td className="px-5 py-2">{c.username}</td>
                        <td className="px-5 py-2 font-mono text-xs">
                          {c.tool}
                          {c.extension && (
                            <div className="text-neutral-500" title={`${t("extension.callVersion")} · sha256:${c.digest ?? ""}`}>
                              {c.extension.slice(c.extension.lastIndexOf("@"))}
                            </div>
                          )}
                        </td>
                        <td className="px-5 py-2">
                          {c.auth_mode && <span className="mr-2"><Badge>{t(`identity.modeShort.${c.auth_mode}`)}</Badge></span>}
                          {c.ok ? <Badge tone="green">{t("asset.callOk")}</Badge> : <Badge tone="red">{t("asset.callFail")}</Badge>}
                          {c.error && <span className="ml-2 text-xs text-neutral-500">{c.error}</span>}
                        </td>
                        <td className="px-5 py-2 text-right text-neutral-600 tabular-nums">{c.latency_ms} ms</td>
                      </tr>
                    ))}
                  </tbody>
                </table></div>
              )}
            </Card>
          )}

          {d.used_by && <UsedByCard apps={d.used_by} />}

          {d.versions && (
            <Card>
              <CardHeader title={t("asset.versions")} />
              <ul>
                {d.versions.map((v) => (
                  <li key={v.version} className="border-t border-neutral-100 px-5 py-3 text-sm">
                    <div className="flex items-center gap-2">
                      <span className="font-medium text-neutral-900">{t("asset.version", { n: v.version })}</span>
                      {statusBadge[v.status]}
                      <span className="ml-auto text-xs text-neutral-500">{t("asset.versionBy", { name: v.publisher || "—", time: formatTime(v.created_at) })}</span>
                    </div>
                    {v.review_note && <p className="mt-1 text-xs text-neutral-600">{t("asset.reviewNote", { note: v.review_note })}</p>}
                  </li>
                ))}
              </ul>
            </Card>
          )}
        </div>

        <Card className="h-fit p-5">
          <dl className="space-y-4">
            <Info label={t("asset.owner")}>{[a.owner_name || a.owner, a.department].filter(Boolean).join(" · ") || "—"}</Info>
            <Info label={t("asset.scope")}>{scopeSummary(t, a.scope, opts.roleName)}</Info>
            <Info label={t("asset.delivery")}>{a.delivery === "auto" ? t("asset.deliveryAuto") : t("asset.deliveryOptional")}</Info>
            <Info label={t("asset.usage")}>{t("asset.usageValue", { uses: a.uses, users: a.users })}</Info>
            {a.kind === "connection" && a.can_manage && (
              <>
                <Info label={t("connections.type")}>{a.spec.extension?.id ? t("extension.badge") : a.spec.type === "openapi" ? t("connections.typeOpenapi") : t("connections.typeMcp")}</Info>
                {a.spec.extension?.id && (
                  <Info label={t("extension.identity")}>
                    <span className="font-mono text-xs break-all">
                      {a.spec.extension.id}@{a.spec.extension.version}
                    </span>
                    <div className="font-mono text-xs break-all text-neutral-500" title={a.spec.extension.digest}>
                      sha256:{a.spec.extension.digest?.slice(0, 12)}
                    </div>
                  </Info>
                )}
                {a.spec.url && (
                  <Info label={a.spec.extension?.id ? t("extension.servers") : a.spec.type === "openapi" ? t("connections.apiBase") : t("connections.url")}>
                    <span className="font-mono text-xs break-all">{a.spec.url}</span>
                  </Info>
                )}
                {a.secret_hint && <Info label={t("connections.secret")}>{t("asset.secretHint", { hint: a.secret_hint })}</Info>}
                {a.spec.user_auth && <Info label={t("identity.mode")}>{t(a.spec.user_auth.mode === "ee_jwt" ? "identity.eeJwt" : "identity.userSecret")}</Info>}
              </>
            )}
            {d.feedback && (
              <Info label={t("asset.feedback")}>{t("asset.feedbackValue", { up: d.feedback.up, down: d.feedback.down, up30d: d.feedback.up_30d })}</Info>
            )}
            <Info label={t("asset.updated")}>{formatTime(a.updated_at)}</Info>
          </dl>
        </Card>
      </div>

      {(d.questions?.length ?? 0) > 0 && (
        <Card className="p-5">
          <h3 className="mb-3 text-sm font-semibold text-neutral-900">{t("asset.questions")}</h3>
          <ul className="space-y-2 text-sm text-neutral-700">
            {d.questions!.map((q) => (
              <li key={q.id} className="flex items-baseline gap-2">
                <span className="min-w-0 flex-1 truncate" title={q.question}>{q.question}</span>
                <span className="shrink-0 text-xs text-neutral-500">{t("asset.questionCount", { count: q.count })}</span>
              </li>
            ))}
          </ul>
        </Card>
      )}

      <ScopeDialog
        open={scopeOpen}
        onOpenChange={setScopeOpen}
        title={t("asset.editScope")}
        description={a.can_manage ? undefined : t("build.shareDesc")}
        scope={a.pending_scope ?? a.scope}
        delivery={a.delivery}
        canDelivery={a.can_manage}
        selfId={owns && a.kind !== "connection" ? user?.id : undefined}
        labels={{ auto: t("asset.deliveryAuto"), optional: t("asset.deliveryOptional"), autoHint: t("editor.deliveryAutoHint"), optionalHint: t("editor.deliveryOptionalHint") }}
        onSave={async (scope, delivery) => {
          const res = await api.patchAsset(a.id, a.can_manage ? { scope, delivery } : { scope });
          notify(res.pending_scope ? t("build.shareSubmitted") : t("asset.scopeSaved"));
          detail.reload();
          hub.reload();
        }}
      />
      <UnpublishDialog
        open={unpubOpen}
        onOpenChange={setUnpubOpen}
        onConfirm={async (note) => {
          await api.patchAsset(a.id, { status: "unpublished", note });
          notify(t("asset.unpublished", { title: a.spec.title }));
          detail.reload();
          hub.reload();
        }}
      />
      {opts.roles && (
        <ToolRolesDialog
          tool={rolesFor}
          roles={opts.roles}
          onOpenChange={(v) => !v && setRolesFor(null)}
          onSave={async (tool, roles) => {
            await act(() => api.setConnectionTool(a.id, tool.name, { enabled: tool.enabled, roles }), t("asset.toolUpdated", { name: tool.name }));
          }}
        />
      )}
    </div>
  );
}
