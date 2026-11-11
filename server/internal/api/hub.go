package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hikariming/dsh-ee/server/internal/extension"
	"github.com/hikariming/dsh-ee/server/internal/rbac"
	"github.com/hikariming/dsh-ee/server/internal/registry"
	"github.com/hikariming/dsh-ee/server/internal/store"
)

// 能力中心：技能、提示词、微应用与企业连接（插件仍走 npm 源，见 catalog.go；这里只管它们的可用范围与下发方式）。

// slug 也是 DSH 里的技能名（/名字），规则与上游 skill 名一致：kebab-case。
var slugRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

const maxSlug = 48

// assetSpec 是各类资产版本的内容，按 kind 取用其中的字段。
type assetSpec struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	// 技能：什么时候该用（给模型的路由提示）
	WhenToUse string `json:"when_to_use,omitempty"`
	// 技能、提示词：Markdown 正文
	Body string `json:"body,omitempty"`
	// 技能：是否允许模型自己加载（否则只能用 /名字 调用）
	ModelInvocable *bool `json:"model_invocable,omitempty"`

	// 微应用：人设与工作方式、示例问题、依赖的能力
	Persona     string   `json:"persona,omitempty"`
	Starters    []string `json:"starters,omitempty"`
	Skills      []string `json:"skills,omitempty"`
	Connections []string `json:"connections,omitempty"`
	Plugins     []string `json:"plugins,omitempty"`
	// 微应用：绑定的知识库（标识），指令里会带上查知识库的规则
	Knowledge []string `json:"knowledge,omitempty"`
	// 微应用：在 DSH 里显示的图标（一个 emoji，留空用名字首字）
	Icon string `json:"icon,omitempty"`
	// 微应用：点开时怎么开始 —— "new"（每次新会话，默认）或 "resume"（回到上次的会话）
	Launch string `json:"launch,omitempty"`
	// 微应用：DSH 右侧栏里的业务工作台（可选）
	Workbench *appWorkbench `json:"workbench,omitempty"`
	// 微应用：样板，出现在「造 → 从样板复制」里（只有管理员能设）
	Template bool `json:"template,omitempty"`
	// 微应用：自带的数据表（app_data.go）
	Tables []appTable `json:"tables,omitempty"`
	// 微应用：网页版，能用的人可以在 /a/<标识> 打开工作台（app_web.go）
	Web bool `json:"web,omitempty"`
	// 微应用：「它能帮你做什么」的要点，最多 3 条
	Highlights []string `json:"highlights,omitempty"`

	// 连接：mcp（上游 MCP 服务地址）或 openapi（接口根地址 + 文档）
	Type         string    `json:"type,omitempty"`
	URL          string    `json:"url,omitempty"`
	Auth         *connAuth `json:"auth,omitempty"`
	OpenAPI      string    `json:"openapi,omitempty"`
	Instructions string    `json:"instructions,omitempty"`
	TimeoutSec   int       `json:"timeout_seconds,omitempty"`
	// 连接：员工身份（ee_jwt / user_secret），和上面的共享凭据并存；见 user_identity.go
	UserAuth *connUserAuth `json:"user_auth,omitempty"`
	// 连接：由扩展清单装出来的（声明式扩展）；见 extension_install.go
	Extension *connExtension `json:"extension,omitempty"`
}

// connAuth 是连接上游的认证方式；凭据本身单独加密存放，不在版本内容里。
type connAuth struct {
	// none / bearer（Authorization: Bearer <凭据>）/ header（自定义请求头）
	Type   string `json:"type"`
	Header string `json:"header,omitempty"`
}

func limitRunes(s string, n int) bool { return len([]rune(s)) <= n }

