package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hikariming/dsh-ee/server/internal/api"
	"github.com/hikariming/dsh-ee/server/internal/secret"
	"github.com/hikariming/dsh-ee/server/internal/store"
)

const extOpenAPI = `openapi: 3.0.3
info: { title: OA, version: "1" }
paths:
  /forms:
    get:
      operationId: list_forms
      summary: 列出审批单
      parameters:
        - { name: status, in: query, schema: { type: string } }
        - { name: tenant, in: query, required: true, schema: { type: string } }
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  total: { type: integer }
                  trace: { type: string }
                  items:
                    type: array
                    items: { type: object, properties: { id: { type: string }, title: { type: string }, internal_note: { type: string } } }
  /leave:
    post:
      operationId: submit_leave
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [company, days]
              properties:
                company: { type: string }
                days: { type: number }
  /ping:
    get:
      operationId: ping
`

// extManifest 按给定的服务器地址和版本写一份清单；extra 是要并进去的其他顶层字段。
func extManifest(server, version string, tools string) string {
	return fmt.Sprintf(`{
  "id": "acme/oa", "version": %q, "name": "Acme OA", "description": "查审批单、提交请假",
  "kind": "declarative", "openapi": "openapi.yaml", "servers": [%q],
  "credential": { "mode": "ee_jwt", "header": "X-EE-Token", "shared": { "type": "header", "header": "X-API-Key" } },
  "tools": %s
}`, version, server, tools)
}

const extToolsV1 = `[
  { "name": "list_forms", "readOnly": true, "description": "列出当前员工的审批单",
    "params": [ { "name": "tenant", "fill": "admin", "label": "租户" } ],
    "keep": ["items.id", "items.title", "total"] },
  { "name": "submit_leave",
    "params": [ { "name": "body.company", "fill": "admin", "label": "公司代码", "default": "A01" } ] }
]`

