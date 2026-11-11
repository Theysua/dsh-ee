package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hikariming/dsh-ee/server/internal/rbac"
	"github.com/hikariming/dsh-ee/server/internal/store"
)

// 应用编辑器（第一版 M5，docs/product-v1-tasks.md）：
//   - 实时预览：编辑器右边的工作台按还没保存的配置取数，以当前员工的身份，规则和 DSH / 网页版一样；
//   - 原料一键生成草稿：知识库 → 问答应用，连接 → 按工具清单建议面板，生成的是没保存的草稿；
//   - 原料被谁用：技能、连接、知识库的详情列出用到它的应用；
//   - 导入导出：一个应用连同依赖导出成一个文件，导入另一个 dsh-ee；文件里不带任何凭据。

// ---- 实时预览 ----

type previewReq struct {
	// 编辑已有应用时带上标识：自带的表从线上版本里读
	App    string    `json:"app"`
	Source *wbSource `json:"source"`
}

// POST /api/me/workbench-preview {app?, source}：给编辑器预览里的一个组件取数。
// 和网页版一样以员工本人的身份执行，只读工具、知识库、数据集照常检查权限；
// 还没保存的应用没有表，自带的表返回空列表并说明。
func (s *Server) handleWorkbenchPreview(w http.ResponseWriter, r *http.Request) {
	var req previewReq
	if !readJSON(w, r, &req) {
		return
	}
	src := req.Source
	if src == nil || (src.Table == "" && src.Dataset == "" && (src.Connection == "" || src.Tool == "")) {
		writeErr(w, http.StatusBadRequest, "source 要写 connection + tool、table 或 dataset 之一")
		return
	}
	v := viewerOf(r)
	ctx := r.Context()
	if src.Table != "" {
		if _, err := s.appTargetFor(ctx, v, strings.TrimSpace(req.App), src.Table); err != nil {
			var ae *appDataError
			if !errors.As(err, &ae) {
				s.internalErr(w, r, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"value": map[string]any{"total": 0, "items": []any{}}, "note": "保存并上线后，这里显示表里的数据"})
			return
		}
	}
	out, err := s.runSource(ctx, v, s.callerBase(r), strings.TrimSpace(req.App), src)
	if err != nil {
		s.writeAppErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- 原料一键生成草稿 ----

type draftReq struct {
	From string `json:"from"` // knowledge / connection
	Slug string `json:"slug"`
}

// POST /api/me/app-drafts {from, slug}：用一个知识库或连接拟一个「自己用」的应用草稿（不保存）。
// 不调用模型：知识库按文档标题和小标题拟示例问题；连接按工具清单建议面板，
// 没有必填参数的只读工具会以员工本人的身份试调一次，按返回的结构猜表格列和指标字段。
// 想让 AI 帮着写人设，可以在 DSH 里直接说「用 X 做个应用」（ee_hub 的 for_me）。
func (s *Server) handleAppDraft(w http.ResponseWriter, r *http.Request) {
	var req draftReq
	if !readJSON(w, r, &req) {
		return
	}
	v := viewerOf(r)
	var (
		slug  string
		sp    *assetSpec
		notes []string
		err   error
	)
	switch req.From {
	case "knowledge":
		slug, sp, notes, err = s.kbDraft(r.Context(), v, strings.TrimSpace(req.Slug))
	case "connection":
		slug, sp, notes, err = s.connectionDraft(r.Context(), v, s.callerBase(r), strings.TrimSpace(req.Slug))
	default:
		writeErr(w, http.StatusBadRequest, "from 只能是 knowledge 或 connection")
		return
	}
	if err != nil {
		s.writeAppErr(w, r, err)
		return
	}
	// 拟出来的东西也要过一遍校验：不合格的工作台去掉，别让作者一保存就报错
	if msg := validateSpec(store.KindApp, sp); msg != "" {
		if sp.Workbench != nil {
			sp.Workbench = nil
			notes = append(notes, "工作台没有通过校验（"+msg+"），先去掉了")
			msg = validateSpec(store.KindApp, sp)
		}
		if msg != "" {
			s.internalErr(w, r, fmt.Errorf("草稿不合格：%s", msg))
			return
		}
	}
	if notes == nil {
		notes = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"slug": s.freeSlug(r.Context(), slug), "spec": sp, "notes": notes})
}

// cut 把文字截到 n 个字以内（超出时末尾加省略号）。
func cut(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func cutSlug(s string) string {
	if len(s) > maxSlug {
		s = strings.TrimRight(s[:maxSlug], "-")
	}
	return s
}

// freeSlug 找一个还没被占用的标识：原样，或者加 -2、-3 …
func (s *Server) freeSlug(ctx context.Context, slug string) string {
	slug = cutSlug(slug)
	for i := 1; i < 50; i++ {
		try := slug
		if i > 1 {
			suffix := fmt.Sprintf("-%d", i)
			try = cutSlug(strings.TrimRight(slug[:min(len(slug), maxSlug-len(suffix))], "-") + suffix)
		}
		if _, err := s.store.GetAssetBySlug(ctx, try); errors.Is(err, store.ErrNotFound) && try != kbConn && try != kbQAName && try != appConn && try != dataConn {
			return try
		}
	}
	return slug
}

const kbDraftPersona = `你是「%s」问答助手，帮员工查「%s」里的制度、手册和资料。

- 先用知识库工具检索，再读原文作答；不要凭印象回答。
- 每个结论都标出处（文档和行号），员工可以点开原文核对。
- 知识库里查不到的，直接说查不到，并建议去问哪个部门；不要编。
- 回答先给结论，再列依据；涉及金额、期限的，原样引用原文。`

func (s *Server) kbDraft(ctx context.Context, v viewer, slug string) (string, *assetSpec, []string, error) {
	b, err := s.store.GetKBaseBySlug(ctx, slug)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !v.kbReads(b)) {
		return "", nil, nil, appErr(http.StatusNotFound, "没有知识库 %s，或你没有查看权限", slug)
	}
	if err != nil {
		return "", nil, nil, err
	}
	docs, err := s.store.ListKDocs(ctx, b.ID)
	if err != nil {
		return "", nil, nil, err
	}
	var notes, starters, titles []string
	for i, d := range docs {
		if i < 8 {
			titles = append(titles, "- "+d.Title)
		}
		if i >= 4 || len(starters) >= 4 {
			continue
		}
		heads := []string{}
		if t, err := s.store.KDocText(ctx, d.ID, 0); err == nil {
			for _, line := range strings.Split(t.Markdown, "\n") {
				if h, ok := strings.CutPrefix(strings.TrimSpace(line), "## "); ok && strings.TrimSpace(h) != "" {
					heads = append(heads, strings.Trim(strings.TrimSpace(h), "#* "))
				}
			}
		}
		if len(heads) > 0 {
			starters = append(starters, cut(fmt.Sprintf("「%s」里，%s是怎么规定的？", d.Title, heads[0]), 200))
		} else {
			starters = append(starters, cut(fmt.Sprintf("「%s」主要讲了什么？", d.Title), 200))
		}
	}
	if len(docs) == 0 {
		notes = append(notes, "这个库还没有文档，示例问题先留空，上传文档后再补")
	}
	if len(docs) > 8 {
		titles = append(titles, fmt.Sprintf("- …共 %d 篇", len(docs)))
	}
	desc := b.Description
	if desc == "" {
		desc = "回答关于「" + b.Title + "」的问题，每个结论都带出处。"
	}
	title := cut(b.Title+"问答", 64)
	widgets := []wbWidget{}
	if len(titles) > 0 {
		widgets = append(widgets, wbWidget{Type: "markdown", Title: "能问什么", Text: "这个应用只按「" + b.Title + "」里的文档回答：\n\n" + strings.Join(titles, "\n")})
	}
	if len(starters) > 0 {
		acts := []wbAction{}
		for _, q := range starters {
			acts = append(acts, wbAction{Label: cut(q, 20), Prompt: q})
		}
		widgets = append(widgets, wbWidget{Type: "actions", Title: "试试问", Actions: acts})
	}
	widgets = append(widgets, wbWidget{Type: "board", Title: "回答要点"})
	sp := &assetSpec{
		Title: title, Description: cut(desc, 1000), Icon: "📚", Launch: "new",
		Persona:    fmt.Sprintf(kbDraftPersona, title, b.Title),
		Starters:   starters,
		Highlights: []string{cut("按「"+b.Title+"」里的文档作答", 60), "每个结论都标出处，可以点开原文", "查不到就直说，不编"},
		Knowledge:  []string{b.Slug},
		Workbench: &appWorkbench{Panes: []wbPane{
			{ID: "main", Type: "panel", Title: "问答", Widgets: widgets},
			{ID: "doc", Type: "kb_doc", Title: "原文"},
		}},
	}
	return b.Slug + "-qa", sp, notes, nil
}

const connDraftPersona = `你是「%s」助手，帮员工查询和办理「%s」里的事。

- 查询用只读工具，结果用表格或要点说明，数字保留原始单位。
- 任何写操作（新建、修改、提交）先把要提交的内容列给员工确认，确认后再调用工具。
- 工具报错时说明原因和下一步怎么办，不要编造数据。`

// requiredParams 读出工具入参里的必填项。
func requiredParams(schema json.RawMessage) []string {
	var s struct {
		Required []string `json:"required"`
	}
	_ = json.Unmarshal(schema, &s)
	return s.Required
}

// guessWidget 按工具的返回猜一个组件：对象列表 → 表格，带数字的对象 → 指标卡。
func guessWidget(title string, src *wbSource, value any) *wbWidget {
	scalarKeys := func(m map[string]any) []string {
		keys := []string{}
		for k, x := range m {
			if !wbPathRe.MatchString(k) {
				continue
			}
			switch x.(type) {
			case string, float64, bool:
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		// 常见的标识、名称放前面
		slices.SortStableFunc(keys, func(a, b string) int { return rankKey(a) - rankKey(b) })
		return keys
	}
	table := func(rows string, list []any) *wbWidget {
		first, _ := list[0].(map[string]any)
		if first == nil {
			return nil
		}
		keys := scalarKeys(first)
		if len(keys) == 0 {
			return nil
		}
		cols := []wbColumn{}
		for _, k := range keys[:min(len(keys), 5)] {
			cols = append(cols, wbColumn{Field: k, Title: k})
		}
		w := &wbWidget{Type: "table", Title: title, Source: src, Rows: rows, Columns: cols}
		if len(keys) > 5 {
			for _, k := range keys[5:min(len(keys), 12)] {
				w.Detail = append(w.Detail, wbColumn{Field: k, Title: k})
			}
		}
		return w
	}
	switch x := value.(type) {
	case []any:
		if len(x) > 0 {
			return table("", x)
		}
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if list, ok := x[k].([]any); ok && len(list) > 0 && wbPathRe.MatchString(k) {
				if w := table(k, list); w != nil {
					return w
				}
			}
		}
		for _, k := range keys {
			if _, ok := x[k].(float64); ok && wbPathRe.MatchString(k) {
				return &wbWidget{Type: "metric", Title: title, Source: src, Field: k, Format: "number"}
			}
		}
	}
	return nil
}

func rankKey(k string) int {
	switch strings.ToLower(k) {
	case "id", "no", "code":
		return 0
	case "name", "title", "subject":
		return 1
	case "status", "state":
		return 2
	}
	return 3
}

func (s *Server) connectionDraft(ctx context.Context, v viewer, base, slug string) (string, *assetSpec, []string, error) {
	c, err := s.userCatalog(ctx, v)
	if err != nil {
		return "", nil, nil, err
	}
	var cn *dshConn
	for _, x := range c.conns {
		if x.asset.Slug == slug {
			cn = x
		}
	}
	if cn == nil {
		return "", nil, nil, appErr(http.StatusNotFound, "你还不能用连接 %s（没有权限或没有启用）", slug)
	}
	title := cn.spec.Title
	var tables, notes, starters []string
	var metricW, tableW []wbWidget
	actions := []wbAction{}
	tried := 0
	for _, t := range cn.tools {
		desc := strings.TrimSpace(t.Description)
		if desc == "" {
			desc = t.Name
		}
		if !t.ReadOnly {
			if len(actions) < 4 {
				actions = append(actions, wbAction{Label: cut(desc, 20), Prompt: cut(fmt.Sprintf("帮我用「%s」办理：%s。动手前先把要提交的内容给我确认。", title, desc), 1000)})
			}
			continue
		}
		if len(starters) < 4 {
			starters = append(starters, cut("帮我查一下："+desc, 200))
		}
		if req := requiredParams(t.InputSchema); len(req) > 0 {
			if len(actions) < 6 {
				actions = append(actions, wbAction{Label: cut(desc, 20), Prompt: cut(fmt.Sprintf("帮我用「%s」查询：%s。需要的条件（%s）先问我。", title, desc, strings.Join(req, "、")), 1000)})
			}
			continue
		}
		if tried >= 6 || len(metricW)+len(tableW) >= 8 {
			continue
		}
		tried++
		src := &wbSource{Connection: slug, Tool: t.Name}
		cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		out, err := s.runSource(cctx, v, base, "", src)
		cancel()
		if err != nil || out["error"] != nil || out["forbidden"] != nil {
			notes = append(notes, fmt.Sprintf("试调 %s 没拿到数据，没放进面板", t.Name))
			continue
		}
		w := guessWidget(cut(desc, 40), src, out["value"])
		if w == nil {
			notes = append(notes, fmt.Sprintf("%s 的返回看不出是列表还是数字，没放进面板", t.Name))
			continue
		}
		if w.Type == "metric" {
			metricW = append(metricW, *w)
		} else {
			tableW = append(tableW, *w)
			tables = append(tables, t.Name)
		}
	}
	widgets := append(metricW, tableW...)
	if len(actions) > 0 {
		widgets = append(widgets, wbWidget{Type: "actions", Title: "快捷操作", Actions: actions[:min(len(actions), 8)]})
	}
	if len(widgets) == 0 {
		widgets = append(widgets, wbWidget{Type: "markdown", Title: "说明", Text: "这个连接的工具都需要条件，面板里先只放 AI 看板。在左边对话里直接说要查什么。"})
	}
	widgets = append(widgets, wbWidget{Type: "board", Title: "AI 看板"})
	if len(widgets) > 12 {
		widgets = append(widgets[:11], widgets[len(widgets)-1])
	}
	if len(tables) > 0 {
		notes = append(notes, "表格的列是按 "+strings.Join(tables, "、")+" 的返回猜的，标题先用了字段名，记得改成中文")
	}
	appTitle := cut(title+"助手", 64)
	desc := cn.spec.Description
	if desc == "" {
		desc = "查询和办理「" + title + "」里的事。"
	}
	sp := &assetSpec{
		Title: appTitle, Description: cut(desc, 1000), Icon: "🧩", Launch: "new",
		Persona:     fmt.Sprintf(connDraftPersona, appTitle, title),
		Starters:    starters,
		Connections: []string{slug},
		Workbench:   &appWorkbench{Panes: []wbPane{{ID: "main", Type: "panel", Title: cut(title, 40), Widgets: widgets}}},
	}
	return slug + "-app", sp, notes, nil
}

// ---- 原料被谁用 ----

type usedByApp struct {
	ID    int64  `json:"id"`
	Slug  string `json:"slug"`
	Title string `json:"title"`
	Icon  string `json:"icon,omitempty"`
}

// appsUsing 列出线上版本里用到这项原料的应用（只列这个人看得到的）。kind 为 skill / connection / knowledge。
func (s *Server) appsUsing(ctx context.Context, v viewer, kind, slug string) ([]usedByApp, error) {
	assets, err := s.store.ListAssets(ctx)
	if err != nil {
		return nil, err
	}
	var apps []*store.Asset
	var ids []int64
	for _, a := range assets {
		if a.Kind == store.KindApp && (available(v, a) || v.owns(a) || v.manages(store.KindApp)) {
			apps = append(apps, a)
			ids = append(ids, a.ID)
		}
	}
	live, err := s.store.LiveAssetVersions(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := []usedByApp{}
	for _, a := range apps {
		lv := live[a.ID]
		if lv == nil {
			continue
		}
		sp := parseSpec(lv.Spec)
		var list []string
		switch kind {
		case store.KindSkill:
			list = sp.Skills
		case store.KindConnection:
			list = sp.Connections
		case store.RequestKnowledge:
			list = sp.Knowledge
		}
		if slices.Contains(list, slug) {
			out = append(out, usedByApp{ID: a.ID, Slug: a.Slug, Title: sp.Title, Icon: sp.Icon})
		}
	}
	return out, nil
}

// ---- 导入导出 ----

const appFileFormat = "dsh-ee-app"

// appFile 是导出的应用文件。凭据从来不在版本内容里（单独加密存放），所以这里天然不带；
// 连接的地址与接口文档只有能管理连接的人导出时才带。
type appFile struct {
	Format     string        `json:"format"`
	Version    int           `json:"version"`
	ExportedAt time.Time     `json:"exported_at"`
	App        appFileItem   `json:"app"`
	Skills     []appFileItem `json:"skills,omitempty"`
	// 连接：spec 只含类型、地址、接口文档、认证方式（不含凭据）
	Connections []appFileItem `json:"connections,omitempty"`
	Knowledge   []appFileKB   `json:"knowledge,omitempty"`
	Plugins     []string      `json:"plugins,omitempty"`
	// 工作台里用到的数据中台数据集（只有名字，导入后要在目标环境里有同名数据集）
	Datasets []string `json:"datasets,omitempty"`
}

type appFileItem struct {
	Slug        string     `json:"slug"`
	Title       string     `json:"title,omitempty"`
	Description string     `json:"description,omitempty"`
	Spec        *assetSpec `json:"spec,omitempty"`
	// 连接：导出时这个连接要配凭据（导入后需要管理员重新填）
	NeedsSecret bool `json:"needs_secret,omitempty"`
}

type appFileKB struct {
	Slug        string         `json:"slug"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Docs        []appFileKBDoc `json:"docs,omitempty"`
}

type appFileKBDoc struct {
	Title    string `json:"title"`
	Folder   string `json:"folder,omitempty"`
	Markdown string `json:"markdown"`
}

// workbenchDatasets 是工作台里用到的数据集。
func workbenchDatasets(wb *appWorkbench) []string {
	var out []string
	if wb == nil {
		return nil
	}
	for _, p := range wb.Panes {
		for _, w := range p.Widgets {
			if w.Source != nil && w.Source.Dataset != "" && !slices.Contains(out, w.Source.Dataset) {
				out = append(out, w.Source.Dataset)
			}
		}
	}
	return out
}

// GET /api/hub/{id}/export?docs=1：导出一个应用。能看到这个应用的人都能导出（和「复制为我的应用」一样）；
// 依赖的技能只带他能看到的，知识库文档只在 docs=1 且他能查这个库时带上。
func (s *Server) handleAppFileExport(w http.ResponseWriter, r *http.Request) {
	a, ok := s.loadAsset(w, r)
	if !ok {
		return
	}
	v := viewerOf(r)
	ctx := r.Context()
	if a.Kind != store.KindApp || (!available(v, a) && !v.owns(a) && !v.manages(a.Kind)) {
		writeErr(w, http.StatusNotFound, "没有这个应用")
		return
	}
	sp, err := s.latestSpec(ctx, a)
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	sp.Template = false
	f := appFile{Format: appFileFormat, Version: 1, ExportedAt: time.Now().UTC(), App: appFileItem{Slug: a.Slug, Title: sp.Title, Spec: sp},
		Plugins: sp.Plugins, Datasets: workbenchDatasets(sp.Workbench)}
	for _, slug := range sp.Skills {
		x, err := s.store.GetAssetBySlug(ctx, slug)
		if err != nil || x.Kind != store.KindSkill || (!available(v, x) && !v.owns(x) && !v.manages(x.Kind)) {
			f.Skills = append(f.Skills, appFileItem{Slug: slug})
			continue
		}
		xs, err := s.latestSpec(ctx, x)
		if err != nil {
			s.internalErr(w, r, err)
			return
		}
		f.Skills = append(f.Skills, appFileItem{Slug: slug, Title: xs.Title, Description: xs.Description, Spec: xs})
	}
	for _, slug := range sp.Connections {
		x, err := s.store.GetAssetBySlug(ctx, slug)
		if err != nil || x.Kind != store.KindConnection {
			f.Connections = append(f.Connections, appFileItem{Slug: slug})
			continue
		}
		it := appFileItem{Slug: slug, Title: x.Title, Description: x.Description}
		if v.manages(store.KindConnection) {
			xs, err := s.latestSpec(ctx, x)
			if err != nil {
				s.internalErr(w, r, err)
				return
			}
			it.Spec = &assetSpec{Title: xs.Title, Description: xs.Description, Type: xs.Type, URL: xs.URL, OpenAPI: xs.OpenAPI, Instructions: xs.Instructions, TimeoutSec: xs.TimeoutSec}
			if xs.Auth != nil {
				it.Spec.Auth = &connAuth{Type: xs.Auth.Type, Header: xs.Auth.Header}
				it.NeedsSecret = xs.Auth.Type != "none"
			}
			if xs.Extension != nil {
				// 扩展装出来的连接连同清单和管理员填的参数一起导出，导入时照清单重新装
				x := *xs.Extension
				it.Spec.Extension = &x
			}
			if xs.UserAuth != nil {
				// 员工身份的设置里没有凭据，跟着连接一起导出；员工各自的凭据不导出
				ua := *xs.UserAuth
				it.Spec.UserAuth = &ua
			}
		}
		f.Connections = append(f.Connections, it)
	}
	withDocs := r.URL.Query().Get("docs") == "1"
	for _, slug := range sp.Knowledge {
		b, err := s.store.GetKBaseBySlug(ctx, slug)
		if err != nil {
			f.Knowledge = append(f.Knowledge, appFileKB{Slug: slug})
			continue
		}
		kb := appFileKB{Slug: slug, Title: b.Title, Description: b.Description}
		if withDocs && v.kbReads(b) {
			docs, texts, err := s.store.KBaseTexts(ctx, b.ID)
			if err != nil {
				s.internalErr(w, r, err)
				return
			}
			for _, d := range docs {
				kb.Docs = append(kb.Docs, appFileKBDoc{Title: d.Title, Folder: d.Folder, Markdown: texts[d.ID]})
			}
		}
		f.Knowledge = append(f.Knowledge, kb)
	}
	s.audit(r, auditAppExport, "app:"+a.Slug, map[string]any{"id": a.ID, "docs": withDocs})
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.dsh-ee-app.json"`, a.Slug))
	writeJSON(w, http.StatusOK, f)
}

// latestSpec 是线上版本的内容，还没上线时用最新提交的版本。
func (s *Server) latestSpec(ctx context.Context, a *store.Asset) (*assetSpec, error) {
	live, err := s.store.LiveAssetVersions(ctx, []int64{a.ID})
	if err != nil {
		return nil, err
	}
	if lv := live[a.ID]; lv != nil {
		return parseSpec(lv.Spec), nil
	}
	vs, err := s.store.ListAssetVersions(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		return &assetSpec{Title: a.Title, Description: a.Description}, nil
	}
	return parseSpec(vs[0].Spec), nil
}

// importItem 是导入报告的一行。
type importItem struct {
	Kind   string `json:"kind"`
	Slug   string `json:"slug"`
	Title  string `json:"title,omitempty"`
	Result string `json:"result"` // created / reused / missing / skipped
	Note   string `json:"note,omitempty"`
}

type importReq struct {
	File json.RawMessage `json:"file"`
	// 应用的新标识（留空用文件里的，被占用时自动加后缀）
	Slug string `json:"slug"`
}

// POST /api/hub/import {file, slug?}：导入一个应用文件，导入后是导入人「自己用」的应用。
//   - 技能：同名的沿用；没有的按文件内容新建成导入人「自己用」的技能；
//   - 连接：同名的沿用；没有的，导入人能管理连接时按文件新建（范围只有自己、没有凭据，要自己补），否则记为缺少；
//   - 知识库：同名的沿用；没有的，导入人能管理知识库且文件里带了文档时新建（只有自己能查），否则记为缺少；
//   - 工作台里引用了缺少的连接或数据集的组件去掉，报告里写明去掉了几个。
func (s *Server) handleAppImport(w http.ResponseWriter, r *http.Request) {
	var req importReq
	if !readJSONLimit(w, r, &req, 16<<20) {
		return
	}
	var f appFile
	if json.Unmarshal(req.File, &f) != nil || f.Format != appFileFormat || f.App.Spec == nil {
		writeErr(w, http.StatusBadRequest, "不是 dsh-ee 导出的应用文件")
		return
	}
	if f.Version != 1 {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("文件格式版本 %d 不认识，请升级 dsh-ee", f.Version))
		return
	}
	v := viewerOf(r)
	ctx := r.Context()
	self := store.Scope{Users: []int64{v.user.ID}}
	report := []importItem{}
	add := func(it importItem) { report = append(report, it) }

	// 技能
	for _, sk := range f.Skills {
		it := importItem{Kind: store.KindSkill, Slug: sk.Slug, Title: sk.Title}
		if x, err := s.store.GetAssetBySlug(ctx, sk.Slug); err == nil {
			it.Result = "reused"
			if x.Kind != store.KindSkill {
				it.Result, it.Note = "missing", "标识已被别的能力占用"
			}
			add(it)
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			s.internalErr(w, r, err)
			return
		}
		if sk.Spec == nil || !slugRe.MatchString(sk.Slug) {
			it.Result, it.Note = "missing", "文件里没有这个技能的内容"
			add(it)
			continue
		}
		sp := *sk.Spec
		if msg := validateSpec(store.KindSkill, &sp); msg != "" {
			it.Result, it.Note = "missing", msg
			add(it)
			continue
		}
		raw, _ := json.Marshal(sp)
		owner := v.user.ID
		a := &store.Asset{Kind: store.KindSkill, Slug: sk.Slug, Title: sp.Title, Description: sp.Description, OwnerID: &owner, Scope: self, Delivery: store.DeliveryOptional}
		if err := s.store.CreateAsset(ctx, a, raw, store.StatusPublished, v.user.ID); err != nil {
			s.internalErr(w, r, err)
			return
		}
		_ = s.store.Subscribe(ctx, v.user.ID, a.ID)
		it.Result, it.Note = "created", "建成了只有你自己能用的技能"
		add(it)
	}

	// 连接
	connOK := map[string]bool{}
	for _, c := range f.Connections {
		it := importItem{Kind: store.KindConnection, Slug: c.Slug, Title: c.Title}
		if x, err := s.store.GetAssetBySlug(ctx, c.Slug); err == nil {
			if x.Kind == store.KindConnection {
				it.Result, connOK[c.Slug] = "reused", true
			} else {
				it.Result, it.Note = "missing", "标识已被别的能力占用"
			}
			add(it)
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			s.internalErr(w, r, err)
			return
		}
		if c.Spec == nil || !v.access.Has(rbac.ConnectionsManage) || !slugRe.MatchString(c.Slug) {
			it.Result, it.Note = "missing", "目标环境没有这个连接，需要管理员先接入"
			add(it)
			continue
		}
		sp := *c.Spec
		if msg := validateSpec(store.KindConnection, &sp); msg != "" {
			it.Result, it.Note = "missing", msg
			add(it)
			continue
		}
		raw, _ := json.Marshal(sp)
		owner := v.user.ID
		a := &store.Asset{Kind: store.KindConnection, Slug: c.Slug, Title: sp.Title, Description: sp.Description, OwnerID: &owner, Scope: self, Delivery: store.DeliveryAuto}
		if err := s.store.CreateAsset(ctx, a, raw, store.StatusPublished, v.user.ID); err != nil {
			s.internalErr(w, r, err)
			return
		}
		it.Result, connOK[c.Slug] = "created", true
		it.Note = "范围只有你自己"
		if c.NeedsSecret {
			it.Note += "；需要在连接设置里填凭据"
		}
		if _, err := s.syncConnectionTools(ctx, a.ID, viewerOf(r), s.callerBase(r)); err != nil {
			it.Note += "；同步工具失败：" + cut(err.Error(), 120)
		}
		add(it)
	}

	// 知识库
	for _, k := range f.Knowledge {
		it := importItem{Kind: store.RequestKnowledge, Slug: k.Slug, Title: k.Title}
		if _, err := s.store.GetKBaseBySlug(ctx, k.Slug); err == nil {
			it.Result = "reused"
			add(it)
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			s.internalErr(w, r, err)
			return
		}
		if len(k.Docs) == 0 || !v.kbManages() || !slugRe.MatchString(k.Slug) || validKBText(k.Title, k.Description) != "" {
			it.Result, it.Note = "missing", "目标环境没有这个知识库；文件里带了文档且你能管理知识库时才会新建"
			add(it)
			continue
		}
		b := &store.KBase{Slug: k.Slug, Title: k.Title, Description: k.Description, Scope: self, Maintainers: store.Maintainers{Users: []int64{v.user.ID}}}
		if err := s.store.CreateKBase(ctx, b, v.user.ID); err != nil {
			s.internalErr(w, r, err)
			return
		}
		for _, d := range k.Docs {
			title := strings.TrimSpace(d.Title)
			if title == "" || d.Markdown == "" {
				continue
			}
			folder, _ := cleanFolder(d.Folder)
			kd := &store.KDoc{BaseID: b.ID, Title: cut(title, 200), Folder: folder, Mime: "text/markdown"}
			if err := s.store.CreateKDoc(ctx, kd, d.Markdown, "从应用文件导入", v.user.ID); err != nil {
				s.internalErr(w, r, err)
				return
			}
		}
		if err := s.kbSync(ctx, []*store.KBase{b}); err != nil {
			s.log.Warn("导入后同步知识库索引失败", "kb", b.Slug, "err", err)
		}
		it.Result, it.Note = "created", fmt.Sprintf("建成了只有你自己能查的库，%d 篇文档", len(k.Docs))
		add(it)
	}

	// 应用：工作台里引用了缺少的连接、工具或数据集的组件去掉
	sp := *f.App.Spec
	sp.Template = false
	removed, err := s.pruneWorkbench(ctx, &sp, connOK)
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	if msg := validateSpec(store.KindApp, &sp); msg != "" {
		writeErr(w, http.StatusBadRequest, "应用内容不合格："+msg)
		return
	}
	if msg, err := s.checkWorkbenchSources(ctx, sp.Workbench); err != nil {
		s.internalErr(w, r, err)
		return
	} else if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if slug == "" {
		slug = f.App.Slug
	}
	if !slugRe.MatchString(slug) || strings.HasPrefix(slug, systemSkillPrefix) {
		slug = "imported-app"
	}
	slug = s.freeSlug(ctx, slug)
	raw, _ := json.Marshal(sp)
	owner := v.user.ID
	a := &store.Asset{Kind: store.KindApp, Slug: slug, Title: sp.Title, Description: sp.Description, OwnerID: &owner, Scope: self, Delivery: store.DeliveryOptional}
	if err := s.store.CreateAsset(ctx, a, raw, store.StatusPublished, v.user.ID); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			writeErr(w, http.StatusConflict, "标识 "+slug+" 已被占用")
			return
		}
		s.internalErr(w, r, err)
		return
	}
	_ = s.store.Subscribe(ctx, v.user.ID, a.ID)
	s.audit(r, auditAppImport, "app:"+slug, map[string]any{"id": a.ID, "from": f.App.Slug, "removed_widgets": removed})
	s.hub.notify()
	writeJSON(w, http.StatusCreated, map[string]any{"asset": map[string]any{"id": a.ID, "slug": slug}, "report": report, "removed_widgets": removed})
}

