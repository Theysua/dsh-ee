package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing/fstest"

	"github.com/hikariming/dsh-ee/server/internal/extension"
	"github.com/hikariming/dsh-ee/server/internal/openapi"
	"github.com/hikariming/dsh-ee/server/internal/store"
)

// 声明式扩展的安装（docs/plugins.md §4.1、§9.4）。
//
// 一个装好的声明式扩展就是一个带清单的 OpenAPI 连接：清单和接口文档存在版本内容里（之后的重启、回滚
// 不用再去取），可见范围、工具开关、角色限定、版本和回滚都用连接已有的那一套。和手工建的连接不同的是，
// 请求发到哪里、用谁的身份、哪些工具只读、哪些参数模型不能填、返回里留哪些字段，全部由清单决定，
// 保存时每次都从清单重新推出来，不看提交上来的值。

// connExtension 是连接版本内容里的扩展部分。
type connExtension struct {
	// 这三项每次保存都从清单重算，提交上来的值不算数
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
	// 清单原文
	Manifest string `json:"manifest,omitempty"`
	// 由人来填的参数的值（fill: admin / app）：参数名 → JSON 值
	Values map[string]json.RawMessage `json:"values,omitempty"`
}

const (
	maxManifestSize = 256 << 10
	// 上传的扩展包：压缩前后的总大小、单个文件大小、文件个数
	maxExtensionUpload = 8 << 20
	maxExtensionFile   = 4 << 20
	maxExtensionFiles  = 200
	// 工具设了「只保留这些字段」时，为了能解析整个返回，最多读这么多
	keepReadLimit = 4 << 20
)

// bindExtension 按清单把连接的内容定下来。返回给提交人看的错误。
func bindExtension(sp *assetSpec) string {
	x := sp.Extension
	if len(x.Manifest) > maxManifestSize {
		return "扩展清单不能超过 256 KB"
	}
	res := extension.CheckInstalled([]byte(x.Manifest), []byte(sp.OpenAPI))
	if msg := checkFailure(res); msg != "" {
		return msg
	}
	m := res.Manifest
	if m.Kind != extension.KindDeclarative {
		return "这个版本只能安装声明式扩展（kind: declarative），服务扩展和端侧扩展还不支持"
	}
	x.ID, x.Version, x.Digest = m.ID, m.Version, res.Digest
	sp.Type = "openapi"
	sp.URL = strings.TrimSpace(sp.URL)
	if sp.URL == "" {
		sp.URL = m.Servers[0]
	}
	if !slices.Contains(m.Servers, sp.URL) {
		return "接口根地址只能用清单里列出的服务器地址：" + strings.Join(m.Servers, "、")
	}
	switch cr := m.Credential; {
	case cr == nil:
		sp.Auth, sp.UserAuth = &connAuth{Type: "none"}, nil
	case cr.Mode == extension.CredShared:
		// 共享凭据放在哪个请求头也由清单决定：写了 header 就放那里，没写是 Authorization: Bearer
		sp.UserAuth, sp.Auth = nil, &connAuth{Type: "bearer"}
		if cr.Header != "" {
			sp.Auth = &connAuth{Type: "header", Header: cr.Header}
		}
	default:
		sp.UserAuth = &connUserAuth{Mode: cr.Mode, Header: cr.Header, Audience: cr.Audience, Claims: cr.Claims, Hint: cr.Hint.Any()}
		sp.Auth = &connAuth{Type: "none"}
		if cr.Shared != nil {
			sp.Auth = &connAuth{Type: cr.Shared.Type, Header: cr.Shared.Header}
		}
	}
	vals, msg := extension.CheckValues(extension.FormParams(res), x.Values)
	if msg != "" {
		return msg
	}
	x.Values = vals
	if strings.TrimSpace(sp.Title) == "" {
		sp.Title = truncateStr(m.Name.Any(), 64)
	}
	if strings.TrimSpace(sp.Description) == "" {
		sp.Description = truncateStr(m.Description.Any(), 1000)
	}
	return ""
}