// validateSpec 检查并规范化版本内容，返回给提交人看的错误。
func validateSpec(kind string, sp *assetSpec) string {
	if kind == store.KindConnection && sp.Extension != nil {
		// 扩展：名字、地址、凭据、工具都由清单决定，先照清单把内容定下来
		if msg := bindExtension(sp); msg != "" {
			return msg
		}
	}
	sp.Title = strings.TrimSpace(sp.Title)
	sp.Description = strings.TrimSpace(sp.Description)
	if sp.Title == "" || !limitRunes(sp.Title, 64) {
		return "标题必填，最多 64 个字符"
	}
	if !limitRunes(sp.Description, 1000) {
		return "说明最多 1000 个字符"
	}
	switch kind {
	case store.KindSkill, store.KindPrompt:
		sp.Body = strings.TrimSpace(sp.Body)
		if sp.Body == "" {
			return "正文不能为空"
		}
		if len(sp.Body) > 64<<10 {
			return "正文不能超过 64 KB"
		}
		if sp.Description == "" {
			return "说明必填：DSH 靠它判断什么时候用这项能力"
		}
		if !limitRunes(sp.WhenToUse, 500) {
			return "适用场景最多 500 个字符"
		}
		if kind == store.KindPrompt {
			sp.ModelInvocable, sp.WhenToUse = nil, ""
		}
		sp.Persona, sp.Starters, sp.Skills, sp.Connections, sp.Plugins, sp.Knowledge = "", nil, nil, nil, nil, nil
		sp.Icon, sp.Launch, sp.Workbench, sp.Template, sp.Tables, sp.Web, sp.Highlights = "", "", nil, false, nil, false, nil
		sp.Type, sp.URL, sp.Auth, sp.UserAuth, sp.OpenAPI, sp.Instructions = "", "", nil, nil, "", ""
		sp.Extension = nil
	case store.KindApp:
		sp.Persona = strings.TrimSpace(sp.Persona)
		if sp.Persona == "" {
			return "人设与工作方式不能为空"
		}
		if len(sp.Persona) > 32<<10 {
			return "人设与工作方式不能超过 32 KB"
		}
		if sp.Description == "" {
			return "说明必填"
		}
		sp.Icon = strings.TrimSpace(sp.Icon)
		if !limitRunes(sp.Icon, 8) {
			return "图标最多 8 个字符"
		}
		sp.Launch = strings.TrimSpace(sp.Launch)
		if sp.Launch != "" && sp.Launch != "new" && sp.Launch != "resume" {
			return "打开方式只能是 new / resume"
		}
		clean := func(xs []string, max int, valid func(string) bool, what string) ([]string, string) {
			out := []string{}
			for _, x := range xs {
				x = strings.TrimSpace(x)
				if x == "" || slices.Contains(out, x) {
					continue
				}
				if !valid(x) {
					return nil, what + " " + x + " 格式不对"
				}
				out = append(out, x)
			}
			if len(out) > max {
				return nil, what + "最多 " + strconv.Itoa(max) + " 个"
			}
			return out, ""
		}
		var msg string
		if sp.Starters, msg = clean(sp.Starters, 6, func(s string) bool { return limitRunes(s, 200) }, "示例问题"); msg != "" {
			return msg
		}
		if sp.Highlights, msg = clean(sp.Highlights, 3, func(s string) bool { return limitRunes(s, 60) }, "要点"); msg != "" {
			return msg
		}
		if len(sp.Highlights) == 0 {
			sp.Highlights = nil
		}
		if sp.Skills, msg = clean(sp.Skills, 20, slugRe.MatchString, "技能"); msg != "" {
			return msg
		}
		if sp.Connections, msg = clean(sp.Connections, 20, slugRe.MatchString, "连接"); msg != "" {
			return msg
		}
		if sp.Plugins, msg = clean(sp.Plugins, 20, registry.ValidName, "插件"); msg != "" {
			return msg
		}
		if sp.Knowledge, msg = clean(sp.Knowledge, 20, slugRe.MatchString, "知识库"); msg != "" {
			return msg
		}
		if msg := validateTables(sp.Tables); msg != "" {
			return "数据表" + msg
		}
		if sp.Workbench != nil {
			if msg := validateWorkbench(sp.Workbench, sp.Connections, sp.Tables); msg != "" {
				return msg
			}
			if msg := checkHomeWidgets(sp.Workbench); msg != "" {
				return msg
			}
		}
		if sp.Web && !webEnabled(sp) {
			return "开放网页版需要工作台里至少有一个业务面板（panel）"
		}
		sp.Body, sp.WhenToUse, sp.ModelInvocable = "", "", nil
		sp.Type, sp.URL, sp.Auth, sp.UserAuth, sp.OpenAPI, sp.Instructions = "", "", nil, nil, "", ""
		sp.Extension = nil
	case store.KindConnection:
		if sp.Type != "mcp" && sp.Type != "openapi" {
			return "连接类型只能是 mcp 或 openapi"
		}
		sp.URL = strings.TrimSpace(sp.URL)
		if sp.Type == "openapi" && sp.URL == "" && strings.TrimSpace(sp.OpenAPI) != "" {
			// 没填根地址时用文档里 servers 的第一项，在导入时补上
		} else if u, err := url.Parse(sp.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return "地址必须是 http(s):// 开头的完整地址，且不能带账号密码"
		}
		if sp.Auth == nil {
			sp.Auth = &connAuth{Type: "none"}
		}
		switch sp.Auth.Type {
		case "none", "bearer":
			sp.Auth.Header = ""
		case "header":
			sp.Auth.Header = strings.TrimSpace(sp.Auth.Header)
			if !headerNameRe.MatchString(sp.Auth.Header) {
				return "自定义请求头的名字不对，例如 X-API-Key"
			}
		default:
			return "认证方式只能是 none / bearer / header"
		}
		if msg := validateUserAuth(sp); msg != "" {
			return msg
		}
		if sp.Type == "openapi" && strings.TrimSpace(sp.OpenAPI) == "" {
			return "OpenAPI 连接需要接口文档（JSON 或 YAML）"
		}
		if sp.Type == "mcp" {
			sp.OpenAPI = ""
		}
		if len(sp.OpenAPI) > 4<<20 {
			return "接口文档不能超过 4 MB"
		}
		if !limitRunes(sp.Instructions, 2000) {
			return "使用说明最多 2000 个字符"
		}
		if sp.TimeoutSec < 0 || sp.TimeoutSec > 120 {
			return "超时应在 1–120 秒之间"
		}
		sp.Body, sp.WhenToUse, sp.ModelInvocable = "", "", nil
		sp.Persona, sp.Starters, sp.Skills, sp.Connections, sp.Plugins, sp.Knowledge = "", nil, nil, nil, nil, nil
		sp.Icon, sp.Launch, sp.Workbench, sp.Template = "", "", nil, false
	default:
		return "未知的能力类型"
	}
	return ""
}

var headerNameRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

var hubKinds = []string{store.KindSkill, store.KindPrompt, store.KindApp, store.KindConnection}

// ---- 当前用户与权限 ----

type viewer struct {
	user   *store.User
	access *rbac.Access
	// 这次请求能用 Agent（令牌带 agent 范围、总开关开着）；只有 DSH 目录与 MCP 用到
	agent bool
	// 账号本身有管理权限；令牌此刻开着管理模式
	canAdmin, adminLive bool
}

func viewerOf(r *http.Request) viewer { return viewer{user: currentUser(r), access: currentAccess(r)} }

func (v viewer) inScope(sc store.Scope) bool {
	return sc.Allows(v.user.ID, v.user.Department, v.access.RoleIDs)
}

