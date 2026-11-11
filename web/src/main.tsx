import { StrictMode, useCallback } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter, Navigate, Route, Routes, useParams, useSearchParams } from "react-router";
import "./index.css";
import "./i18n";
import { initTheme } from "./lib/theme";
import { StoreProvider } from "./lib/store";
import { AuthProvider, RequireAuth, useAuth } from "./lib/auth";
import { api, type SetupStatus } from "./lib/api";
import { MenuProvider, useMenus } from "./lib/menus";
import { AppLayout } from "./components/layout";
import { Toaster } from "@/components/shadcn/sonner";
import { TooltipProvider } from "@/components/shadcn/tooltip";
import { ConfirmProvider } from "./lib/confirm";
import { PAGE_COMPONENTS } from "./pages/registry";
import { PAGE_META, type PageKey } from "./lib/page-meta";
import Landing from "./pages/landing";
import Login from "./pages/login";
import Account from "./pages/account";
import PluginDetail from "./pages/plugin-detail";
import AssetDetail from "./pages/asset-detail";
import AssetEditor from "./pages/asset-editor";
import ExtensionInstall from "./pages/extension-install";
import AppWebPage from "./pages/app-web";
import AccessRequestPage from "./pages/access-request";
import KnowledgeBase from "./pages/knowledge-base";
import KnowledgeDoc from "./pages/knowledge-doc";
import DevicePage from "./pages/device";
import Onboarding from "./pages/onboarding";
import GetStarted from "./pages/get-started";
import Report from "./pages/report";
import { useApi } from "./lib/use-api";
import { useTranslation } from "react-i18next";
import { Compass } from "lucide-react";
import { Spinner } from "./components/ui";

function Status({ title, children }: { title: string; children?: React.ReactNode }) {
  return (
    <div className="flex flex-col items-center px-6 py-24 text-center">
      <span aria-hidden className="mb-4 flex h-12 w-12 items-center justify-center rounded-full bg-neutral-100 text-neutral-400 ring-1 ring-neutral-200/70 ring-inset">
        <Compass size={22} strokeWidth={1.6} />
      </span>
      <h1 className="text-2xl leading-8 font-bold tracking-[-0.02em] text-neutral-900">{title}</h1>
      {children && <p className="mt-2 max-w-md text-sm leading-6 text-neutral-600">{children}</p>}
    </div>
  );
}

/** 整页加载中：菜单、清单还没回来时的占位。 */
function PageLoading() {
  const { t } = useTranslation();
  return (
    <div role="status" className="flex items-center justify-center gap-2 py-24 text-sm text-neutral-500">
      <Spinner size={18} className="text-neutral-400" />
      {t("common.loading")}
    </div>
  );
}

/**
 * /go：登录后统一的分流点。管理员的必做项没配完时就地显示部署清单；
 * 还没有 DSH 接入过、也没点过「以后再说」的人去「开始使用」；其余进入菜单里的第一个页面。
 */
function Start() {
  const { homePath, loading } = useMenus();
  const { can } = useAuth();
  const setup = useApi<SetupStatus | null>(() => can("settings.manage") ? api.setupStatus() : Promise.resolve(null));
  const welcome = useApi(api.welcome);
  const { t } = useTranslation();
  // 清单刷新时一并刷新接入状态：Agent 配置期间管理员的 DSH 会接入
  const reload = useCallback(() => {
    setup.reload();
    welcome.reload();
  }, [setup.reload, welcome.reload]);
  // 只有第一次加载显示加载中；之后的刷新不能卸掉清单页（上面有进行中的 Agent 接入）
  if (loading || (!setup.data && setup.loading) || (!welcome.data && welcome.loading)) return <PageLoading />;
  if (!homePath) return <Status title={t("shell.noPages")}>{t("shell.noPagesHint")}</Status>;
  // /go?setup=1：必做项都完成后，管理员也能回来看清单（例如导入示例内容）
  const forced = new URLSearchParams(window.location.search).has("setup");
  if (setup.data && (!setup.data.required_done || forced)) return <Onboarding status={setup.data} reload={reload} />;
  if (welcome.data?.show) return <Navigate to="/get-started" replace />;
  return <Navigate to={homePath} replace />;
}

/**
 * /connections/<标识>?app=<应用>：DSH 工作台里「无权限 → 去申请」的链接只知道连接的标识。
 * 能看到这个连接就去详情页；看不到（正是没权限的时候）就去申请页。
 * 知识库数据源（kb）没有具体的库：去应用详情的「依赖与权限」，那里列出缺哪个库。
 */
function ConnectionBySlug() {
  const { slug = "" } = useParams();
  const [params] = useSearchParams();
  const app = params.get("app") ?? "";
  const hub = useApi(api.hub);
  if (hub.loading && !hub.data) return <PageLoading />;
  if (slug === "kb") {
    const a = hub.data?.assets.find((x) => x.kind === "app" && x.slug === app);
    return <Navigate to={a ? `/assets/${a.id}` : "/knowledge"} replace />;
  }
  const a = hub.data?.assets.find((x) => x.kind === "connection" && x.slug === slug);
  if (a?.available) return <Navigate to={`/assets/${a.id}`} replace />;
  return <Navigate to={`/request?${new URLSearchParams({ kind: "connection", slug, ...(app ? { app } : {}) })}`} replace />;
}

