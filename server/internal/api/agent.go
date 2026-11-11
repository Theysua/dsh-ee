package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hikariming/dsh-ee/server/internal/mcp"
	"github.com/hikariming/dsh-ee/server/internal/rbac"
	"github.com/hikariming/dsh-ee/server/internal/secret"
)

// Agent 工具：DSH 里的模型通过 MCP 调 dsh-ee，等价于这位员工在网页上操作。
//
// 工具按领域合并：一个工具加一个 action 枚举，既省提示词，也方便按权限整体过滤。
// 调用不重写业务逻辑——把入参拼成请求，直接交给路由表里的处理函数，
// 所以校验、审计、通知、额度与网页完全一致。

type effect string

const (
	effRead   effect = "read"
	effWrite  effect = "write"
	effDanger effect = "danger"
)

// agentParam 是一个入参。同名参数在同一个工具的不同 action 里必须同类型。
type agentParam struct {
	Name     string
	Type     string // string / integer / boolean / object / array
	Required bool
	Desc     string
	Enum     []string
	// path：取代路径里的 {Name}；query：放进查询串；body：放进 JSON 请求体；
	// local：只给 Shape 用，不转发。空表示 GET / DELETE 为 query，其余为 body。
	In string
	// 没传时用这个值（目前只给路径参数用，例如 id 默认 current）
	Default string
	// 转发时的字段名，默认同 Name
	As     string
	Items  string         // array 的元素类型，默认 string
	Schema map[string]any // 复杂类型直接给 JSON Schema，覆盖 Type / Enum / Items
}

type agentSpec struct {
	Tool   string
	Action string
	// 给模型看的一句话
	Summary string
	// 审批框里的英文短语（写 / 危险操作才用）
	En     string
	Effect effect
	Params []agentParam
	// 只有管理模式开启期间才可用（令牌带 admin 范围且没到期）
	Admin bool
	// 额外的可见条件（例如只有账号有管理权限的人才看得到「申请管理模式」）
	Show func(p *Principal) bool
	// 自己组装请求体（入参不是请求体的简单子集时用，例如把一组改动合并进完整设置）；返回值取代按参数拼出的 body
	Body func(ctx context.Context, s *Server, p *Principal, args map[string]any) (map[string]any, error)
	// 由 DSH 里的接入插件在本机完成（要读本机文件）；服务端只声明，不执行
	Local bool
	// 危险操作必填：生成给人看的预览，说明将要发生什么
	Preview func(ctx context.Context, s *Server, p *Principal, args map[string]any) (string, error)
	// 裁剪响应，减少回给模型的字段；nil 时原样返回
	Shape func(p *Principal, args map[string]any, body []byte) any
}

func (sp *agentSpec) key() string { return sp.Tool + "." + sp.Action }