// checkFailure 把没通过的检查结果写成一句话。
func checkFailure(res *extension.Result) string {
	if res.OK && res.Manifest != nil {
		return ""
	}
	for _, d := range res.Results {
		if d.Severity == extension.SevError {
			msg := "扩展没有通过检查：" + d.Message + "（" + d.Where + "）"
			if res.Errors > 1 {
				msg += "，另有 " + strconv.Itoa(res.Errors-1) + " 个问题"
			}
			return msg + "。用 `dsh-ee extension check <目录>` 看全部问题和改法"
		}
	}
	return "扩展没有通过检查"
}

// extensionCompat 检查扩展要求的最低 dsh-ee 版本。开发版（版本号不是 x.y.z）不拦。
func (s *Server) extensionCompat(m *extension.Manifest) string {
	if m.MinVersion == "" {
		return ""
	}
	cur, ok1 := semverCore(s.opts.Version)
	min, ok2 := semverCore(m.MinVersion)
	if ok1 && ok2 && slices.Compare(cur[:], min[:]) < 0 {
		return fmt.Sprintf("这个扩展要求 dsh-ee %s 以上，当前是 %s；请先升级 dsh-ee", m.MinVersion, s.opts.Version)
	}
	return ""
}

func semverCore(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// checkExtensionChange 拦下会让一个连接「换了身份」的新版本：扩展装出来的连接只能升级成同一个扩展的另一个版本。
// 应用和员工是按连接名用它的工具的，连接名背后换成另一个扩展、或悄悄变回手工连接，都不该发生。
func (s *Server) checkExtensionChange(ctx context.Context, a *store.Asset, sp *assetSpec) (string, error) {
	lv, err := s.store.LiveAssetVersion(ctx, a.ID)
	if err != nil {
		return "", err
	}
	old := parseSpec(lv.Spec).Extension
	switch {
	case old == nil:
		return "", nil
	case sp.Extension == nil:
		return "这个连接是扩展 " + old.ID + " 装出来的，地址、凭据和工具都由清单决定，不能改成手工连接；要换掉请删除后重新接入", nil
	case sp.Extension.ID != old.ID:
		return fmt.Sprintf("这个连接装的是扩展 %s，不能换成另一个扩展 %s；请把它作为新的扩展安装", old.ID, sp.Extension.ID), nil
	}
	return "", nil
}

// extensionAudit 给审计记录补上扩展的身份、版本和清单哈希。
func extensionAudit(sp *assetSpec, meta map[string]any) map[string]any {
	if x := sp.Extension; x != nil {
		meta["extension"], meta["digest"] = x.ID+"@"+x.Version, x.Digest
	}
	return meta
}

// connOp 是存在工具上的调用信息：怎么拼请求，加上清单规定的、调用时要补的参数和要留的字段。
type connOp struct {
	openapi.Operation
	Fixed []extension.BoundParam `json:"fixed,omitempty"`
	Keep  []string               `json:"keep,omitempty"`
}

// applyFixed 把不由模型填的参数补进这次调用的参数里。模型自己带了这些参数的，一律不算数。
func applyFixed(args map[string]any, fixed []extension.BoundParam, values map[string]json.RawMessage) error {
	for _, p := range fixed {
		field, inBody := strings.CutPrefix(p.Name, "body.")
		raw := values[p.Key]
		if len(raw) == 0 || string(raw) == "null" {
			if p.Required {
				return fmt.Errorf("参数 %s 由管理员在扩展配置里统一设置，现在还没有设置；请联系管理员，不要自己填", p.Key)
			}
			if body, ok := args["body"].(map[string]any); inBody && ok {
				delete(body, field)
			} else if !inBody {
				delete(args, p.Name)
			}
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return fmt.Errorf("参数 %s 的设置读不出来，请管理员重新保存扩展配置", p.Key)
		}
		if !inBody {
			args[p.Name] = v
			continue
		}
		body, ok := args["body"].(map[string]any)
		if !ok {
			body = map[string]any{}
			args["body"] = body
		}
		body[field] = v
	}
	return nil
}

// keepFields 只留下返回里指定的字段。写第一层的字段名：返回的是对象就留它的这些字段，是数组就留每一项的这些字段；
// 「字段.子字段」再往里一层：那个字段是对象就留它的子字段，是数组就留它每一项的子字段
// （{"items": [...], "total": 1} 这样包了一层的列表，写 items.id、items.title、total）。
// 返回不是 JSON、或既不是对象也不是数组时 ok 为 false，调用方原样返回。
func keepFields(body []byte, keep []string) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	var whole []string
	sub := map[string][]string{}
	var parents []string
	for _, k := range keep {
		parent, child, dotted := strings.Cut(k, ".")
		if !dotted {
			whole = append(whole, k)
		} else if child != "" && !strings.Contains(child, ".") {
			if sub[parent] == nil {
				parents = append(parents, parent)
			}
			sub[parent] = append(sub[parent], child)
		}
	}
	pick := func(obj map[string]any, fields []string) map[string]any {
		out := make(map[string]any, len(fields))
		for _, k := range fields {
			if x, ok := obj[k]; ok {
				out[k] = x
			}
		}
		return out
	}
	// each 对一个对象、或数组里的每个对象做同一件事；别的值原样留着
	each := func(x any, f func(map[string]any) map[string]any) any {
		switch y := x.(type) {
		case map[string]any:
			return f(y)
		case []any:
			for i, item := range y {
				if obj, ok := item.(map[string]any); ok {
					y[i] = f(obj)
				}
			}
		}
		return x
	}
	top := func(obj map[string]any) map[string]any {
		out := pick(obj, whole)
		for _, parent := range parents {
			if _, kept := out[parent]; kept {
				continue
			}
			if x, ok := obj[parent]; ok {
				out[parent] = each(x, func(o map[string]any) map[string]any { return pick(o, sub[parent]) })
			}
		}
		return out
	}
	switch v.(type) {
	case map[string]any, []any:
		v = each(v, top)
	default:
		return nil, false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, false
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), true
}