function NotFound() {
  const { loading } = useMenus();
  const { t } = useTranslation();
  if (loading) return <PageLoading />;
  return <Status title={t("shell.notFound")}>{t("shell.notFoundHint")}</Status>;
}

/** 旧链接等菜单加载完再跳，避免落到默认路由。 */
function LegacyPage({ pageKey }: { pageKey: PageKey }) {
  const { pathOf, loading } = useMenus();
  if (loading) return <PageLoading />;
  return <Navigate to={pathOf(pageKey)} replace />;
}

/**
 * 旧的能力中心（/hub、/market）：员工这边只剩应用，其余类型在「造 → 原料」里。
 * 按原来的 tab 跳到对应位置，书签和 DSH 里的旧链接都还能用。
 */
function LegacyHub() {
  const { pathOf, loading } = useMenus();
  const [params] = useSearchParams();
  if (loading) return <PageLoading />;
  const tab = params.get("tab");
  if (tab === "plugin") return <Navigate to={`${pathOf("materials")}?advanced=1`} replace />;
  if (tab === "skill" || tab === "prompt" || tab === "connection") return <Navigate to={`${pathOf("materials")}?tab=${tab}`} replace />;
  return <Navigate to={pathOf("apps")} replace />;
}

/** 路由来自后端菜单：页面的路由、名称、访问权限都可在「菜单与页面」里在线修改。 */
function AppRoutes() {
  const { menus } = useMenus();
  const { can } = useAuth();
  const pages = menus.filter((m) => m.kind === "page" && m.page_key in PAGE_COMPONENTS);
  // 从系统设置拆出来的页面：管理员改过菜单、迁移没能把它们挂上时，仍可从默认路由打开
  const splitOut = (["admin-brand", "admin-auth"] as const).filter((k) => can("settings.manage") && !pages.some((m) => m.page_key === k));
  return (
    <Routes>
      <Route index element={<Landing />} />
      <Route path="login" element={<Login />} />
      <Route
        path="report/monthly"
        element={
          <RequireAuth>
            <Report />
          </RequireAuth>
        }
      />
      <Route
        path="a/:slug"
        element={
          <RequireAuth>
            <AppWebPage />
          </RequireAuth>
        }
      />
      <Route
        path="get-started"
        element={
          <RequireAuth>
            <GetStarted />
          </RequireAuth>
        }
      />
      <Route
        element={
          <RequireAuth>
            <AppLayout />
          </RequireAuth>
        }
      >
        <Route path="go" element={<Start />} />
        {/* 旧地址：书签与旧版本链接 */}
        <Route path="start" element={<Navigate to="/go" replace />} />
        <Route path="account" element={<Account />} />
        <Route path="device" element={<DevicePage />} />
        <Route path="plugins/:name" element={<PluginDetail />} />
        <Route path="assets/new" element={<AssetEditor />} />
        <Route path="extensions/install" element={<ExtensionInstall />} />
        <Route path="assets/:id" element={<AssetDetail />} />
        <Route path="assets/:id/edit" element={<AssetEditor />} />
        <Route path="connections/:slug" element={<ConnectionBySlug />} />
        <Route path="request" element={<AccessRequestPage />} />
        <Route path="knowledge/docs/:id" element={<KnowledgeDoc />} />
        <Route path="knowledge/:id" element={<KnowledgeBase />} />
        {!pages.some((m) => m.path === "/market") && <Route path="market" element={<LegacyHub />} />}
        {!pages.some((m) => m.path === "/hub") && <Route path="hub" element={<LegacyHub />} />}
        {!pages.some((m) => m.path === "/keys") && <Route path="keys" element={<LegacyPage pageKey="my-keys" />} />}
        {!pages.some((m) => m.path === "/admin/keys") && <Route path="admin/keys" element={<LegacyPage pageKey="admin-keys" />} />}
        {splitOut.map((k) => {
          const Page = PAGE_COMPONENTS[k];
          return <Route key={k} path={PAGE_META[k].defaultPath.replace(/^\//, "")} element={<Page />} />;
        })}
        {pages.map((m) => {
          const Page = PAGE_COMPONENTS[m.page_key as PageKey];
          return <Route key={m.id} path={m.path.replace(/^\//, "")} element={<Page />} />;
        })}
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  );
}

// 首屏的 .dark 类已由 index.html 的内联脚本加好，这里接着做运行时切换与「跟随系统」
initTheme();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <AuthProvider>
      <StoreProvider>
        <MenuProvider>
          <TooltipProvider delayDuration={300}>
            <ConfirmProvider>
              <BrowserRouter>
                <AppRoutes />
              </BrowserRouter>
              <Toaster position="bottom-center" />
            </ConfirmProvider>
          </TooltipProvider>
        </MenuProvider>
      </StoreProvider>
    </AuthProvider>
  </StrictMode>,
);