// manages 判断能否管理这一类资产：连接看「管理企业连接」，其余看「管理能力与审核」。
func (v viewer) manages(kind string) bool {
	if kind == store.KindConnection {
		return v.access.Has(rbac.ConnectionsManage)
	}
	return v.access.Has(rbac.PluginsManage)
}

func (v viewer) owns(a *store.Asset) bool { return a.OwnerID != nil && *a.OwnerID == v.user.ID }

// available 是员工能看到并启用的资产：已上架、没下架、在可用范围内。
func available(v viewer, a *store.Asset) bool {
	return a.Status == store.AssetActive && a.LiveVersion > 0 && v.inScope(a.Scope)
}

// ---- 变化通知：DSH 的 MCP 长连接与技能目录轮询据此刷新 ----

type hubBroker struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
	rev  int64
}

func (b *hubBroker) subscribe() (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	if b.subs == nil {
		b.subs = map[chan struct{}]struct{}{}
	}
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// notify 告诉所有在线的 DSH：能力目录可能变了。
func (b *hubBroker) notify() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rev++
	for ch := range b.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// ---- 列表与详情 ----

type hubItem struct {
	*store.Asset
	// 线上版本的内容（列表里不含正文）；还没上架时是最新提交的版本
	Spec       *assetSpec `json:"spec"`
	Owner      string     `json:"owner"`
	OwnerName  string     `json:"owner_name"`
	Department string     `json:"department"`
	// 在我的可用范围内且已上架
	Available bool `json:"available"`
	// 在我的 DSH 里生效（自动生效，或我启用了）
	Enabled    bool `json:"enabled"`
	Subscribed bool `json:"subscribed"`
	// 近 14 天的使用次数与人数
	Uses  int `json:"uses"`
	Users int `json:"users"`
	// 连接：我能用的工具数 / 全部工具数
	Tools      int `json:"tools"`
	ToolsTotal int `json:"tools_total"`
	// 我能管理（编辑、审核、改范围）
	CanManage bool `json:"can_manage"`
}

func parseSpec(raw json.RawMessage) *assetSpec {
	var sp assetSpec
	_ = json.Unmarshal(raw, &sp)
	return &sp
}

// toolAllowed 判断用户能否用连接里的这个工具。
func toolAllowed(v viewer, t *store.ConnectionTool) bool {
	if !t.Enabled {
		return false
	}
	if len(t.Roles) == 0 {
		return true
	}
	for _, r := range v.access.RoleIDs {
		if slices.Contains(t.Roles, r) {
			return true
		}
	}
	return v.access.Super
}

// hubItems 列出当前用户能看到的资产：可用的、自己的、以及有管理权限的那几类的全部。
func (s *Server) hubItems(r *http.Request, withBody bool) ([]*hubItem, error) {
	ctx := r.Context()
	v := viewerOf(r)
	assets, err := s.store.ListAssets(ctx)
	if err != nil {
		return nil, err
	}
	var visible []*store.Asset
	var ids []int64
	for _, a := range assets {
		if available(v, a) || v.owns(a) || v.manages(a.Kind) {
			visible = append(visible, a)
			ids = append(ids, a.ID)
		}
	}
	live, err := s.store.LiveAssetVersions(ctx, ids)
	if err != nil {
		return nil, err
	}
	subs, err := s.store.Subscriptions(ctx, v.user.ID)
	if err != nil {
		return nil, err
	}
	usage, err := s.store.AssetUsage(ctx, time.Now().AddDate(0, 0, -14))
	if err != nil {
		return nil, err
	}
	users, err := s.userIndex(r)
	if err != nil {
		return nil, err
	}
	var connIDs []int64
	for _, a := range visible {
		if a.Kind == store.KindConnection {
			connIDs = append(connIDs, a.ID)
		}
	}
	tools := map[int64][]*store.ConnectionTool{}
	if len(connIDs) > 0 {
		all, err := s.store.ConnectionTools(ctx, connIDs...)
		if err != nil {
			return nil, err
		}
		for _, t := range all {
			tools[t.AssetID] = append(tools[t.AssetID], t)
		}
	}
	out := make([]*hubItem, 0, len(visible))
	for _, a := range visible {
		it := &hubItem{Asset: a, Subscribed: subs[a.ID], CanManage: v.manages(a.Kind)}
		if lv := live[a.ID]; lv != nil {
			it.Spec = parseSpec(lv.Spec)
		} else if vs, err := s.store.ListAssetVersions(ctx, a.ID); err == nil && len(vs) > 0 {
			it.Spec = parseSpec(vs[0].Spec)
		} else {
			it.Spec = &assetSpec{Title: a.Title, Description: a.Description}
		}
		if !withBody {
			it.Spec.Body, it.Spec.Persona, it.Spec.OpenAPI = "", "", ""
		}
		if x := it.Spec.Extension; x != nil && (!withBody || !it.CanManage) {
			// 清单原文和管理员填的值只给管理的人看；其他人只需要知道装的是哪个扩展的哪个版本
			it.Spec.Extension = &connExtension{ID: x.ID, Version: x.Version, Digest: x.Digest}
		}
		if u := users[derefID(a.OwnerID)]; u != nil {
			it.Owner, it.OwnerName, it.Department = u.Username, u.DisplayName, u.Department
		}
		it.Available = available(v, a)
		it.Enabled = it.Available && (a.Delivery == store.DeliveryAuto || it.Subscribed)
		it.Uses, it.Users = usage[a.ID].Uses, usage[a.ID].Users
		for _, t := range tools[a.ID] {
			it.ToolsTotal++
			if toolAllowed(v, t) {
				it.Tools++
			}
		}
		out = append(out, it)
	}
	return out, nil
}

// GET /api/hub：能力中心里的技能、提示词、微应用与连接，以及插件的可用范围与下发方式。
func (s *Server) handleHub(w http.ResponseWriter, r *http.Request) {
	items, err := s.hubItems(r, false)
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	policies, err := s.store.PluginPolicies(r.Context())
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"assets": items, "plugin_policies": policies})
}