// agentExcluded 是永久不开放给 Agent 的路由，必须写清原因。
var agentExcluded = map[string]string{
	"GET /healthz":                              "探活接口",
	"GET /demo/travel/summary":                  "示例差旅系统，模型经企业连接调用",
	"GET /demo/travel/claims":                   "示例差旅系统，模型经企业连接调用",
	"POST /demo/travel/claims":                  "示例差旅系统，模型经企业连接调用",
	"GET /demo/travel/monthly":                  "示例业务系统，模型经企业连接调用",
	"GET /demo/crm/summary":                     "示例业务系统，模型经企业连接调用",
	"GET /demo/crm/customers":                   "示例业务系统，模型经企业连接调用",
	"GET /demo/crm/customers/{id}":              "示例业务系统，模型经企业连接调用",
	"GET /demo/crm/renewals":                    "示例业务系统，模型经企业连接调用",
	"POST /demo/crm/visits":                     "示例业务系统，模型经企业连接调用",
	"GET /demo/itsm/summary":                    "示例业务系统，模型经企业连接调用",
	"GET /demo/itsm/tickets":                    "示例业务系统，模型经企业连接调用",
	"POST /demo/itsm/tickets":                   "示例业务系统，模型经企业连接调用",
	"GET /demo/itsm/categories":                 "示例业务系统，模型经企业连接调用",
	"DELETE /api/admin/demo":                    "清除示例数据会删账号和内容，由超级管理员在网页上确认",
	"GET /api/admin/demo":                       "管理员在网页上导入示例内容",
	"POST /api/admin/demo":                      "导入示例会建账号并把应用推给全员，由管理员在网页上操作，演示账号的密码不进模型上下文",
	"GET /api/auth/methods":                     "登录页用",
	"GET /api/auth/oidc/start":                  "企业统一登录由人在浏览器完成",
	"GET /api/auth/oidc/callback":               "企业统一登录由人在浏览器完成",
	"GET /api/auth/handoff":                     "浏览器打开一次性登录链接用，链接只经接入插件交给浏览器",
	"POST /api/dsh/web-link":                    "接入插件打开网页时自己调用；链接能直接登录网页，不进模型上下文",
	"POST /api/dsh/feedback":                    "接入插件的 👍👎 按钮自己调用；员工自己打分，不让模型替人评价",
	"GET /api/apps/{slug}/data":                 "应用数据给网页用；模型经 /mcp 的 app__rows / app__write",
	"GET /api/apps/{slug}/data/{table}":         "应用数据给网页用；模型经 /mcp 的 app__rows",
	"POST /api/apps/{slug}/data/{table}":        "应用数据给网页用；模型经 /mcp 的 app__write",
	"PATCH /api/apps/{slug}/data/{table}/{id}":  "应用数据给网页用；模型经 /mcp 的 app__write",
	"DELETE /api/apps/{slug}/data/{table}/{id}": "应用数据给网页用；模型经 /mcp 的 app__write（删除要确认）",
	"GET /api/apps/{slug}/data/{table}/export":  "导出 CSV 是给人下载的文件",
	"GET /api/me/apps/{slug}/web":               "网页版应用给浏览器用；DSH 里的工作台走接入插件",
	"GET /api/hub/{id}/export":                  "导出的应用文件是给人下载的",
	"POST /api/hub/import":                      "导入要选本机文件，在网页上做",
	"POST /api/me/workbench-preview":            "编辑器预览取数给网页用；模型直接调对应的连接、知识库或 app__ 工具",
	"POST /api/me/app-drafts":                   "草稿给网页编辑器用；DSH 里的 AI 直接用 ee_hub 的 for_me 建应用",
	"GET /api/me/access-target":                 "申请页显示对象名称用；模型用 ee_connections.requests",
	"POST /api/me/access-requests/{id}":         "审批会改别人的权限，由审批人在网页「我的工作台」里确认；连接申请另有 ee_connections_admin.resolve",
	"GET /api/admin/data":                       "数据中台由管理员在网页上维护；模型经 /mcp 的 data__dataset 读数据集",
	"POST /api/admin/data/tables":               "上传表格要选本机文件，在网页上做",
	"GET /api/admin/data/tables/{name}":         "数据中台由管理员在网页上维护",
	"DELETE /api/admin/data/tables/{name}":      "删表会让依赖它的数据集失效，由管理员在网页上确认",
	"POST /api/admin/data/run":                  "试运行 SQL 由管理员在网页上做；模型只能用写好的数据集",
	"POST /api/admin/data/datasets":             "数据集的 SQL 由管理员在网页上写并试运行",
	"PUT /api/admin/data/datasets/{slug}":       "数据集的 SQL 由管理员在网页上写并试运行",
	"DELETE /api/admin/data/datasets/{slug}":    "删数据集会让引用它的工作台失效，由管理员在网页上确认",
	"GET /api/data/datasets":                    "编辑器提示用；模型经 data__dataset 的说明知道有哪些数据集",
	"POST /api/me/apps/{slug}/source":           "网页版应用取数；模型直接调对应的连接、知识库或 app__ 工具",
	"GET /api/admin/auth/oidc":                  "统一登录配置含 client secret 的设置，由管理员在网页上改",
	"PUT /api/admin/auth/oidc":                  "统一登录配置含 client secret，由管理员在网页上改",
	"POST /api/admin/auth/oidc/test":            "统一登录配置由管理员在网页上试连",
	"GET /api/admin/report/monthly":             "月度报告是给人看的可打印网页；同样的数据 Agent 用 ee_stats 读",
	"PUT /api/admin/report/params":              "报告的估算参数由管理员在报告页上调",
	"POST /api/auth/login":                      "登录由人完成",
	"POST /api/auth/demo-login":                 "演示站访客在登录页上点选身份",
	"GET /demo/intro/{lang}":                    "演示站首页的介绍视频",
	"POST /api/auth/logout":                     "会话由网页管理",
	"POST /api/auth/password":                   "凭据只在网页上由本人操作",
	"POST /api/device/code":                     "设备登录协议入口",
	"POST /api/device/token":                    "设备登录协议入口",
	"GET /api/device/{user_code}":               "授权必须由人在浏览器完成",
	"POST /api/device/{user_code}":              "授权必须由人在浏览器完成，Agent 不能替自己授权",
	"POST /api/dsh/logout":                      "接入插件的退出登录",
	"POST /api/dsh/apps/{name}/subscription":    "员工在接入插件界面上亲手启用微应用，不让模型被提示注入诱导去改员工的配置",
	"DELETE /api/dsh/apps/{name}/subscription":  "员工在接入插件界面上亲手停用微应用，不让模型被提示注入诱导去改员工的配置",
	"GET /api/me/menus":                         "网页菜单",
	"GET /api/me/dsh-clients":                   "下载页轮询 Desktop 接入状态",
	"GET /api/me/keys":                          "旧版 key 由网页管理",
	"POST /api/me/keys":                         "旧版 key 由网页管理，明文不进模型上下文",
	"DELETE /api/me/keys/{id}":                  "旧版 key 由网页管理",
	"POST /api/me/tokens":                       "新令牌的明文不进模型上下文，在网页上生成",
	"GET /api/me/npm-tokens":                    "旧版 npm 令牌由网页管理",
	"POST /api/me/npm-tokens":                   "旧版 npm 令牌由网页管理，明文不进模型上下文",
	"DELETE /api/me/npm-tokens/{id}":            "旧版 npm 令牌由网页管理",
	"GET /api/settings":                         "网页读取站点设置",
	"GET /api/connect":                          "网页上的接入指南",
	"GET /api/dsh/whoami":                       "接入插件自己的协议入口，Agent 用 ee_me.whoami",
	"GET /api/dsh/catalog":                      "接入插件自己的协议入口",
	"GET /api/dsh/brand/logo":                   "企业 logo 图片，不适合给模型",
	"GET /api/dsh/skills/{name}":                "接入插件自己的协议入口",
	"GET /api/dsh/releases":                     "员工下载页读取版本",
	"GET /dl/{target}/{file}":                   "安装包二进制下载，不适合给模型",
	"GET /dl/connect/{file}":                    "接入插件 tarball 下载，不适合给模型",
	"/mcp":                                      "MCP 协议入口",
	"/npm/":                                     "npm 协议入口",
	"GET /index/dsh-index.json":                 "DSH 插件索引协议入口",
	"GET /setup.sh":                             "安装脚本",
	"GET /setup.ps1":                            "安装脚本",
	"GET /v1/models":                            "模型网关协议入口",
	"POST /v1/chat/completions":                 "模型网关协议入口",
	"POST /api/me/packages":                     "网页上传；Agent 用 ee_plugins.upload（经 /api/agent/packages）",
	"POST /api/me/packages/check":               "网页发布前预检；Agent 的 upload 会直接发布，暂不单独暴露检查工具",

	"GET /api/me/welcome":            "网页登录后的落点判断",
	"PUT /api/me/welcome":            "网页「开始使用」页的「以后再说」",
	"POST /api/me/get-started":       "接入码用来把 DSH 的设备登录与网页对上，Agent 不能替自己发起授权",
	"GET /api/me/get-started/{code}": "网页「开始使用」页轮询接入进度",

	"GET /api/kb":                      "知识库在网页上管理；DSH 里检索用 kb__ 工具",
	"POST /api/kb":                     "建知识库在网页上做",
	"PUT /api/kb/settings":             "「知识问答」的设置在网页上改",
	"POST /api/kb/search":              "网页上的试搜；DSH 里用 kb__search",
	"GET /api/kb/queries":              "检索记录在网页上看",
	"GET /api/kb/{id}":                 "知识库在网页上管理；DSH 里用 kb__browse",
	"PATCH /api/kb/{id}":               "知识库的范围与维护者只在网页上由人修改",
	"DELETE /api/kb/{id}":              "删除知识库只在网页上做",
	"POST /api/kb/{id}/docs":           "上传文档在网页上做",
	"POST /api/kb/{id}/docs/check":     "上传前查同名文档，网页上传时用",
	"GET /api/kb/{id}/gaps":            "维护者在网页上看的待补文档",
	"POST /api/kb/{id}/gaps/ignore":    "待补文档的忽略由维护者在网页上做",
	"POST /api/kb-docs/{id}/reconvert": "按新的表头设置重新转换，在网页上由维护者做",
	"GET /api/kb-docs/{id}":            "DSH 里读原文用 kb__read",
	"PATCH /api/kb-docs/{id}":          "改文档只在网页上做，不让模型被文档里的内容诱导去改知识库",
	"DELETE /api/kb-docs/{id}":         "删文档只在网页上做",
	"POST /api/kb-docs/{id}/file":      "上传文档在网页上做",
	"GET /api/kb-docs/{id}/original":   "原件二进制下载，不适合给模型",

	"POST /api/admin/menus/reset":     "全局破坏性操作",
	"GET /api/admin/upstream/export":  "离线包导出必须在网页上做",
	"POST /api/admin/upstream/import": "离线包导入必须在网页上做",

	// 按员工本人的身份调用（user_identity.go）：凭据和签名密钥都不经过模型
	"GET /.well-known/jwks.json":                            "ee_jwt 的验签公钥，给企业系统读",
	"PUT /api/me/connections/{id}/credential":               "员工自己的凭据只在网页上填，不经过模型",
	"DELETE /api/me/connections/{id}/credential":            "员工自己的凭据只在网页上管，不经过模型",
	"GET /api/admin/connections/{id}/credentials":           "谁填了凭据只在网页上看",
	"DELETE /api/admin/connections/{id}/credentials/{user}": "收回员工凭据只在网页上做",
	"GET /api/admin/ee-jwt":                                 "签名密钥只在网页上看",
	"POST /api/admin/ee-jwt/rotate":                         "轮换签名密钥只在网页上做",
}

