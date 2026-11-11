package api

import (
	"net/http"

	"github.com/hikariming/dsh-ee/server/internal/rbac"
)

// authKind 是一条路由的认证方式。
type authKind int

const (
	// authNone：公开接口，或自带认证（令牌、npm 协议、网关）。
	authNone authKind = iota
	// authUser：网页会话。
	authUser
	// authAgent：DSH 里的接入插件带 agent 范围的令牌，等价于代这位员工操作。
	authAgent
)

// route 是路由表的一行。网页接口与 Agent 工具同源：处理函数只有一份，
// 校验、审计、通知、额度等副作用在两边完全一致。
type route struct {
	pattern string
	h       http.HandlerFunc
	auth    authKind
	// 任一即可；空表示只需登录
	perms []string
	// 对应的 Agent 工具；nil 时必须出现在 agentExcluded 或 agentPending 里（有测试卡住）
	agent *agentSpec
}

// handler 是注册到 mux 的完整处理链（含认证）。
func (r route) handler(s *Server) http.Handler {
	switch r.auth {
	case authUser:
		return s.requireUser(r.inner(s))
	case authAgent:
		return s.requireAgent(r.inner(s))
	}
	return r.inner(s)
}

// inner 是认证之后的处理链：权限点校验加处理函数。Agent 工具调用直接走这里。
func (r route) inner(s *Server) http.Handler {
	if s.opts.DemoSite != nil && demoSiteBlocked[r.pattern] {
		return http.HandlerFunc(demoSiteRefuse)
	}
	if len(r.perms) > 0 {
		return s.requirePerm(r.perms...)(r.h)
	}
	return r.h
}