func (s *Server) loadAsset(w http.ResponseWriter, r *http.Request) (*store.Asset, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return nil, false
	}
	a, err := s.store.GetAsset(r.Context(), id)
	if notFound(err) {
		writeErr(w, http.StatusNotFound, "能力不存在")
		return nil, false
	}
	if err != nil {
		s.internalErr(w, r, err)
		return nil, false
	}
	return a, true
}

type assetVersionItem struct {
	*store.AssetVersion
	Spec      *assetSpec `json:"spec"`
	Publisher string     `json:"publisher"`
	Reviewer  string     `json:"reviewer"`
}

// GET /api/hub/{id}
func (s *Server) handleHubDetail(w http.ResponseWriter, r *http.Request) {
	a, ok := s.loadAsset(w, r)
	if !ok {
		return
	}
	v := viewerOf(r)
	if !available(v, a) && !v.owns(a) && !v.manages(a.Kind) {
		writeErr(w, http.StatusNotFound, "能力不存在")
		return
	}
	items, err := s.hubItems(r, true)
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	var item *hubItem
	for _, it := range items {
		if it.ID == a.ID {
			item = it
		}
	}
	if item == nil {
		writeErr(w, http.StatusNotFound, "能力不存在")
		return
	}
	resp := map[string]any{"asset": item}
	if v.owns(a) || v.manages(a.Kind) {
		vs, err := s.store.ListAssetVersions(r.Context(), a.ID)
		if err != nil {
			s.internalErr(w, r, err)
			return
		}
		users, err := s.userIndex(r)
		if err != nil {
			s.internalErr(w, r, err)
			return
		}
		out := make([]assetVersionItem, len(vs))
		for i, x := range vs {
			out[i] = assetVersionItem{AssetVersion: x, Spec: parseSpec(x.Spec), Publisher: users.name(x.CreatedBy), Reviewer: users.name(x.ReviewedBy)}
			out[i].AssetVersion.Spec = nil
		}
		resp["versions"] = out
	}
	if a.Kind == store.KindConnection {
		tools, err := s.store.ConnectionTools(r.Context(), a.ID)
		if err != nil {
			s.internalErr(w, r, err)
			return
		}
		if !v.manages(a.Kind) {
			mine := []*store.ConnectionTool{}
			for _, t := range tools {
				if toolAllowed(v, t) {
					t.Operation, t.Roles = nil, nil
					mine = append(mine, t)
				}
			}
			tools = mine
		}
		resp["tools"] = tools
		resp["server_name"] = s.mcpServerName(r)
		if item.Spec != nil && item.Spec.UserAuth != nil {
			creds, err := s.store.UserCredentialInfos(r.Context(), v.user.ID)
			if err != nil {
				s.internalErr(w, r, err)
				return
			}
			resp["my_credential"] = credentialView(item.Spec.UserAuth, creds, a.ID)
		}
		if v.manages(a.Kind) {
			calls, err := s.store.RecentConnectionCalls(r.Context(), a.ID, 50)
			if err != nil {
				s.internalErr(w, r, err)
				return
			}
			resp["calls"] = calls
		}
	}
	if a.Kind == store.KindSkill || a.Kind == store.KindConnection {
		used, err := s.appsUsing(r.Context(), v, a.Kind, a.Slug)
		if err != nil {
			s.internalErr(w, r, err)
			return
		}
		resp["used_by"] = used
	}
	if a.Kind == store.KindApp {
		deps, err := s.appDeps(r.Context(), v, item.Spec)
		if err != nil {
			s.internalErr(w, r, err)
			return
		}
		resp["deps"] = deps
	}
	s.feedbackExtras(r, resp, a)
	writeJSON(w, http.StatusOK, resp)
}

// ---- 新建与提交新版本 ----

type hubCreateReq struct {
	Kind     string          `json:"kind"`
	Slug     string          `json:"slug"`
	Category string          `json:"category"`
	Spec     json.RawMessage `json:"spec"`
	Scope    *store.Scope    `json:"scope"`
	Delivery string          `json:"delivery"`
	// 连接的上游凭据（只写，不回显）
	Secret *string `json:"secret"`
}

// initialStatus 决定新版本的状态：连接由管理员维护，直接生效；可用范围只有提交人自己的
// （「自己用」）不用审核；其余在白名单模式下要审核，有审核权限的人提交的直接上架。
//
// 「自己用」免审核是安全的：应用本身不带权限，运行时以员工本人的身份调用工具，
// 能选的知识库、连接也只是他本来就能用的那些。
func (s *Server) initialStatus(r *http.Request, kind string, sc store.Scope) (string, error) {
	if kind == store.KindConnection || currentAccess(r).Has(rbac.PluginsManage) || sc.OnlyUser(currentUser(r).ID) {
		return store.StatusPublished, nil
	}
	set, err := s.appSettings(r.Context())
	if err != nil {
		return "", err
	}
	if set.AdmissionMode == "open" {
		return store.StatusPublished, nil
	}
	return store.StatusPending, nil
}