// 声明式扩展从检查到安装、调用、升级的全过程：请求发到哪里、用谁的身份、哪些参数模型不能填、
// 返回里留哪些字段，都由清单决定。
func TestDeclarativeExtension(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if _, err := api.Bootstrap(ctx, st, "admin", "admin-password-123"); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, _ := secret.NewBox(key)
	srv := api.New(st, box, api.Options{DataDir: t.TempDir(), Version: "0.2.0"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	admin := newClient(t, ts.URL)
	code, body := admin.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "admin-password-123"}, nil)
	expect(t, "管理员登录", code, 200, body)
	code, body = admin.do("GET", "/api/settings", nil, nil)
	expect(t, "读设置", code, 200, body)
	set := decodeAs[map[string]any](t, body)
	set["scope"], set["baseUrl"] = "@acme", ts.URL
	code, body = admin.do("PUT", "/api/admin/settings", set, nil)
	expect(t, "设置访问地址", code, 200, body)

	code, body = admin.do("POST", "/api/admin/users", map[string]any{"username": "alice", "password": "alice-password-1", "department": "研发效能部"}, nil)
	expect(t, "建用户", code, 201, body)
	alice := newClient(t, ts.URL)
	code, body = alice.do("POST", "/api/auth/login", map[string]string{"username": "alice", "password": "alice-password-1"}, nil)
	expect(t, "登录", code, 200, body)
	code, body = alice.do("POST", "/api/me/npm-tokens", map[string]any{"name": "dsh", "access": "read"}, nil)
	expect(t, "接入令牌", code, 201, body)
	dsh := dshClient{t: t, base: ts.URL, token: decodeAs[struct{ Token string }](t, body).Token}

	// 企业的 OA：记下每个请求，返回里带一个不该给模型看的字段
	type seen struct {
		Method, Path, Query, APIKey, Token, Body string
	}
	var mu sync.Mutex
	var reqs []seen
	oa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("X-API-Key"), r.Header.Get("X-EE-Token"), string(b)})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/forms":
			_, _ = io.WriteString(w, `{"total":2,"trace":"req-8842","items":[{"id":"F1","title":"报销 <差旅>","internal_note":"仅内部可见"},{"id":"F2","title":"请假","internal_note":"x"}]}`)
		case "/api/leave":
			_, _ = io.WriteString(w, `{"ok":true}`)
		default:
			_, _ = io.WriteString(w, `{"pong":true}`)
		}
	}))
	defer oa.Close()
	last := func() seen {
		mu.Lock()
		defer mu.Unlock()
		if len(reqs) == 0 {
			t.Fatal("OA 没有收到请求")
		}
		return reqs[len(reqs)-1]
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(reqs)
	}
	server := oa.URL + "/api"

	zipOf := func(dir string, files map[string]string) string {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, content := range files {
			w, _ := zw.Create(dir + name)
			_, _ = io.WriteString(w, content)
		}
		_ = zw.Close()
		return base64.StdEncoding.EncodeToString(buf.Bytes())
	}
	type inspected struct {
		Check struct {
			OK      bool
			Digest  string
			Results []struct{ Code, Severity, Message string }
		}
		Installable bool
		Blocked     string
		Slug        string
		Package     struct{ Manifest, OpenAPI string }
		Tools       []struct {
			Name     string
			ReadOnly bool `json:"read_only"`
			Method   string
			Keep     []string
			Fixed    []struct{ Name, Key, Fill string }
		}
		Params []struct {
			Key      string
			Fill     string
			Required bool
			Default  json.RawMessage
			Tools    []string
		}
		Change *struct {
			From       string
			NewWrites  []string `json:"new_writes"`
			NewReads   []string `json:"new_reads"`
			Removed    []string
			Credential []string
		}
		Values map[string]json.RawMessage
	}
	inspect := func(what string, req map[string]any) inspected {
		t.Helper()
		code, body := admin.do("POST", "/api/admin/extensions/inspect", req, nil)
		expect(t, what, code, 200, body)
		return decodeAs[inspected](t, body)
	}

	// ---- 安装前的检查 ----
	v1 := extManifest(server, "1.0.0", extToolsV1)
	// 直接压缩整个文件夹得到的包，文件都在一层目录下面
	pkg := inspect("检查扩展包", map[string]any{"zip": zipOf("acme-oa/", map[string]string{"dsh-ee.extension.json": v1, "openapi.yaml": extOpenAPI, "README.md": "# OA"})})
	if !pkg.Check.OK || !pkg.Installable || pkg.Slug != "oa" || pkg.Package.Manifest != v1 {
		t.Fatalf("合格的扩展包应当可以安装：%+v", pkg)
	}
	if len(pkg.Tools) != 3 || len(pkg.Params) != 2 {
		t.Fatalf("应当有 3 个工具、2 个要管理员填的参数：%+v", pkg)
	}
	if p := pkg.Params[0]; p.Key != "tenant" || !p.Required || p.Fill != "admin" {
		t.Fatalf("tenant 参数 = %+v", p)
	}
	// 只读的账号不能检查扩展
	code, body = alice.do("POST", "/api/admin/extensions/inspect", map[string]any{"manifest": v1, "openapi": extOpenAPI}, nil)
	expect(t, "普通员工不能检查扩展", code, 403, body)
	// 有问题的扩展：给出每一条问题，不能装
	bad := inspect("检查有问题的扩展", map[string]any{"manifest": strings.Replace(v1, `"readOnly": true, `, "", 1) + " ", "openapi": "openapi: 3.0.3\npaths: {}\n"})
	if bad.Check.OK || bad.Installable || len(bad.Check.Results) == 0 {
		t.Fatalf("接口文档读不了的扩展不该能装：%+v", bad)
	}
	// 服务扩展这个版本装不了，要说清楚
	svc := inspect("检查服务扩展", map[string]any{"manifest": `{"id":"acme/svc","version":"1.0.0","name":"S","description":"d","kind":"service","service":{"url":"https://x.acme.internal"}}`})
	if svc.Installable || !strings.Contains(svc.Blocked, "声明式") {
		t.Fatalf("服务扩展应当提示还不支持：%+v", svc)
	}
	// 要求的 dsh-ee 版本比当前高
	newer := inspect("检查要求新版本的扩展", map[string]any{"manifest": strings.Replace(v1, `"kind"`, `"minVersion": "9.0.0", "kind"`, 1), "openapi": extOpenAPI})
	if newer.Installable || !strings.Contains(newer.Blocked, "9.0.0") {
		t.Fatalf("minVersion 高于当前版本应当拦下：%+v", newer)
	}
	// 压缩包里的路径不能跳出去
	code, body = admin.do("POST", "/api/admin/extensions/inspect", map[string]any{"zip": zipOf("../", map[string]string{"dsh-ee.extension.json": v1})}, nil)
	expect(t, "包里有 ../ 路径", code, 400, body)

	// ---- 安装 ----
	install := func(what string, spec map[string]any, extra map[string]any, want int) (int64, string) {
		t.Helper()
		req := map[string]any{"kind": "connection", "slug": "oa", "delivery": "auto", "spec": spec}
		for k, v := range extra {
			req[k] = v
		}
		code, body := admin.do("POST", "/api/hub", req, nil)
		expect(t, what, code, want, body)
		return decodeAs[struct{ Asset store.Asset }](t, body).Asset.ID, string(body)
	}
	spec := func(manifest string, values map[string]any) map[string]any {
		return map[string]any{"title": "", "description": "", "openapi": extOpenAPI, "extension": map[string]any{"manifest": manifest, "values": values}}
	}
	// 必填的管理员参数没填：装不上
	_, msg := install("缺必填参数", spec(v1, nil), nil, 400)
	if !strings.Contains(msg, "租户") {
		t.Fatalf("应当指出缺的是哪个参数：%s", msg)
	}
	// 接口根地址只能是清单里列出来的
	s := spec(v1, map[string]any{"tenant": "t-001"})
	s["url"] = "https://evil.example.com/api"
	_, msg = install("换了清单之外的地址", s, nil, 400)
	if !strings.Contains(msg, server) {
		t.Fatalf("应当说明只能用清单里的地址：%s", msg)
	}
	// 清单没过检查的不能装（绕过检查页直接调接口也一样）
	_, _ = install("清单有错", spec(strings.Replace(v1, `"acme/oa"`, `"Acme OA"`, 1), map[string]any{"tenant": "t-001"}), nil, 400)
	// 凭据怎么带由清单决定：提交上来的 auth / user_auth / type 不算数
	s = spec(v1, map[string]any{"tenant": "t-001"})
	s["type"], s["auth"], s["user_auth"] = "mcp", map[string]any{"type": "none"}, map[string]any{"mode": "user_secret"}
	id, _ := install("安装", s, map[string]any{"secret": "oa-key"}, 201)
	path := "/api/hub/" + itoa(id)

	type detail struct {
		Asset struct {
			Title string
			Spec  struct {
				Type, URL string
				Auth      struct{ Type, Header string }
				UserAuth  *struct{ Mode, Header string } `json:"user_auth"`
				Extension *struct {
					ID, Version, Digest, Manifest string
					Values                        map[string]json.RawMessage
				}
			}
		}
		Tools []struct {
			Name        string
			Enabled     bool
			ReadOnly    bool            `json:"read_only"`
			InputSchema json.RawMessage `json:"input_schema"`
		}
		Calls []struct {
			Tool, Extension, Digest string
			AuthMode                string `json:"auth_mode"`
		}
	}
	get := func(c *client) detail {
		t.Helper()
		code, body := c.do("GET", path, nil, nil)
		expect(t, "连接详情", code, 200, body)
		return decodeAs[detail](t, body)
	}
	d := get(admin)
	sp := d.Asset.Spec
	if d.Asset.Title != "Acme OA" || sp.Type != "openapi" || sp.URL != server || sp.Auth.Type != "header" || sp.Auth.Header != "X-API-Key" ||
		sp.UserAuth == nil || sp.UserAuth.Mode != "ee_jwt" || sp.UserAuth.Header != "X-EE-Token" {
		t.Fatalf("连接的内容应当全由清单决定：%+v", d.Asset)
	}
	if x := sp.Extension; x == nil || x.ID != "acme/oa" || x.Version != "1.0.0" || x.Digest != pkg.Check.Digest || x.Manifest != v1 ||
		string(x.Values["tenant"]) != `"t-001"` || string(x.Values["company"]) != `"A01"` {
		t.Fatalf("扩展信息 = %+v", sp.Extension)
	}
	tools := map[string]int{}
	for i, tl := range d.Tools {
		tools[tl.Name] = i
	}
	if len(d.Tools) != 3 || !d.Tools[tools["list_forms"]].Enabled || !d.Tools[tools["ping"]].Enabled || d.Tools[tools["submit_leave"]].Enabled {
		t.Fatalf("只读工具默认启用，写操作默认不启用：%+v", d.Tools)
	}
	if strings.Contains(string(d.Tools[tools["list_forms"]].InputSchema), "tenant") {
		t.Fatal("管理员设置的参数不该出现在工具定义里")
	}
	// 员工看得到装的是哪个扩展，看不到清单原文和管理员填的值
	if x := get(alice).Asset.Spec.Extension; x == nil || x.ID != "acme/oa" || x.Manifest != "" || x.Values != nil {
		t.Fatalf("员工看到的扩展信息 = %+v", x)
	}

	call := func(tool string, args map[string]any) (string, bool) {
		t.Helper()
		res := dsh.rpc("tools/call", map[string]any{"name": tool, "arguments": args})
		r, _ := res["result"].(map[string]any)
		if r == nil {
			t.Fatalf("调 %s：%v", tool, res)
		}
		text := ""
		for _, c := range r["content"].([]any) {
			text += fmt.Sprint(c.(map[string]any)["text"])
		}
		isErr, _ := r["isError"].(bool)
		return text, isErr
	}

	// ---- 调用：模型带了管理员的参数也不算数；返回只留清单指定的字段 ----
	text, isErr := call("oa__list_forms", map[string]any{"status": "pending", "tenant": "someone-else"})
	if isErr {
		t.Fatalf("list_forms 失败：%s", text)
	}
	got := last()
	if got.Path != "/api/forms" || !strings.Contains(got.Query, "tenant=t-001") || strings.Contains(got.Query, "someone-else") || !strings.Contains(got.Query, "status=pending") {
		t.Fatalf("发给 OA 的请求 = %+v", got)
	}
	if got.APIKey != "oa-key" || strings.Count(got.Token, ".") != 2 {
		t.Fatalf("共享凭据和员工身份应当各放在清单指定的请求头里：%+v", got)
	}
	if text != `{"items":[{"id":"F1","title":"报销 <差旅>"},{"id":"F2","title":"请假"}],"total":2}` {
		t.Fatalf("返回应当只留清单指定的字段（列表包了一层也一样），文字不转义：%s", text)
	}
	// 写操作默认没启用：员工调不到
	if res := dsh.rpc("tools/call", map[string]any{"name": "oa__submit_leave", "arguments": map[string]any{"body": map[string]any{"days": 2}}}); res["error"] == nil {
		t.Fatalf("没启用的写操作不该能调：%v", res)
	}
	code, body = admin.do("PATCH", "/api/admin/connections/"+itoa(id)+"/tools/submit_leave", map[string]any{"enabled": true, "roles": []int64{}}, nil)
	expect(t, "启用写操作", code, 204, body)
	text, isErr = call("oa__submit_leave", map[string]any{"body": map[string]any{"days": 2, "company": "B02"}})
	if isErr {
		t.Fatalf("submit_leave 失败：%s", text)
	}
	if got = last(); got.Method != "POST" || !strings.Contains(got.Body, `"company":"A01"`) || strings.Contains(got.Body, "B02") || !strings.Contains(got.Body, `"days":2`) {
		t.Fatalf("请求体里管理员设置的字段应当是 A01：%+v", got)
	}

	// 调用记录写明是哪个扩展的哪个版本
	d = get(admin)
	if len(d.Calls) < 2 || d.Calls[0].Extension != "acme/oa@1.0.0" || d.Calls[0].Digest != pkg.Check.Digest || d.Calls[0].AuthMode != "ee_jwt" {
		t.Fatalf("调用记录 = %+v", d.Calls)
	}

	// ---- 只有一份共享凭据的扩展：放在哪个请求头也由清单决定，提交上来的 auth 不算数 ----
	sharedManifest := strings.NewReplacer(`"acme/oa"`, `"acme/oa-report"`,
		`{ "mode": "ee_jwt", "header": "X-EE-Token", "shared": { "type": "header", "header": "X-API-Key" } }`, `{ "mode": "shared", "header": "X-API-Key" }`).Replace(v1)
	ss := spec(sharedManifest, map[string]any{"tenant": "t-009"})
	ss["auth"] = map[string]any{"type": "bearer"}
	code, body = admin.do("POST", "/api/hub", map[string]any{"kind": "connection", "slug": "oa-report", "delivery": "auto", "spec": ss, "secret": "report-key"}, nil)
	expect(t, "安装共享凭据的扩展", code, 201, body)
	if text, isErr = call("oa-report__ping", nil); isErr {
		t.Fatalf("ping 失败：%s", text)
	}
	if got = last(); got.APIKey != "report-key" || got.Token != "" {
		t.Fatalf("共享凭据应当放在清单写的 X-API-Key 里，不带员工身份：%+v", got)
	}

	// ---- 改管理员参数：提交新版本，清单不变 ----
	newVersion := func(what string, spec map[string]any, want int) string {
		t.Helper()
		code, body := admin.do("POST", path+"/versions", map[string]any{"spec": spec}, nil)
		expect(t, what, code, want, body)
		return string(body)
	}
	newVersion("改参数", spec(v1, map[string]any{"tenant": "t-002", "company": "A01"}), 201)
	if _, isErr = call("oa__list_forms", nil); isErr || !strings.Contains(last().Query, "tenant=t-002") {
		t.Fatalf("改完参数应当马上生效：%+v", last())
	}

	// ---- 升级 ----
	// 新版本：list_forms 不再标只读（变成写操作），多了一个只读工具的说明，凭据模式不变
	v2 := extManifest(server, "1.1.0", `[
  { "name": "list_forms", "params": [ { "name": "tenant", "fill": "admin", "label": "租户" } ], "keep": ["items.id", "items.title", "total"] },
  { "name": "submit_leave", "params": [ { "name": "body.company", "fill": "admin", "label": "公司代码", "default": "A01" } ] },
  { "name": "ping", "readOnly": true, "description": "看 OA 通不通" }
]`)
	up := inspect("检查升级包", map[string]any{"manifest": v2, "openapi": extOpenAPI, "id": id})
	if !up.Installable || up.Change == nil || up.Change.From != "acme/oa@1.0.0" || fmt.Sprint(up.Change.NewWrites) != "[list_forms]" || len(up.Change.Credential) != 0 {
		t.Fatalf("升级前应当指出 list_forms 变成了写操作：%+v %+v", up, up.Change)
	}
	if string(up.Values["tenant"]) != `"t-002"` {
		t.Fatalf("升级时应当带出现在的参数值：%v", up.Values)
	}
	// 换成另一个扩展、或者变回手工连接，都不行
	other := strings.Replace(v2, `"acme/oa"`, `"acme/crm"`, 1)
	if msg := newVersion("换成另一个扩展", spec(other, map[string]any{"tenant": "t-002"}), 400); !strings.Contains(msg, "acme/crm") {
		t.Fatalf("应当说明不能换扩展：%s", msg)
	}
	if blocked := inspect("检查另一个扩展的升级包", map[string]any{"manifest": other, "openapi": extOpenAPI, "id": id}); blocked.Installable {
		t.Fatal("检查页也应当拦下换扩展")
	}
	newVersion("变回手工连接", map[string]any{"title": "OA", "type": "openapi", "url": server, "openapi": extOpenAPI}, 400)
	newVersion("升级", spec(v2, map[string]any{"tenant": "t-002"}), 201)
	d = get(admin)
	tools = map[string]int{}
	for i, tl := range d.Tools {
		tools[tl.Name] = i
	}
	if x := d.Asset.Spec.Extension; x.Version != "1.1.0" || x.Digest == pkg.Check.Digest {
		t.Fatalf("升级后的扩展信息 = %+v", x)
	}
	if lf := d.Tools[tools["list_forms"]]; lf.ReadOnly || lf.Enabled {
		t.Fatalf("从只读变成写操作的工具，升级后要管理员重新打开：%+v", lf)
	}
	if sl := d.Tools[tools["submit_leave"]]; !sl.Enabled {
		t.Fatal("管理员已经打开的写操作，升级后保持打开")
	}
	before := count()
	if res := dsh.rpc("tools/call", map[string]any{"name": "oa__list_forms", "arguments": map[string]any{}}); res["error"] == nil || count() != before {
		t.Fatalf("升级后还没重新打开的工具不该能调：%v", res)
	}
	if text, isErr = call("oa__ping", nil); isErr {
		t.Fatalf("ping 失败：%s", text)
	}
	if d = get(admin); d.Calls[0].Extension != "acme/oa@1.1.0" {
		t.Fatalf("升级后的调用记录应当记新版本：%+v", d.Calls[0])
	}
}