func init() {
	agentExcluded["POST /api/admin/extensions/inspect"] = "安装扩展要管理员在网页上看过清单再确认；检查扩展用命令行 dsh-ee extension check"
}

// agentPending 是还没开放、等后续阶段的路由（管理侧工具）。
// 开放一条就把它从这里删掉、在 agentSpecs 里加上；测试会卡住漏掉的。
var agentPending = map[string]string{}

func init() {
	for _, p := range []string{
		"GET /api/admin/users", "POST /api/admin/users", "PATCH /api/admin/users/{id}", "PUT /api/admin/users/{id}/roles", "PATCH /api/admin/users/{id}/quota",
		"GET /api/admin/permissions", "POST /api/admin/permissions", "DELETE /api/admin/permissions/{code}",
		"GET /api/admin/roles", "POST /api/admin/roles", "PATCH /api/admin/roles/{id}", "DELETE /api/admin/roles/{id}",
		"GET /api/admin/menus", "POST /api/admin/menus", "PUT /api/admin/menus/order", "PATCH /api/admin/menus/{id}", "DELETE /api/admin/menus/{id}",
		"GET /api/admin/providers", "POST /api/admin/providers", "PATCH /api/admin/providers/{id}", "DELETE /api/admin/providers/{id}", "POST /api/admin/providers/{id}/check",
		"GET /api/admin/models", "POST /api/admin/models", "PATCH /api/admin/models/{id}", "DELETE /api/admin/models/{id}",
		"GET /api/admin/keys", "POST /api/admin/keys", "DELETE /api/admin/keys/{id}",
		"GET /api/admin/tokens", "POST /api/admin/tokens", "POST /api/admin/tokens/revoke",
		"PUT /api/admin/settings",
		"GET /api/admin/auth/ldap", "PUT /api/admin/auth/ldap", "POST /api/admin/auth/ldap/test", "POST /api/admin/auth/ldap/sync",
		"PATCH /api/hub/{id}", "DELETE /api/hub/{id}",
		"GET /api/admin/hub/review", "POST /api/admin/hub/review", "POST /api/admin/hub/scope-review", "PUT /api/admin/plugin-policies/{name...}",
		"POST /api/admin/connections/test", "POST /api/admin/connections/{id}/sync", "PATCH /api/admin/connections/{id}/tools/{name}",
		"GET /api/admin/connect", "POST /api/admin/connect/publish", "PUT /api/admin/brand/logo",
		"GET /api/admin/dsh-releases", "POST /api/admin/dsh-releases/check", "POST /api/admin/dsh-releases/{id}/mirror", "POST /api/admin/dsh-releases/{id}/recommend", "DELETE /api/admin/dsh-releases/{id}",
		"GET /api/admin/stats", "GET /api/admin/audit",
		"GET /api/admin/packages", "GET /api/admin/packages/{name...}", "PATCH /api/admin/packages/{name...}",
		"GET /api/admin/review", "POST /api/admin/review", "GET /api/admin/tarball",
		"GET /api/admin/registry", "POST /api/admin/registry/check", "GET /api/admin/npm-tokens", "DELETE /api/admin/npm-tokens/{id}",
		"GET /api/admin/upstream/feed", "POST /api/admin/upstream/sync", "GET /api/admin/upstream/jobs", "GET /api/admin/upstream/jobs/{id}", "POST /api/admin/upstream/check-updates",
	} {
		agentPending[p] = "管理侧工具，阶段 C 开放"
	}
}