func (s *Server) decodeSpec(w http.ResponseWriter, r *http.Request, kind string, raw json.RawMessage) (*assetSpec, json.RawMessage, bool) {
	var sp assetSpec
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if len(raw) == 0 || dec.Decode(&sp) != nil {
		writeErr(w, http.StatusBadRequest, "spec 格式不对")
		return nil, nil, false
	}
	if msg := validateSpec(kind, &sp); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return nil, nil, false
	}
	if sp.Extension != nil {
		var m extension.Manifest
		_ = json.Unmarshal([]byte(sp.Extension.Manifest), &m)
		if msg := s.extensionCompat(&m); msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return nil, nil, false
		}
	}
	if !currentAccess(r).Has(rbac.PluginsManage) {
		sp.Template = false
	}
	if msg, err := s.checkWorkbenchSources(r.Context(), sp.Workbench); err != nil {
		s.internalErr(w, r, err)
		return nil, nil, false
	} else if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return nil, nil, false
	}
	b, _ := json.Marshal(sp)
	return &sp, b, true
}

// POST /api/hub
func (s *Server) handleHubCreate(w http.ResponseWriter, r *http.Request) {
	var req hubCreateReq
	if !readJSONLimit(w, r, &req, 6<<20) {
		return
	}
	v := viewerOf(r)
	if !slices.Contains(hubKinds, req.Kind) {
		writeErr(w, http.StatusBadRequest, "kind 只能是 skill / prompt / app / connection")
		return
	}
	if req.Kind == store.KindConnection && !v.access.Has(rbac.ConnectionsManage) {
		writeErr(w, http.StatusForbidden, "接入企业连接需要「管理企业连接」权限")
		return
	}
	if req.Kind != store.KindConnection && !v.access.Has(rbac.RegistryPublish) {
		writeErr(w, http.StatusForbidden, "你的账号没有发布能力的权限")
		return
	}
	req.Slug = strings.TrimSpace(req.Slug)
	if strings.HasPrefix(req.Slug, systemSkillPrefix) {
		writeErr(w, http.StatusBadRequest, "标识不能以 "+systemSkillPrefix+" 开头，这是系统技能的前缀")
		return
	}
	if !slugRe.MatchString(req.Slug) || len(req.Slug) > maxSlug {
		writeErr(w, http.StatusBadRequest, "标识只能用小写字母、数字和连字符，例如 expense-policy，最多 48 个字符")
		return
	}
	if req.Slug == kbConn || req.Slug == kbQAName {
		writeErr(w, http.StatusBadRequest, "标识 "+req.Slug+" 留给知识库使用")
		return
	}
	if req.Slug == appConn || req.Slug == dataConn {
		writeErr(w, http.StatusBadRequest, "标识 "+req.Slug+" 留给应用数据和数据中台使用")
		return
	}
	sp, raw, ok := s.decodeSpec(w, r, req.Kind, req.Spec)
	if !ok {
		return
	}
	scope := store.Scope{All: true}
	if req.Scope != nil {
		scope = req.Scope.Normalize()
	}
	delivery := store.DeliveryOptional
	if req.Delivery != "" {
		delivery = req.Delivery
	}
	if msg := s.checkDelivery(v, req.Kind, delivery); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	// 「自己用」的下发方式固定为「自选」：只有自己，自动生效没有意义
	if req.Kind != store.KindConnection && scope.OnlyUser(v.user.ID) {
		delivery = store.DeliveryOptional
	}
	status, err := s.initialStatus(r, req.Kind, scope)
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	owner := v.user.ID
	a := &store.Asset{
		Kind: req.Kind, Slug: req.Slug, Title: sp.Title, Description: sp.Description,
		Category: truncateStr(strings.TrimSpace(req.Category), 32), OwnerID: &owner, Scope: scope, Delivery: delivery,
	}
	if err := s.store.CreateAsset(r.Context(), a, raw, status, v.user.ID); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			writeErr(w, http.StatusConflict, "标识 "+req.Slug+" 已被占用")
			return
		}
		s.internalErr(w, r, err)
		return
	}
	// 自己做的能力默认在自己的 DSH 里启用，方便试用
	_ = s.store.Subscribe(r.Context(), v.user.ID, a.ID)
	resp := map[string]any{"asset": a, "status": status}
	if req.Kind == store.KindConnection {
		if req.Secret != nil {
			if err := s.saveConnSecret(r, a.ID, *req.Secret); err != nil {
				s.internalErr(w, r, err)
				return
			}
		}
		if n, err := s.syncConnectionTools(r.Context(), a.ID, viewerOf(r), s.callerBase(r)); err != nil {
			resp["sync_error"] = err.Error()
		} else {
			resp["tools"] = n
		}
	}
	s.audit(r, auditAssetCreate, req.Kind+":"+req.Slug, extensionAudit(sp, map[string]any{"id": a.ID, "status": status, "delivery": delivery}))
	s.hub.notify()
	writeJSON(w, http.StatusCreated, resp)
}

// checkDelivery：「自动生效」等于把能力推到一批人的 DSH 里，要有管理权限。
func (s *Server) checkDelivery(v viewer, kind, delivery string) string {
	if delivery != store.DeliveryAuto && delivery != store.DeliveryOptional {
		return "下发方式只能是 auto 或 optional"
	}
	if delivery == store.DeliveryAuto && !v.manages(kind) {
		return "设为「自动生效」需要管理权限"
	}
	return ""
}

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// readJSONLimit 同 readJSON，但允许更大的请求体（技能正文、OpenAPI 文档）。
func readJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(dst); err != nil {
			writeErr(w, http.StatusBadRequest, "请求体格式错误："+err.Error())
			return false
		}
		return true
	}
	writeErr(w, http.StatusUnsupportedMediaType, "请求体必须是 application/json")
	return false
}

type hubVersionReq struct {
	Spec   json.RawMessage `json:"spec"`
	Secret *string         `json:"secret"`
}