// pruneWorkbench 去掉工作台里引用了不存在（或这次没导入成功）的连接、工具、数据集的组件；
// 去空了的业务面板一起去掉，一个窗格都不剩就不要工作台。返回去掉的组件数。
func (s *Server) pruneWorkbench(ctx context.Context, sp *assetSpec, connOK map[string]bool) (int, error) {
	if sp.Workbench == nil {
		return 0, nil
	}
	tools := map[string]map[string]bool{}
	toolExists := func(conn, tool string) (bool, error) {
		if !connOK[conn] {
			return false, nil
		}
		if _, ok := tools[conn]; !ok {
			a, err := s.store.GetAssetBySlug(ctx, conn)
			if err != nil {
				return false, nil
			}
			list, err := s.store.ConnectionTools(ctx, a.ID)
			if err != nil {
				return false, err
			}
			tools[conn] = map[string]bool{}
			for _, t := range list {
				tools[conn][t.Name] = t.ReadOnly
			}
		}
		return tools[conn][tool], nil
	}
	removed := 0
	panes := []wbPane{}
	for _, p := range sp.Workbench.Panes {
		if p.Type != "panel" {
			panes = append(panes, p)
			continue
		}
		keep := []wbWidget{}
		for _, w := range p.Widgets {
			ok := true
			if src := w.Source; src != nil && w.Type != "kb_scope" {
				switch {
				case src.Table != "" || src.Connection == kbConn:
				case src.Dataset != "":
					_, err := s.store.GetDataSet(ctx, src.Dataset)
					if err != nil && !errors.Is(err, store.ErrNotFound) {
						return 0, err
					}
					ok = err == nil
				default:
					exists, err := toolExists(src.Connection, src.Tool)
					if err != nil {
						return 0, err
					}
					ok = exists
				}
			}
			if ok {
				keep = append(keep, w)
			} else {
				removed++
			}
		}
		if len(keep) > 0 {
			p.Widgets = keep
			panes = append(panes, p)
		}
	}
	if len(panes) == 0 {
		sp.Workbench, sp.Web = nil, false
	} else {
		sp.Workbench.Panes = panes
		if sp.Web && !webEnabled(sp) {
			sp.Web = false
		}
	}
	return removed, nil
}