// ---- 安装前的检查 ----

type extensionInspectReq struct {
	// 扩展包（zip）的 base64；或者直接给清单和接口文档的原文
	Zip      string `json:"zip"`
	Manifest string `json:"manifest"`
	OpenAPI  string `json:"openapi"`
	// 给已有的连接升级时传它的 id：结果里会带上和线上版本的差别
	ID int64 `json:"id"`
}

// extensionToolView 是给管理员看的一个工具。
type extensionToolView struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Method      string                 `json:"method"`
	Path        string                 `json:"path"`
	ReadOnly    bool                   `json:"read_only"`
	Fixed       []extension.BoundParam `json:"fixed,omitempty"`
	Keep        []string               `json:"keep,omitempty"`
}

// extensionChange 是升级带来的、要管理员留意的变化。
type extensionChange struct {
	From string `json:"from"`
	// 这些写操作是新出现的（或从只读变成了写操作），升级后先不启用
	NewWrites []string `json:"new_writes,omitempty"`
	NewReads  []string `json:"new_reads,omitempty"`
	Removed   []string `json:"removed,omitempty"`
	// 凭据模式变了：旧的 → 新的
	Credential []string `json:"credential,omitempty"`
	// 请求会发往的地址多了
	NewServers []string `json:"new_servers,omitempty"`
}