// POST /api/hub/{id}/versions：提交新版本（负责人或管理员）。
func (s *Server) handleHubNewVersion(w http.ResponseWriter, r *http.Request) {
	a, ok := s.loadAsset(w, r)
	if !ok {
		return
	}
	v := viewerOf(r)
	if !v.owns(a) && !v.manages(a.Kind) {
		writeErr(w, http.StatusForbidden, "只有负责人或管理员能提交新版本")
		return
	}
	if a.Kind == store.KindConnection && !v.manages(a.Kind) {
		writeErr(w, http.StatusForbidden, "修改企业连接需要「管理企业连接」权限")
		return
	}
	var req hubVersionReq
	if !readJSONLimit(w, r, &req, 6<<20) {
		return
	}
	sp, raw, ok := s.decodeSpec(w, r, a.Kind, req.Spec)
	if !ok {
		return
	}
	if a.Kind == store.KindApp && a.LiveVersion > 0 {
		if msg, err := s.checkAppTablesChange(r.Context(), a, sp); err != nil {
			s.internalErr(w, r, err)
			return
		} else if msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
	}
	if a.Kind == store.KindConnection && a.LiveVersion > 0 {
		if msg, err := s.checkExtensionChange(r.Context(), a, sp); err != nil {
			s.internalErr(w, r, err)
			return
		} else if msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
	}
	status, err := s.initialStatus(r, a.Kind, a.Scope)
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	ver, err := s.store.AddAssetVersion(r.Context(), a.ID, raw, status, v.user.ID)
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	// 线上展示信息跟着线上版本走：直接生效的版本、或还没有上过架的资产才更新
	if status == store.StatusPublished || a.LiveVersion == 0 {
		if err := s.store.UpdateAsset(r.Context(), a.ID, store.AssetPatch{Title: &sp.Title, Description: &sp.Description}); err != nil {
			s.internalErr(w, r, err)
			return
		}
	}
	resp := map[string]any{"version": ver, "status": status}
	if a.Kind == store.KindConnection {
		if req.Secret != nil {
			if err := s.saveConnSecret(r, a.ID, *req.Secret); err != nil {
				s.internalErr(w, r, err)
				return
			}
		}
		s.dropConnClient(a.ID)
		if n, err := s.syncConnectionTools(r.Context(), a.ID, viewerOf(r), s.callerBase(r)); err != nil {
			resp["sync_error"] = err.Error()
		} else {
			resp["tools"] = n
		}
	}
	s.audit(r, auditAssetVersion, a.Kind+":"+a.Slug, extensionAudit(sp, map[string]any{"id": a.ID, "version": ver, "status": status}))
	s.hub.notify()
	writeJSON(w, http.StatusCreated, resp)
}

type hubPatchReq struct {
	Category *string      `json:"category"`
	Scope    *store.Scope `json:"scope"`
	Delivery *string      `json:"delivery"`
	// active / unpublished
	Status *string `json:"status"`
	Note   string  `json:"note"`
}