// ---- 请求上下文：这次操作经由 Agent ----

type agentCtxKey struct{}

type agentInfo struct {
	Principal *Principal
	Tool      string
}

func withAgent(ctx context.Context, p *Principal, tool string) context.Context {
	return context.WithValue(ctx, agentCtxKey{}, &agentInfo{Principal: p, Tool: tool})
}

func agentFrom(ctx context.Context) (*agentInfo, bool) {
	i, ok := ctx.Value(agentCtxKey{}).(*agentInfo)
	return i, ok
}

// agentOn 判断这个主体能不能用 Agent：令牌带 agent 范围，且系统设置没关总开关。
func (s *Server) agentOn(ctx context.Context, p *Principal) bool {
	if p == nil || !p.Scopes.Has(rbac.ScopeAgent) {
		return false
	}
	set, err := s.appSettings(ctx)
	return err == nil && set.Connect.AgentEnabled
}

// requireAgent 认证接入插件的请求（令牌带 agent 范围），用于插件在本机代员工完成的操作，例如上传插件包。
func (s *Server) requireAgent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			writeErr(w, http.StatusUnauthorized, "请先登录")
			return
		}
		p, err := s.resolveBearer(r.Context(), token, clientIP(r), r.Header.Get("X-DshEE-Client"), r.Header.Get("X-DshEE-Host"))
		if err != nil {
			if be, ok := err.(bearerError); ok {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "登录已失效，请重新登录", "code": string(be)})
			} else {
				s.internalErr(w, r, err)
			}
			return
		}
		if !s.agentOn(r.Context(), p) {
			writeErr(w, http.StatusForbidden, "Agent 功能未开启，或这个令牌没有 agent 范围")
			return
		}
		ctx := withAgent(withUser(r.Context(), p.User, p.Access), p, "ee_plugins.upload")
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ---- 工具目录 ----