// POST /api/admin/extensions/inspect：检查一个上传的扩展，返回安装前要给管理员看的全部内容。不保存任何东西。
func (s *Server) handleExtensionInspect(w http.ResponseWriter, r *http.Request) {
	var req extensionInspectReq
	if !readJSONLimit(w, r, &req, maxExtensionUpload*4/3+maxExtensionFile*2+(64<<10)) {
		return
	}
	var files fs.FS
	if req.Zip != "" {
		raw, err := base64.StdEncoding.DecodeString(req.Zip)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "上传的文件读不出来")
			return
		}
		if files, err = unzipExtension(raw); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		if strings.TrimSpace(req.Manifest) == "" {
			writeErr(w, http.StatusBadRequest, "请上传扩展包（zip），或选择扩展目录里的 "+extension.ManifestFile+" 和接口文档")
			return
		}
		if len(req.Manifest) > maxManifestSize || len(req.OpenAPI) > maxExtensionFile {
			writeErr(w, http.StatusBadRequest, "清单不能超过 256 KB，接口文档不能超过 4 MB")
			return
		}
		// 只给了两个文件：图标、附带的应用没法查，按已安装的方式检查
		files = nil
	}
	var res *extension.Result
	if files != nil {
		res = extension.Check(files)
	} else {
		res = extension.CheckInstalled([]byte(req.Manifest), []byte(req.OpenAPI))
	}
	resp := map[string]any{"check": res}
	if res.Manifest == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	m := res.Manifest
	resp["manifest"] = m
	installable := res.OK
	switch {
	case m.Kind != extension.KindDeclarative && slices.Contains([]string{extension.KindService, extension.KindLocal}, m.Kind):
		installable = false
		resp["blocked"] = "这个版本只能安装声明式扩展（kind: declarative），服务扩展和端侧扩展还不支持"
	case res.OK:
		if msg := s.extensionCompat(m); msg != "" {
			installable = false
			resp["blocked"] = msg
		}
	}
	resp["installable"] = installable
	if !installable || m.Kind != extension.KindDeclarative {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	bound, err := extension.Bind(res)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	tools := make([]extensionToolView, len(bound))
	for i, t := range bound {
		tools[i] = extensionToolView{Name: t.Name, Description: t.Description, Method: t.Op.Method, Path: t.Op.Path, ReadOnly: t.ReadOnly, Fixed: t.Fixed, Keep: t.Keep}
	}
	resp["tools"] = tools
	resp["params"] = extension.FormParams(res)
	// 清单里写了、但这个版本装的时候不处理的部分，要让管理员知道
	ignored := []string{}
	if len(m.Config) > 0 {
		ignored = append(ignored, "config")
	}
	if len(m.Apps) > 0 {
		ignored = append(ignored, "apps")
	}
	resp["ignored"] = ignored
	// 安装时原样交回来的两份原文
	manifest := []byte(req.Manifest)
	if files != nil {
		manifest, _ = fs.ReadFile(files, extension.ManifestFile)
	}
	resp["package"] = map[string]string{"manifest": string(manifest), "openapi": string(res.OpenAPIDoc)}
	resp["slug"] = suggestSlug(m.ID)
	if req.ID > 0 {
		change, values, msg, err := s.extensionChange(r, req.ID, res, bound)
		if err != nil {
			s.internalErr(w, r, err)
			return
		}
		if msg != "" {
			resp["installable"], resp["blocked"] = false, msg
		}
		resp["change"], resp["values"] = change, values
	}
	writeJSON(w, http.StatusOK, resp)
}

// suggestSlug 用 id 里的名字作默认的连接名（工具前缀）。
func suggestSlug(id string) string {
	_, name, _ := strings.Cut(id, "/")
	if len(name) > maxSlug {
		name = strings.TrimRight(name[:maxSlug], "-")
	}
	if !slugRe.MatchString(name) {
		return ""
	}
	return name
}