// PATCH /api/hub/{id}：可用范围与分类（负责人或管理员）、下发方式与上下架（管理员）。
func (s *Server) handleHubPatch(w http.ResponseWriter, r *http.Request) {
	a, ok := s.loadAsset(w, r)
	if !ok {
		return
	}
	var req hubPatchReq
	if !readJSON(w, r, &req) {
		return
	}
	v := viewerOf(r)
	manage := v.manages(a.Kind)
	if !manage && !v.owns(a) {
		writeErr(w, http.StatusForbidden, "只有负责人或管理员能修改")
		return
	}
	if (req.Delivery != nil || req.Status != nil) && !manage {
		writeErr(w, http.StatusForbidden, "修改下发方式与上下架需要管理权限")
		return
	}
	p := store.AssetPatch{Category: req.Category, Delivery: req.Delivery, Status: req.Status}
	if req.Category != nil {
		c := truncateStr(strings.TrimSpace(*req.Category), 32)
		p.Category = &c
	}
	// 负责人（没有管理权限）把范围改大：新范围进入审核，通过前照旧
	var requested *store.Scope
	if req.Scope != nil {
		sc := req.Scope.Normalize()
		needReview, err := s.scopeNeedsReview(r, a, sc)
		if err != nil {
			s.internalErr(w, r, err)
			return
		}
		if needReview {
			requested = &sc
		} else {
			p.Scope = &sc
		}
	}
	if req.Delivery != nil {
		if msg := s.checkDelivery(v, a.Kind, *req.Delivery); msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
	}
	if req.Status != nil {
		if *req.Status != store.AssetActive && *req.Status != store.AssetUnpublished {
			writeErr(w, http.StatusBadRequest, "status 只能是 active 或 unpublished")
			return
		}
		if *req.Status == store.AssetUnpublished && strings.TrimSpace(req.Note) == "" {
			writeErr(w, http.StatusBadRequest, "下架需要填写原因")
			return
		}
	}
	if err := s.store.UpdateAsset(r.Context(), a.ID, p); err != nil {
		s.internalErr(w, r, err)
		return
	}
	if requested != nil {
		if err := s.store.RequestAssetScope(r.Context(), a.ID, *requested); err != nil {
			s.internalErr(w, r, err)
			return
		}
		s.audit(r, auditScopeRequest, a.Kind+":"+a.Slug, map[string]any{"id": a.ID, "scope": requested})
	} else if p.Scope != nil && a.PendingScope != nil {
		// 改成了不用审核的范围，原来的申请作废
		if err := s.store.ClearAssetScopeRequest(r.Context(), a.ID); err != nil {
			s.internalErr(w, r, err)
			return
		}
	}
	detail := map[string]any{"id": a.ID}
	if p.Scope != nil {
		detail["scope"] = p.Scope
	}
	if p.Delivery != nil {
		detail["delivery"] = *p.Delivery
	}
	if p.Status != nil {
		detail["status"] = *p.Status
		detail["note"] = truncate(req.Note, 500)
	}
	s.audit(r, auditAssetEdit, a.Kind+":"+a.Slug, detail)
	s.hub.notify()
	updated, err := s.store.GetAsset(r.Context(), a.ID)
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// scopeNeedsReview：管理员、连接、自由收录模式，以及收窄范围都直接生效；
// 负责人把范围改大（超出现在的范围）要审核。
func (s *Server) scopeNeedsReview(r *http.Request, a *store.Asset, sc store.Scope) (bool, error) {
	v := viewerOf(r)
	if a.Kind == store.KindConnection || v.manages(a.Kind) {
		return false, nil
	}
	userIn := func(id int64) bool {
		u, err := s.store.GetUser(r.Context(), id)
		if err != nil {
			return false
		}
		acc, err := s.store.UserAccess(r.Context(), id)
		if err != nil {
			return false
		}
		return a.Scope.Allows(u.ID, u.Department, acc.RoleIDs)
	}
	if sc.Within(a.Scope, userIn) {
		return false, nil
	}
	set, err := s.appSettings(r.Context())
	if err != nil {
		return false, err
	}
	return set.AdmissionMode != "open", nil
}

// DELETE /api/hub/{id}：彻底删除（管理员）。
func (s *Server) handleHubDelete(w http.ResponseWriter, r *http.Request) {
	a, ok := s.loadAsset(w, r)
	if !ok {
		return
	}
	if !viewerOf(r).manages(a.Kind) {
		writeErr(w, http.StatusForbidden, "删除需要管理权限")
		return
	}
	if err := s.store.DeleteAsset(r.Context(), a.ID); err != nil {
		s.internalErr(w, r, err)
		return
	}
	if a.Kind == store.KindApp {
		// 应用自带的数据保留 30 天再清掉
		if err := s.store.DeleteAssetAppRows(r.Context(), a.ID); err != nil {
			s.log.Warn("标记删除应用数据失败", "err", err)
		}
	}
	s.dropConnClient(a.ID)
	s.audit(r, auditAssetDelete, a.Kind+":"+a.Slug, map[string]any{"id": a.ID})
	s.hub.notify()
	w.WriteHeader(http.StatusNoContent)
}

// POST / DELETE /api/hub/{id}/subscription：在自己的 DSH 里启用 / 停用一项「自己启用」的能力。
func (s *Server) handleHubSubscribe(w http.ResponseWriter, r *http.Request) {
	a, ok := s.loadAsset(w, r)
	if !ok {
		return
	}
	v := viewerOf(r)
	if r.Method == http.MethodPost {
		if !available(v, a) {
			writeErr(w, http.StatusForbidden, "这项能力不在你的可用范围内")
			return
		}
		if err := s.store.Subscribe(r.Context(), v.user.ID, a.ID); err != nil {
			s.internalErr(w, r, err)
			return
		}
	} else if err := s.store.Unsubscribe(r.Context(), v.user.ID, a.ID); err != nil {
		s.internalErr(w, r, err)
		return
	}
	s.hub.notify()
	w.WriteHeader(http.StatusNoContent)
}

// ---- 审核 ----

type assetReviewItem struct {
	Asset    *store.Asset `json:"asset"`
	Version  int          `json:"version"`
	Spec     *assetSpec   `json:"spec"`
	Previous *assetSpec   `json:"previous"`
	// 上一个线上版本号，0 表示首次提交
	PreviousVersion int       `json:"previous_version"`
	Publisher       string    `json:"publisher"`
	PublisherName   string    `json:"publisher_name"`
	Department      string    `json:"department"`
	CreatedAt       time.Time `json:"created_at"`
}

// GET /api/admin/hub/review：待审核的技能、提示词与微应用版本。
func (s *Server) handleHubReviewQueue(w http.ResponseWriter, r *http.Request) {
	pending, err := s.store.PendingAssetVersions(r.Context())
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	users, err := s.userIndex(r)
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	out := []assetReviewItem{}
	for _, pv := range pending {
		a, err := s.store.GetAsset(r.Context(), pv.AssetID)
		if err != nil {
			continue
		}
		it := assetReviewItem{Asset: a, Version: pv.Version, Spec: parseSpec(pv.Spec), CreatedAt: pv.CreatedAt}
		if lv, err := s.store.LiveAssetVersion(r.Context(), a.ID); err == nil {
			it.Previous, it.PreviousVersion = parseSpec(lv.Spec), lv.Version
		}
		if u := users[derefID(pv.CreatedBy)]; u != nil {
			it.Publisher, it.PublisherName, it.Department = u.Username, u.DisplayName, u.Department
		}
		out = append(out, it)
	}
	scopes, err := s.scopeRequests(r, users)
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "scopes": scopes})
}

// scopeReviewItem 是一条「扩大可用范围」的申请。
type scopeReviewItem struct {
	Asset         *store.Asset `json:"asset"`
	Spec          *assetSpec   `json:"spec"`
	Scope         store.Scope  `json:"scope"`
	Requested     store.Scope  `json:"requested"`
	Publisher     string       `json:"publisher"`
	PublisherName string       `json:"publisher_name"`
	Department    string       `json:"department"`
	CreatedAt     *time.Time   `json:"created_at"`
}