// routes 是全部 HTTP 路由。前端静态资源与 404 兜底不在表里，见 Handler。
func (s *Server) routes() []route {
	pub := func(pattern string, h http.HandlerFunc) route { return route{pattern: pattern, h: h} }
	// 登录即可；每个接口按权限点校验，菜单只是前端的展示配置
	usr := func(pattern string, h http.HandlerFunc) route { return route{pattern: pattern, h: h, auth: authUser} }
	prm := func(pattern string, h http.HandlerFunc, codes ...string) route {
		return route{pattern: pattern, h: h, auth: authUser, perms: codes}
	}
	agt := func(pattern string, h http.HandlerFunc, codes ...string) route {
		return route{pattern: pattern, h: h, auth: authAgent, perms: codes}
	}

	rs := []route{
		pub("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]string{"status": "ok", "version": s.opts.Version})
		}),

		// 示例内容里的演示差旅系统，只认示例连接带的密钥
		pub("GET /demo/travel/summary", s.handleDemoSummary),
		pub("GET /demo/travel/claims", s.handleDemoClaims),
		pub("POST /demo/travel/claims", s.handleDemoCreateClaim),
		pub("GET /demo/travel/monthly", s.handleDemoMonthly),
		pub("GET /demo/crm/summary", s.handleDemoCRMSummary),
		pub("GET /demo/crm/customers", s.handleDemoCRMCustomers),
		pub("GET /demo/crm/customers/{id}", s.handleDemoCRMCustomer),
		pub("GET /demo/crm/renewals", s.handleDemoCRMRenewals),
		pub("POST /demo/crm/visits", s.handleDemoCRMVisit),
		pub("GET /demo/itsm/summary", s.handleDemoTicketSummary),
		pub("GET /demo/itsm/tickets", s.handleDemoTickets),
		pub("POST /demo/itsm/tickets", s.handleDemoCreateTicket),
		pub("GET /demo/itsm/categories", s.handleDemoTicketCategories),

		pub("GET /api/auth/methods", s.handleAuthMethods),
		pub("GET /api/auth/oidc/start", s.handleOIDCStart),
		pub("GET /api/auth/oidc/callback", s.handleOIDCCallback),
		pub("GET /api/auth/handoff", s.handleAuthHandoff),
		pub("POST /api/auth/login", s.handleLogin),
		pub("POST /api/auth/demo-login", s.handleDemoLogin),
		pub("GET /demo/intro/{lang}", s.handleDemoVideo),
		pub("POST /api/auth/logout", s.handleLogout),
		usr("GET /api/auth/me", s.handleMe),
		usr("POST /api/auth/password", s.handleChangePassword),
		pub("POST /api/device/code", s.handleDeviceCode),
		pub("POST /api/device/token", s.handleDeviceToken),
		usr("GET /api/device/{user_code}", s.handleDeviceInfo),
		usr("POST /api/device/{user_code}", s.handleDeviceApprove),
		pub("POST /api/dsh/logout", s.handleDshLogout),
		usr("GET /api/me/menus", s.handleMyMenus),

		// 员工：额度、查看并自助创建 / 吊销自己的 key
		prm("GET /api/me/keys", s.handleMyKeys, rbac.KeysSelf),
		prm("POST /api/me/keys", s.handleCreateMyKey, rbac.KeysSelf),
		prm("DELETE /api/me/keys/{id}", s.handleRevokeMyKey, rbac.KeysSelf),
		prm("GET /api/me/tokens", s.handleMyTokens, rbac.KeysSelf, rbac.RegistryUse),
		prm("POST /api/me/tokens", s.handleCreateMyToken, rbac.KeysSelf, rbac.RegistryUse),
		prm("DELETE /api/me/tokens/{id}", s.handleRevokeMyToken, rbac.KeysSelf, rbac.RegistryUse),
		usr("POST /api/me/tokens/{id}/elevate", s.handleElevate),
		usr("DELETE /api/me/tokens/{id}/elevate", s.handleEndElevate),
		usr("GET /api/me/admin-link", s.handleAdminLink),
		prm("GET /api/me/usage", s.handleMyUsage, rbac.PortalView),
		prm("GET /api/me/calls/model", s.handleMyModelCalls, rbac.LLMUse),
		prm("GET /api/me/calls/connection", s.handleMyConnectionCalls, rbac.PortalView),
		prm("GET /api/me/connections", s.handleMyConnections, rbac.PortalView),
		prm("PUT /api/me/connections/{id}/credential", s.handleSetMyCredential, rbac.PortalView),
		prm("DELETE /api/me/connections/{id}/credential", s.handleDeleteMyCredential, rbac.PortalView),
		prm("POST /api/me/connection-requests", s.handleCreateConnectionRequest, rbac.PortalView),
		prm("GET /api/me/access-target", s.handleAccessTarget, rbac.PortalView),
		prm("GET /api/me/access-requests", s.handleMyAccessRequests, rbac.PortalView),
		prm("POST /api/me/access-requests", s.handleCreateAccessRequest, rbac.PortalView),
		prm("POST /api/me/access-requests/{id}", s.handleResolveAccessRequest, rbac.PortalView),
		prm("GET /api/me/doctor", s.handleMyDoctor, rbac.PortalView),
		prm("GET /api/me/dsh-clients", s.handleMyDshClients, rbac.PortalView),
		usr("GET /api/me/welcome", s.handleWelcome),
		usr("PUT /api/me/welcome", s.handlePutWelcome),
		usr("POST /api/me/get-started", s.handleCreateSetupCode),
		usr("GET /api/me/get-started/{code}", s.handleSetupCodeStatus),

		// 用户（分配额度、分发 key、分配角色的页面也需要列出用户）
		prm("GET /api/admin/demo", s.handleDemoStatus, rbac.SettingsManage),
		prm("POST /api/admin/demo", s.handleDemoImport, rbac.SettingsManage),
		prm("DELETE /api/admin/demo", s.handleDemoClear, rbac.SettingsManage),
		prm("GET /api/admin/users", s.handleListUsers, rbac.UsersView, rbac.UsersManage, rbac.QuotasManage, rbac.KeysManage, rbac.RolesManage),
		prm("POST /api/admin/users", s.handleCreateUser, rbac.UsersManage),
		prm("PATCH /api/admin/users/{id}", s.handleUpdateUser, rbac.UsersManage),
		prm("PUT /api/admin/users/{id}/roles", s.handleSetUserRoles, rbac.RolesManage),
		prm("PATCH /api/admin/users/{id}/quota", s.handleUpdateUserQuota, rbac.QuotasManage),

		// 角色与权限点
		prm("GET /api/admin/permissions", s.handleListPermissions, rbac.RolesManage, rbac.MenusManage),
		prm("POST /api/admin/permissions", s.handleCreatePermission, rbac.RolesManage),
		prm("DELETE /api/admin/permissions/{code}", s.handleDeletePermission, rbac.RolesManage),
		prm("GET /api/admin/roles", s.handleListRoles, rbac.RolesManage, rbac.UsersManage),
		prm("POST /api/admin/roles", s.handleCreateRole, rbac.RolesManage),
		prm("PATCH /api/admin/roles/{id}", s.handleUpdateRole, rbac.RolesManage),
		prm("DELETE /api/admin/roles/{id}", s.handleDeleteRole, rbac.RolesManage),

		// 菜单与页面
		prm("GET /api/admin/menus", s.handleListMenus, rbac.MenusManage),
		prm("POST /api/admin/menus", s.handleCreateMenu, rbac.MenusManage),
		prm("PUT /api/admin/menus/order", s.handleReorderMenus, rbac.MenusManage),
		prm("POST /api/admin/menus/reset", s.handleResetMenus, rbac.MenusManage),
		prm("PATCH /api/admin/menus/{id}", s.handleUpdateMenu, rbac.MenusManage),
		prm("DELETE /api/admin/menus/{id}", s.handleDeleteMenu, rbac.MenusManage),

		// 模型接入与 key
		prm("GET /api/admin/providers", s.handleListProviders, rbac.ProvidersManage, rbac.QuotasManage, rbac.KeysManage),
		prm("POST /api/admin/providers", s.handleCreateProvider, rbac.ProvidersManage),
		prm("PATCH /api/admin/providers/{id}", s.handleUpdateProvider, rbac.ProvidersManage),
		prm("DELETE /api/admin/providers/{id}", s.handleDeleteProvider, rbac.ProvidersManage),
		prm("POST /api/admin/providers/{id}/check", s.handleCheckProvider, rbac.ProvidersManage),
		prm("GET /api/admin/models", s.handleListModels, rbac.ProvidersManage, rbac.QuotasManage, rbac.KeysManage),
		prm("POST /api/admin/models", s.handleCreateModels, rbac.ProvidersManage),
		prm("PATCH /api/admin/models/{id}", s.handleUpdateModel, rbac.ProvidersManage),
		prm("DELETE /api/admin/models/{id}", s.handleDeleteModel, rbac.ProvidersManage),
		prm("GET /api/admin/keys", s.handleListKeys, rbac.KeysManage),
		prm("POST /api/admin/keys", s.handleIssueKey, rbac.KeysManage),
		prm("DELETE /api/admin/keys/{id}", s.handleRevokeKey, rbac.KeysManage),
		usr("GET /api/admin/tokens", s.handleAdminTokens),
		usr("POST /api/admin/tokens", s.handleIssueToken),
		usr("POST /api/admin/tokens/revoke", s.handleRevokeTokens),

		// 系统设置：登录即可读（前端要用组织名、作用域、评分开关），按段落校验写权限
		usr("GET /api/settings", s.handleGetSettings),
		prm("PUT /api/admin/settings", s.handlePutSettings, rbac.SettingsManage, rbac.RegistryManage, rbac.UpstreamManage),

		// LDAP / AD 登录：配置里有服务账号，单独存、单独授权
		prm("GET /api/admin/auth/ldap", s.handleGetLDAP, rbac.SettingsManage),
		prm("PUT /api/admin/auth/ldap", s.handlePutLDAP, rbac.SettingsManage),
		prm("POST /api/admin/auth/ldap/test", s.handleTestLDAP, rbac.SettingsManage),
		prm("POST /api/admin/auth/ldap/sync", s.handleSyncLDAP, rbac.SettingsManage),
		prm("GET /api/admin/report/monthly", s.handleMonthlyReport, rbac.DashboardView),
		prm("PUT /api/admin/report/params", s.handlePutReportParams, rbac.DashboardView),
		prm("GET /api/admin/auth/oidc", s.handleGetOIDC, rbac.SettingsManage),
		prm("PUT /api/admin/auth/oidc", s.handlePutOIDC, rbac.SettingsManage),
		prm("POST /api/admin/auth/oidc/test", s.handleTestOIDC, rbac.SettingsManage),

		// 能力中心：技能、提示词、微应用、企业连接，插件的可用范围与下发方式
		prm("GET /api/hub", s.handleHub, rbac.PortalView),
		prm("POST /api/hub", s.handleHubCreate, rbac.RegistryPublish, rbac.ConnectionsManage),
		prm("GET /api/hub/{id}", s.handleHubDetail, rbac.PortalView),
		prm("PATCH /api/hub/{id}", s.handleHubPatch, rbac.PortalView),
		prm("DELETE /api/hub/{id}", s.handleHubDelete, rbac.PluginsManage, rbac.ConnectionsManage),
		prm("POST /api/hub/{id}/versions", s.handleHubNewVersion, rbac.RegistryPublish, rbac.PluginsManage, rbac.ConnectionsManage),
		prm("GET /api/hub/{id}/export", s.handleAppFileExport, rbac.PortalView),
		prm("POST /api/hub/import", s.handleAppImport, rbac.RegistryPublish),
		prm("POST /api/me/workbench-preview", s.handleWorkbenchPreview, rbac.PortalView),
		prm("POST /api/me/app-drafts", s.handleAppDraft, rbac.RegistryPublish),
		prm("POST /api/hub/{id}/subscription", s.handleHubSubscribe, rbac.PortalView),
		prm("DELETE /api/hub/{id}/subscription", s.handleHubSubscribe, rbac.PortalView),
		prm("GET /api/admin/hub/review", s.handleHubReviewQueue, rbac.PluginsManage),
		prm("POST /api/admin/hub/review", s.handleHubReview, rbac.PluginsManage),
		prm("POST /api/admin/hub/scope-review", s.handleHubScopeReview, rbac.PluginsManage),
		prm("PUT /api/admin/plugin-policies/{name...}", s.handlePutPluginPolicy, rbac.PluginsManage),
		prm("POST /api/admin/extensions/inspect", s.handleExtensionInspect, rbac.ConnectionsManage),
		prm("POST /api/admin/connections/test", s.handleConnTest, rbac.ConnectionsManage),
		prm("POST /api/admin/connections/{id}/sync", s.handleConnSync, rbac.ConnectionsManage),
		prm("PATCH /api/admin/connections/{id}/tools/{name}", s.handleConnTool, rbac.ConnectionsManage),
		// 按员工本人的身份调用（user_identity.go）：谁填了凭据、ee_jwt 的签名密钥和公钥
		prm("GET /api/admin/connections/{id}/credentials", s.handleConnCredentials, rbac.ConnectionsManage),
		prm("DELETE /api/admin/connections/{id}/credentials/{user}", s.handleRevokeConnCredential, rbac.ConnectionsManage),
		prm("GET /api/admin/ee-jwt", s.handleEEJWTInfo, rbac.ConnectionsManage),
		prm("POST /api/admin/ee-jwt/rotate", s.handleEEJWTRotate, rbac.ConnectionsManage),
		pub("GET /.well-known/jwks.json", s.handleJWKS),

		// 知识库：建库、权限、文档、试搜与检索记录（员工在 DSH 里用 /mcp 的 kb__ 工具检索）
		prm("GET /api/kb", s.handleKBList, rbac.PortalView),
		prm("POST /api/kb", s.handleKBCreate, rbac.KnowledgeManage),
		prm("PUT /api/kb/settings", s.handleKBSettings, rbac.KnowledgeManage),
		prm("POST /api/kb/search", s.handleKBSearch, rbac.PortalView),
		prm("GET /api/kb/queries", s.handleKBQueries, rbac.PortalView),
		prm("GET /api/kb/{id}", s.handleKBDetail, rbac.PortalView),
		prm("PATCH /api/kb/{id}", s.handleKBPatch, rbac.PortalView),
		prm("DELETE /api/kb/{id}", s.handleKBDelete, rbac.KnowledgeManage),
		prm("POST /api/kb/{id}/docs", s.handleKBUpload, rbac.PortalView),
		prm("POST /api/kb/{id}/docs/check", s.handleKBUploadCheck, rbac.PortalView),
		prm("GET /api/kb/{id}/gaps", s.handleKBGaps, rbac.PortalView),
		prm("POST /api/kb/{id}/gaps/ignore", s.handleKBGapIgnore, rbac.PortalView),
		prm("GET /api/kb-docs/{id}", s.handleKDocDetail, rbac.PortalView),
		prm("PATCH /api/kb-docs/{id}", s.handleKDocPatch, rbac.PortalView),
		prm("DELETE /api/kb-docs/{id}", s.handleKDocDelete, rbac.PortalView),
		prm("POST /api/kb-docs/{id}/file", s.handleKDocReplace, rbac.PortalView),
		prm("POST /api/kb-docs/{id}/reconvert", s.handleKDocReconvert, rbac.PortalView),
		prm("GET /api/kb-docs/{id}/original", s.handleKDocOriginal, rbac.PortalView),

		// DSH 接入：接入插件的发布，以及员工本机 DSH 调用的目录与 MCP 网关（令牌认证）
		prm("GET /api/connect", s.handleConnectInfo, rbac.PortalView),
		prm("GET /api/admin/connect", s.handleAdminConnect, rbac.SettingsManage),
		prm("POST /api/admin/connect/publish", s.handlePublishConnect, rbac.SettingsManage),
		pub("GET /api/dsh/whoami", s.handleDshWhoami),
		pub("POST /api/dsh/web-link", s.handleDshWebLink),
		pub("POST /api/dsh/feedback", s.handleDshFeedback),

		// 应用自带数据（M13）：网页用；DSH 里的模型走 /mcp 上的 app__ 工具
		prm("GET /api/apps/{slug}/data", s.handleAppTables, rbac.PortalView),
		prm("GET /api/apps/{slug}/data/{table}", s.handleAppRows, rbac.PortalView),
		prm("POST /api/apps/{slug}/data/{table}", s.handleAppInsert, rbac.PortalView),
		prm("PATCH /api/apps/{slug}/data/{table}/{id}", s.handleAppUpdate, rbac.PortalView),
		prm("DELETE /api/apps/{slug}/data/{table}/{id}", s.handleAppDelete, rbac.PortalView),
		prm("GET /api/apps/{slug}/data/{table}/export", s.handleAppExport, rbac.PortalView),
		// 数据中台（M15）：管理沿用「管理企业连接」
		prm("GET /api/admin/data", s.handleDataOverview, rbac.ConnectionsManage),
		prm("POST /api/admin/data/tables", s.handleDataUpload, rbac.ConnectionsManage),
		prm("GET /api/admin/data/tables/{name}", s.handleDataTablePreview, rbac.ConnectionsManage),
		prm("DELETE /api/admin/data/tables/{name}", s.handleDataTableDelete, rbac.ConnectionsManage),
		prm("POST /api/admin/data/run", s.handleDataRun, rbac.ConnectionsManage),
		prm("POST /api/admin/data/datasets", s.handleDataSetSave, rbac.ConnectionsManage),
		prm("PUT /api/admin/data/datasets/{slug}", s.handleDataSetSave, rbac.ConnectionsManage),
		prm("DELETE /api/admin/data/datasets/{slug}", s.handleDataSetDelete, rbac.ConnectionsManage),
		prm("GET /api/data/datasets", s.handleMyDataSets, rbac.PortalView),
		// 网页版应用（M14）
		prm("GET /api/me/apps/{slug}/web", s.handleWebApp, rbac.PortalView),
		prm("POST /api/me/apps/{slug}/source", s.handleWebAppSource, rbac.PortalView),
		pub("GET /api/dsh/catalog", s.handleDshCatalog),
		pub("GET /api/dsh/brand/logo", s.handleBrandLogo),
		prm("PUT /api/admin/brand/logo", s.handleUploadBrandLogo, rbac.SettingsManage),
		pub("GET /api/dsh/skills/{name}", s.handleDshSkill),
		pub("POST /api/dsh/apps/{name}/subscription", s.handleDshAppSubscription),
		pub("DELETE /api/dsh/apps/{name}/subscription", s.handleDshAppSubscription),
		prm("GET /api/dsh/releases", s.handleDshReleases, rbac.PortalView),
		prm("GET /dl/{target}/{file}", s.handleDownloadDshRelease, rbac.PortalView),
		pub("GET /dl/connect/{file}", s.handleConnectTarball),
		prm("GET /api/admin/dsh-releases", s.handleAdminDshReleases, rbac.SettingsManage),
		prm("POST /api/admin/dsh-releases/check", s.handleCheckDshReleases, rbac.SettingsManage),
		prm("POST /api/admin/dsh-releases/{id}/mirror", s.handleMirrorDshRelease, rbac.SettingsManage),
		prm("POST /api/admin/dsh-releases/{id}/recommend", s.handleRecommendDshRelease, rbac.SettingsManage),
		prm("DELETE /api/admin/dsh-releases/{id}", s.handleDeleteDshRelease, rbac.SettingsManage),
		pub("/mcp", s.handleMCP),

		// 管理员向导：首次部署清单、日常巡检、审核时读内容、连接申请
		prm("GET /api/admin/setup-status", s.handleSetupStatus, rbac.SettingsManage),
		prm("GET /api/admin/attention", s.handleAttention, rbac.DashboardView, rbac.PluginsManage, rbac.ConnectionsManage, rbac.QuotasManage, rbac.UsersManage, rbac.UsersView, rbac.UpstreamManage, rbac.ProvidersManage),
		prm("GET /api/admin/review/inspect", s.handleInspectPackage, rbac.PluginsManage),
		prm("GET /api/admin/hub/inspect", s.handleInspectAsset, rbac.PluginsManage, rbac.ConnectionsManage),
		prm("GET /api/admin/connection-requests", s.handleListConnectionRequests, rbac.ConnectionsManage),
		prm("POST /api/admin/connection-requests/{id}", s.handleResolveConnectionRequest, rbac.ConnectionsManage),

		// 统计与审计
		prm("GET /api/admin/stats", s.handleStats, rbac.DashboardView),
		prm("GET /api/admin/audit", s.handleListAudit, rbac.AuditView),
		prm("GET /api/me/overview", s.handleMyOverview, rbac.PortalView),

		// 能力市场与发布（员工）
		prm("GET /api/catalog", s.handleCatalog, rbac.PortalView),
		prm("GET /api/catalog/{name...}", s.handleCatalogDetail, rbac.PortalView),
		prm("GET /api/me/packages", s.handleMyPackages, rbac.RegistryPublish),
		prm("POST /api/me/packages", s.handleUploadPackage, rbac.RegistryPublish),
		prm("POST /api/me/packages/check", s.handleCheckPackage, rbac.RegistryPublish),
		agt("POST /api/agent/packages", s.handleUploadPackage, rbac.RegistryPublish),
		prm("POST /api/me/import-requests", s.handleImportRequest, rbac.RegistryPublish),
		prm("GET /api/me/npm-tokens", s.handleMyNpmTokens, rbac.RegistryUse, rbac.RegistryPublish),
		prm("POST /api/me/npm-tokens", s.handleCreateMyNpmToken, rbac.RegistryUse, rbac.RegistryPublish),
		prm("DELETE /api/me/npm-tokens/{id}", s.handleRevokeMyNpmToken, rbac.RegistryUse, rbac.RegistryPublish),

		// 能力管理与审核
		prm("GET /api/admin/packages", s.handleAdminPackages, rbac.PluginsManage, rbac.RegistryManage, rbac.DashboardView),
		prm("GET /api/admin/packages/{name...}", s.handleAdminPackageDetail, rbac.PluginsManage, rbac.RegistryManage),
		prm("PATCH /api/admin/packages/{name...}", s.handlePatchPackage, rbac.PluginsManage),
		prm("GET /api/admin/review", s.handleReviewQueue, rbac.PluginsManage),
		prm("POST /api/admin/review", s.handleReview, rbac.PluginsManage),
		prm("GET /api/admin/tarball", s.handleAdminTarball, rbac.PluginsManage),

		// npm 源与上游预载
		prm("GET /api/admin/registry", s.handleRegistryOverview, rbac.RegistryManage, rbac.UpstreamManage, rbac.DashboardView),
		prm("POST /api/admin/registry/check", s.handleRegistryCheck, rbac.RegistryManage),
		prm("GET /api/admin/npm-tokens", s.handleAllNpmTokens, rbac.RegistryManage),
		prm("DELETE /api/admin/npm-tokens/{id}", s.handleRevokeNpmToken, rbac.RegistryManage),
		prm("GET /api/admin/upstream/feed", s.handleUpstreamFeed, rbac.UpstreamManage),
		prm("POST /api/admin/upstream/sync", s.handleUpstreamSync, rbac.UpstreamManage),
		prm("GET /api/admin/upstream/jobs", s.handleListJobs, rbac.UpstreamManage),
		prm("GET /api/admin/upstream/jobs/{id}", s.handleGetJob, rbac.UpstreamManage),
		prm("POST /api/admin/upstream/check-updates", s.handleCheckUpdates, rbac.UpstreamManage),
		prm("GET /api/admin/upstream/export", s.handleExportBundle, rbac.UpstreamManage),
		prm("POST /api/admin/upstream/import", s.handleImportBundle, rbac.UpstreamManage),

		// npm 协议与 DSH 插件索引
		pub("/npm/", s.handleNpm),
		pub("GET /index/dsh-index.json", s.handleDshIndex),
		pub("GET /setup.sh", s.handleSetupSh),
		pub("GET /setup.ps1", s.handleSetupPs1),

		// OpenAI 兼容网关，用个人 key 鉴权
		pub("GET /v1/models", s.handleModels),
		pub("POST /v1/chat/completions", s.handleChatCompletions),
	}
	for i := range rs {
		rs[i].agent = agentSpecs[rs[i].pattern]
	}
	return rs
}