type agentAction struct {
	spec  *agentSpec
	route *route
}

type agentTool struct {
	name    string
	doc     string
	actions []*agentAction
}

// agentToolDocs 是每个工具的总述，也决定工具的出现顺序。
var agentToolDocs = []struct{ name, doc string }{
	{"ee_me", "我的账号、额度与用量。"},
	{"ee_tokens", "我的接入令牌。新令牌的明文不经过这里，请到网页「我的令牌」生成。"},
	{"ee_hub", "能力中心：技能、提示词、微应用与企业连接。分享给同事用 create；写好的内容会进入审核。"},
	{"ee_plugins", "插件：浏览插件市场、我发布的插件、上传本地插件包、申请引入社区插件。"},
	{"ee_connections", "企业连接：我能用哪些内部系统及其工具，没权限时提交使用申请。"},
	{"ee_records", "我的使用记录：模型调用与企业连接调用。"},
	{"ee_doctor", "服务端接入诊断。本机检查用 ee_local_doctor（接入插件提供）。"},
	{"ee_users", "【管理模式】用户与机器人账号：查询、新建（LDAP 账号或机器人，本地密码账号请在网页建）、停用、设角色、设额度。"},
	{"ee_roles", "【管理模式】角色与权限点：查看、新建、改权限、删除。"},
	{"ee_menus", "【管理模式】网页菜单：查看、新建、改名 / 改显隐、调整顺序、删除。"},
	{"ee_models", "【管理模式】模型接入：上游供应商（接入新供应商要 API key，请在网页上做）与对外模型的查看、启停、检测、上架、删除。"},
	{"ee_admin_tokens", "【管理模式】全员令牌：查看、吊销。下发新令牌明文不经过这里，请在网页「令牌管理」做。"},
	{"ee_review", "【管理模式】审核：技能 / 提示词 / 微应用与插件的待审队列、通过或驳回。"},
	{"ee_hub_admin", "【管理模式】能力中心管理：改任一能力的范围、下发方式、上下架，删除，设插件策略。企业连接的调用日志在 ee_hub 的 detail 里。"},
	{"ee_connections_admin", "【管理模式】企业连接：试连、同步工具、开关工具与按角色授权。"},
	{"ee_registry", "【管理模式】npm 源与上游：源状态、插件库、同步上游、检查更新、任务进度。"},
	{"ee_settings", "【管理模式】系统设置、品牌与外观（DSH 名称、首屏标题、配色）、DSH 接入插件发布、LDAP 配置（绑定密码不经过这里）。"},
	{"ee_audit", "【管理模式】审计日志查询。"},
	{"ee_stats", "【管理模式】运营统计：用量、活跃、安装。"},
}

func buildAgentTools(routes []route) []*agentTool {
	byTool := map[string]*agentTool{}
	var out []*agentTool
	for _, d := range agentToolDocs {
		t := &agentTool{name: d.name, doc: d.doc}
		byTool[d.name] = t
		out = append(out, t)
	}
	for i := range routes {
		sp := routes[i].agent
		if sp == nil {
			continue
		}
		t := byTool[sp.Tool]
		if t == nil {
			panic("agentSpec 的工具没有登记在 agentToolDocs：" + sp.Tool)
		}
		t.actions = append(t.actions, &agentAction{spec: sp, route: &routes[i]})
	}
	return out
}