func (s *Server) scopeRequests(r *http.Request, users userIndex) ([]scopeReviewItem, error) {
	assets, err := s.store.ListAssets(r.Context())
	if err != nil {
		return nil, err
	}
	out := []scopeReviewItem{}
	for _, a := range assets {
		if a.PendingScope == nil {
			continue
		}
		it := scopeReviewItem{Asset: a, Scope: a.Scope, Requested: *a.PendingScope, CreatedAt: a.ScopeRequestedAt}
		if lv, err := s.store.LiveAssetVersion(r.Context(), a.ID); err == nil {
			it.Spec = parseSpec(lv.Spec)
		} else {
			it.Spec = &assetSpec{Title: a.Title, Description: a.Description}
		}
		if u := users[derefID(a.OwnerID)]; u != nil {
			it.Publisher, it.PublisherName, it.Department = u.Username, u.DisplayName, u.Department
		}
		out = append(out, it)
	}
	return out, nil
}

type scopeReviewReq struct {
	AssetID int64  `json:"asset_id"`
	Action  string `json:"action"`
	Note    string `json:"note"`
}

// POST /api/admin/hub/scope-review：通过或驳回扩大范围的申请。
func (s *Server) handleHubScopeReview(w http.ResponseWriter, r *http.Request) {
	var req scopeReviewReq
	if !readJSON(w, r, &req) {
		return
	}
	a, err := s.store.GetAsset(r.Context(), req.AssetID)
	if notFound(err) {
		writeErr(w, http.StatusNotFound, "能力不存在")
		return
	}
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	if req.Action != "approve" && req.Action != "reject" {
		writeErr(w, http.StatusBadRequest, "action 只能是 approve 或 reject")
		return
	}
	if req.Action == "reject" && strings.TrimSpace(req.Note) == "" {
		writeErr(w, http.StatusBadRequest, "驳回需要填写原因")
		return
	}
	ok, err := s.store.ResolveAssetScope(r.Context(), a.ID, req.Action == "approve", truncate(req.Note, 1000))
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	if !ok {
		writeErr(w, http.StatusConflict, "这项能力没有待审核的范围申请")
		return
	}
	action := auditReviewApprove
	if req.Action == "reject" {
		action = auditReviewReject
	}
	s.audit(r, action, a.Kind+":"+a.Slug+"#scope", map[string]any{"requested": a.PendingScope, "note": truncate(req.Note, 500)})
	s.hub.notify()
	w.WriteHeader(http.StatusNoContent)
}

type assetReviewReq struct {
	AssetID int64  `json:"asset_id"`
	Version int    `json:"version"`
	Action  string `json:"action"`
	Note    string `json:"note"`
}

// POST /api/admin/hub/review：通过或驳回一个版本。
func (s *Server) handleHubReview(w http.ResponseWriter, r *http.Request) {
	var req assetReviewReq
	if !readJSON(w, r, &req) {
		return
	}
	a, err := s.store.GetAsset(r.Context(), req.AssetID)
	if notFound(err) {
		writeErr(w, http.StatusNotFound, "能力不存在")
		return
	}
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	var to string
	switch req.Action {
	case "approve":
		to = store.StatusPublished
	case "reject":
		to = store.StatusRejected
		if strings.TrimSpace(req.Note) == "" {
			writeErr(w, http.StatusBadRequest, "驳回需要填写原因")
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "action 只能是 approve 或 reject")
		return
	}
	ok, err := s.store.SetAssetVersionStatus(r.Context(), a.ID, req.Version, []string{store.StatusPending}, to, currentUser(r).ID, truncate(req.Note, 1000))
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	if !ok {
		writeErr(w, http.StatusConflict, "这个版本不是待审核状态")
		return
	}
	if to == store.StatusPublished && req.Version > a.LiveVersion {
		if pv, err := s.store.GetAssetVersion(r.Context(), a.ID, req.Version); err == nil {
			sp := parseSpec(pv.Spec)
			_ = s.store.UpdateAsset(r.Context(), a.ID, store.AssetPatch{Title: &sp.Title, Description: &sp.Description})
		}
	}
	action := auditReviewApprove
	if to == store.StatusRejected {
		action = auditReviewReject
	}
	s.audit(r, action, a.Kind+":"+a.Slug+"#"+strconv.Itoa(req.Version), map[string]any{"note": truncate(req.Note, 500)})
	s.hub.notify()
	w.WriteHeader(http.StatusNoContent)
}

// ---- 插件的可用范围与下发方式 ----

type pluginPolicyReq struct {
	Scope    store.Scope `json:"scope"`
	Delivery string      `json:"delivery"`
}

// PUT /api/admin/plugin-policies/{name...}
func (s *Server) handlePutPluginPolicy(w http.ResponseWriter, r *http.Request) {
	name, ok := packageName(w, r)
	if !ok {
		return
	}
	var req pluginPolicyReq
	if !readJSON(w, r, &req) {
		return
	}
	if req.Delivery != store.DeliveryAuto && req.Delivery != store.DeliveryOptional {
		writeErr(w, http.StatusBadRequest, "下发方式只能是 auto 或 optional")
		return
	}
	if _, err := s.store.GetPackage(r.Context(), name); notFound(err) {
		writeErr(w, http.StatusNotFound, "包不存在")
		return
	} else if err != nil {
		s.internalErr(w, r, err)
		return
	}
	p := store.PluginPolicy{Name: name, Scope: req.Scope.Normalize(), Delivery: req.Delivery}
	if err := s.store.SetPluginPolicy(r.Context(), p, currentUser(r).ID); err != nil {
		s.internalErr(w, r, err)
		return
	}
	s.audit(r, auditPackageEdit, name, map[string]any{"scope": p.Scope, "delivery": p.Delivery})
	s.hub.notify()
	writeJSON(w, http.StatusOK, p)
}