// extensionChange 比较要装的版本和连接线上的版本。第三个返回值非空表示不能这样升级。
func (s *Server) extensionChange(r *http.Request, id int64, res *extension.Result, bound []extension.BoundTool) (*extensionChange, map[string]json.RawMessage, string, error) {
	ctx := r.Context()
	a, err := s.store.GetAsset(ctx, id)
	if err != nil || a.Kind != store.KindConnection {
		return nil, nil, "要升级的连接不存在", nil
	}
	lv, err := s.store.LiveAssetVersion(ctx, id)
	if err != nil {
		return nil, nil, "要升级的连接还没有生效的版本", nil
	}
	old := parseSpec(lv.Spec)
	ch := &extensionChange{From: "手工建的连接"}
	var values map[string]json.RawMessage
	oldMode := ""
	if old.UserAuth != nil {
		oldMode = old.UserAuth.Mode
	} else if old.Auth != nil && old.Auth.Type != "none" {
		oldMode = extension.CredShared
	}
	oldServers := []string{old.URL}
	if x := old.Extension; x != nil {
		if x.ID != res.Manifest.ID {
			return nil, nil, fmt.Sprintf("连接 %s 装的是扩展 %s，不能换成另一个扩展 %s；请把它作为新的扩展安装", a.Slug, x.ID, res.Manifest.ID), nil
		}
		ch.From = x.ID + "@" + x.Version
		values = x.Values
		if prev := extension.CheckInstalled([]byte(x.Manifest), []byte(old.OpenAPI)); prev.Manifest != nil {
			oldServers = prev.Manifest.Servers
		}
	}
	newMode := ""
	if res.Credential != nil {
		newMode = res.Credential.Mode
	}
	if oldMode != newMode {
		ch.Credential = []string{oldMode, newMode}
	}
	for _, srv := range res.Manifest.Servers {
		if !slices.Contains(oldServers, srv) {
			ch.NewServers = append(ch.NewServers, srv)
		}
	}
	existing, err := s.store.ConnectionTools(ctx, id)
	if err != nil {
		return nil, nil, "", err
	}
	was := map[string]bool{}
	for _, t := range existing {
		was[t.Name] = t.ReadOnly
	}
	now := map[string]bool{}
	for _, t := range bound {
		now[t.Name] = true
		ro, had := was[t.Name]
		switch {
		case !t.ReadOnly && (!had || ro):
			ch.NewWrites = append(ch.NewWrites, t.Name)
		case t.ReadOnly && !had:
			ch.NewReads = append(ch.NewReads, t.Name)
		}
	}
	for _, t := range existing {
		if !now[t.Name] {
			ch.Removed = append(ch.Removed, t.Name)
		}
	}
	return ch, values, "", nil
}

// unzipExtension 把上传的扩展包读进内存。只读普通文件，路径不合法的、太大的、太多的都拒绝；
// 包里只有一个顶层目录时（直接压缩了整个文件夹）以它为根。
func unzipExtension(raw []byte) (fs.FS, error) {
	if len(raw) > maxExtensionUpload {
		return nil, fmt.Errorf("扩展包不能超过 %d MB", maxExtensionUpload>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("上传的不是 zip 文件")
	}
	files := fstest.MapFS{}
	total := 0
	for _, f := range zr.File {
		name := path.Clean(strings.ReplaceAll(f.Name, "\\", "/"))
		if f.FileInfo().IsDir() || strings.HasPrefix(path.Base(name), ".") || strings.HasPrefix(name, "__MACOSX/") {
			continue
		}
		if !fs.ValidPath(name) || !f.Mode().IsRegular() {
			return nil, fmt.Errorf("扩展包里有不合法的路径：%s", f.Name)
		}
		if len(files) >= maxExtensionFiles {
			return nil, fmt.Errorf("扩展包里的文件不能超过 %d 个", maxExtensionFiles)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("扩展包里的 %s 读不出来", f.Name)
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxExtensionFile+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("扩展包里的 %s 读不出来", f.Name)
		}
		total += len(data)
		if len(data) > maxExtensionFile || total > maxExtensionUpload {
			return nil, fmt.Errorf("扩展包解开后太大（单个文件最多 %d MB，总共最多 %d MB）", maxExtensionFile>>20, maxExtensionUpload>>20)
		}
		files[name] = &fstest.MapFile{Data: data}
	}
	if _, ok := files[extension.ManifestFile]; ok {
		return files, nil
	}
	root := ""
	for name := range files {
		dir, _, ok := strings.Cut(name, "/")
		if !ok || (root != "" && dir != root) {
			return files, nil
		}
		root = dir
	}
	if _, ok := files[root+"/"+extension.ManifestFile]; ok {
		return fs.Sub(files, root)
	}
	return files, nil
}