// usable 判断主体能否执行这个动作：令牌范围与账号权限求交之后，满足路由要求的任一权限点。
func (a *agentAction) usable(p *Principal) bool {
	if a.spec.Admin && !p.AdminLive {
		return false
	}
	if a.spec.Show != nil && !a.spec.Show(p) {
		return false
	}
	if len(a.route.perms) == 0 {
		return true
	}
	return p.Access.Has(a.route.perms...)
}

func (t *agentTool) visible(p *Principal) []*agentAction {
	var out []*agentAction
	for _, a := range t.actions {
		if a.usable(p) {
			out = append(out, a)
		}
	}
	return out
}

func paramSchema(pr agentParam) map[string]any {
	if pr.Schema != nil {
		return pr.Schema
	}
	m := map[string]any{"type": pr.Type}
	if pr.Desc != "" {
		m["description"] = pr.Desc
	}
	if len(pr.Enum) > 0 {
		m["enum"] = pr.Enum
	}
	if pr.Type == "array" {
		item := pr.Items
		if item == "" {
			item = "string"
		}
		m["items"] = map[string]any{"type": item}
	}
	return m
}

// toolDef 生成 tools/list 里的一项；没有可用 action 时返回 nil。
func (t *agentTool) toolDef(p *Principal) map[string]any {
	acts := t.visible(p)
	if len(acts) == 0 {
		return nil
	}
	var desc strings.Builder
	desc.WriteString(t.doc)
	desc.WriteString("\n用 action 选择操作：")
	names := make([]string, 0, len(acts))
	props := map[string]any{}
	meta := map[string]any{}
	readOnly, danger := true, false
	for _, a := range acts {
		sp := a.spec
		names = append(names, sp.Action)
		var req, opt []string
		for _, pr := range sp.Params {
			if _, ok := props[pr.Name]; !ok {
				props[pr.Name] = paramSchema(pr)
			}
			if pr.Required {
				req = append(req, pr.Name)
			} else {
				opt = append(opt, pr.Name)
			}
		}
		fmt.Fprintf(&desc, "\n- %s：%s", sp.Action, sp.Summary)
		if len(req) > 0 {
			desc.WriteString("；必填 " + strings.Join(req, ","))
		}
		if len(opt) > 0 {
			desc.WriteString("；可选 " + strings.Join(opt, ","))
		}
		if sp.Effect != effRead {
			readOnly = false
			meta[sp.Action] = map[string]string{"e": string(sp.Effect), "zh": sp.Summary, "en": sp.En}
		}
		if sp.Effect == effDanger {
			danger = true
		}
	}
	if danger {
		props["confirm"] = map[string]any{"type": "string", "description": "危险操作的确认码。先不带它调用得到预览，把预览原样告诉用户并得到同意后，带上返回的 confirm 再调用一次"}
		desc.WriteString("\n危险操作（标注为 danger）要两步：先调用拿到 preview，原样展示给用户并得到同意，再带 confirm 调用。")
	}
	props["action"] = map[string]any{"type": "string", "enum": names}
	def := map[string]any{
		"name":        t.name,
		"description": desc.String(),
		"inputSchema": map[string]any{"type": "object", "properties": props, "required": []string{"action"}},
		"annotations": map[string]any{"readOnlyHint": readOnly},
	}
	if len(meta) > 0 {
		def["_meta"] = map[string]any{"dshee": map[string]any{"actions": meta}}
	}
	return def
}

// agentToolList 是这个主体在 tools/list 里能看到的 ee_* 工具。Agent 关闭或令牌没有 agent 范围时为空。
func (s *Server) agentToolList(ctx context.Context, p *Principal) []map[string]any {
	if !s.agentOn(ctx, p) {
		return nil
	}
	var out []map[string]any
	for _, t := range s.agentTools {
		if def := t.toolDef(p); def != nil {
			out = append(out, def)
		}
	}
	return out
}

func (s *Server) findAgentTool(name string) *agentTool {
	for _, t := range s.agentTools {
		if t.name == name {
			return t
		}
	}
	return nil
}

// ---- 调用 ----

const (
	agentConfirmTTL = 5 * time.Minute
	// 回给模型的文本上限，超出截断，避免一次响应撑爆上下文
	agentMaxText = 24000
)

func agentError(msg string) json.RawMessage { return mcp.TextResult(msg, true) }

// argsHash 是入参（去掉 confirm）规范化后的哈希；encoding/json 对 map 按键排序，所以结果稳定。
func argsHash(tool string, args map[string]any) string {
	clean := make(map[string]any, len(args))
	for k, v := range args {
		if k != "confirm" {
			clean[k] = v
		}
	}
	b, _ := json.Marshal(clean)
	sum := sha256.Sum256(append([]byte(tool+"\n"), b...))
	return hex.EncodeToString(sum[:])
}

// agentCall 执行一次 ee_* 工具调用，返回 MCP 工具结果。
func (s *Server) agentCall(r *http.Request, p *Principal, name string, raw json.RawMessage) json.RawMessage {
	ctx := r.Context()
	if !s.agentOn(ctx, p) {
		return agentError("Agent 功能未开启，或当前登录没有 agent 范围。可以在 DSH 里输入 /ee-login 重新登录。")
	}
	tool := s.findAgentTool(name)
	if tool == nil {
		return agentError("没有这个工具：" + name)
	}
	args := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return agentError("参数必须是 JSON 对象")
		}
	}
	visible := tool.visible(p)
	if len(visible) == 0 {
		return agentError("你的账号没有权限使用 " + name)
	}
	action, _ := args["action"].(string)
	var act *agentAction
	for _, a := range tool.actions {
		if a.spec.Action == action {
			act = a
		}
	}
	if act == nil {
		names := make([]string, 0, len(visible))
		for _, a := range visible {
			names = append(names, a.spec.Action)
		}
		return agentError(fmt.Sprintf("action 必须是 %s 之一", strings.Join(names, " / ")))
	}
	if !act.usable(p) {
		return agentError(fmt.Sprintf("你的账号没有权限执行 %s.%s", name, action))
	}
	sp := act.spec
	if msg := checkArgs(sp, args); msg != "" {
		return agentError(fmt.Sprintf("%s.%s 的参数不对：%s", name, action, msg))
	}
	if sp.Local {
		return agentError(fmt.Sprintf("%s.%s 要读本机文件，由 DSH 里的 dsh-ee 接入插件在本机完成；当前客户端没有处理它，请把接入插件升级到最新版本。", name, action))
	}

	if sp.Effect == effDanger {
		hash := argsHash(name, args)
		confirm, _ := args["confirm"].(string)
		if confirm == "" {
			preview, err := sp.Preview(ctx, s, p, args)
			if err != nil {
				return agentError(err.Error())
			}
			nonce := secret.Token()
			if err := s.store.CreateAgentConfirm(ctx, p.User.ID, p.TokenID, name, action, hash, secret.Hash(nonce), agentConfirmTTL); err != nil {
				s.log.Error("写入确认码失败", "err", err)
				return agentError("服务器内部错误")
			}
			return jsonText(map[string]any{
				"confirm_required": true,
				"confirm":          nonce,
				"preview":          preview,
				"expires_in":       int(agentConfirmTTL.Seconds()),
				"next":             "把 preview 原样告诉用户，得到同意后，用相同参数再调用一次并带上 confirm。",
			}, false)
		}
		ok, err := s.store.ConsumeAgentConfirm(ctx, p.User.ID, p.TokenID, name, action, hash, secret.Hash(confirm))
		if err != nil {
			s.log.Error("校验确认码失败", "err", err)
			return agentError("服务器内部错误")
		}
		if !ok {
			return agentError("确认码无效、已经用过、已过期，或与这次的参数不一致。请不带 confirm 重新调用，拿到新的预览。")
		}
	}
	return s.agentForward(r, p, act, args)
}

func jsonText(v any, isErr bool) json.RawMessage {
	b, _ := json.Marshal(v)
	return mcp.TextResult(string(b), isErr)
}

// checkArgs 校验入参：必填、类型、枚举，并拒绝未声明的参数（模型多半是拼错了名字，早点告诉它）。
func checkArgs(sp *agentSpec, args map[string]any) string {
	known := map[string]*agentParam{}
	for i := range sp.Params {
		known[sp.Params[i].Name] = &sp.Params[i]
	}
	for k := range args {
		if k == "action" {
			continue
		}
		if k == "confirm" && sp.Effect == effDanger {
			continue
		}
		if known[k] == nil {
			names := make([]string, 0, len(known))
			for n := range known {
				names = append(names, n)
			}
			sort.Strings(names)
			return fmt.Sprintf("没有参数 %q，可用参数：%s", k, strings.Join(names, ", "))
		}
	}
	for _, pr := range sp.Params {
		v, present := args[pr.Name]
		if !present || v == nil {
			if pr.Required {
				return "缺少必填参数 " + pr.Name
			}
			continue
		}
		if msg := checkType(pr, v); msg != "" {
			return msg
		}
	}
	return ""
}

func checkType(pr agentParam, v any) string {
	typ := pr.Type
	if pr.Schema != nil {
		typ, _ = pr.Schema["type"].(string)
	}
	switch typ {
	case "string":
		s, ok := v.(string)
		if !ok {
			return pr.Name + " 必须是字符串"
		}
		if pr.Required && strings.TrimSpace(s) == "" {
			return pr.Name + " 不能为空"
		}
		if len(pr.Enum) > 0 && !slices.Contains(pr.Enum, s) {
			return fmt.Sprintf("%s 必须是 %s 之一", pr.Name, strings.Join(pr.Enum, " / "))
		}
	case "integer":
		f, ok := v.(float64)
		if !ok || f != float64(int64(f)) {
			return pr.Name + " 必须是整数"
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return pr.Name + " 必须是布尔值"
		}
	case "object":
		if _, ok := v.(map[string]any); !ok {
			return pr.Name + " 必须是对象"
		}
	case "array":
		if _, ok := v.([]any); !ok {
			return pr.Name + " 必须是数组"
		}
	}
	return ""
}

// recorder 收集处理函数的响应。
type recorder struct {
	header http.Header
	status int
	body   []byte
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
}
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.body = append(r.body, b...)
	return len(b), nil
}

// agentForward 把入参拼成请求，交给路由表里的处理函数。
func (s *Server) agentForward(orig *http.Request, p *Principal, act *agentAction, args map[string]any) json.RawMessage {
	sp, rt := act.spec, act.route
	method, path, _ := strings.Cut(rt.pattern, " ")
	pathValues := map[string]string{}
	query := url.Values{}
	body := map[string]any{}
	for _, pr := range sp.Params {
		v, ok := args[pr.Name]
		if (!ok || v == nil) && pr.Default != "" {
			v, ok = pr.Default, true
		}
		if !ok || v == nil {
			continue
		}
		in := pr.In
		if in == "" {
			if method == http.MethodGet || method == http.MethodDelete {
				in = "query"
			} else {
				in = "body"
			}
		}
		field := pr.As
		if field == "" {
			field = pr.Name
		}
		switch in {
		case "path":
			pathValues[field] = fmt.Sprint(v)
		case "query":
			query.Set(field, fmt.Sprint(v))
		case "body":
			body[field] = v
		}
	}
	if sp.Body != nil {
		b, err := sp.Body(orig.Context(), s, p, args)
		if err != nil {
			return agentError(err.Error())
		}
		body = b
	}
	for k, v := range pathValues {
		path = strings.NewReplacer("{"+k+"...}", url.PathEscape(v), "{"+k+"}", url.PathEscape(v)).Replace(path)
	}
	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	rd := strings.NewReader("")
	if method != http.MethodGet && method != http.MethodDelete {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	ctx := withAgent(withUser(orig.Context(), p.User, p.Access), p, sp.key())
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return agentError("内部错误：" + err.Error())
	}
	req.RemoteAddr = orig.RemoteAddr
	// 内部转发仍是一次 HTTP 请求：保留 Host/TLS 与代理头，
	// 否则 publicBase 会生成 "http://" 这种没有 host 的绝对链接。
	req.Host = orig.Host
	req.TLS = orig.TLS
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		if v := orig.Header.Values(h); len(v) > 0 {
			req.Header[h] = v
		}
	}
	if rd.Len() > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range pathValues {
		req.SetPathValue(k, v)
	}
	rec := &recorder{header: http.Header{}}
	rt.inner(s).ServeHTTP(rec, req)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	switch {
	case rec.status >= 500:
		return agentError("服务器内部错误，请稍后再试")
	case rec.status >= 400:
		return agentError(errorText(rec.body, rec.status))
	}
	if len(rec.body) == 0 {
		return mcp.TextResult("已完成", false)
	}
	var out any = json.RawMessage(rec.body)
	if sp.Shape != nil {
		out = sp.Shape(p, args, rec.body)
	}
	b, _ := json.Marshal(out)
	text := string(b)
	if r := []rune(text); len(r) > agentMaxText {
		text = string(r[:agentMaxText]) + "…（内容过长已截断，请缩小范围或加筛选条件）"
	}
	return mcp.TextResult(text, false)
}

// errorText 取处理函数写的错误提示（网页上也是这句中文）。
func errorText(body []byte, status int) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return e.Error
	}
	if t := strings.TrimSpace(string(body)); t != "" {
		if r := []rune(t); len(r) > 300 {
			t = string(r[:300])
		}
		return t
	}
	return fmt.Sprintf("请求失败（%d）", status)
}
