package extension

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

const exampleDir = "../../../extensions/examples/demo-declarative"

// 仓库里的声明式示例必须一直是合格的：没有错误，也没有提醒。
func TestExamplePasses(t *testing.T) {
	res := Check(os.DirFS(exampleDir))
	if !res.OK || res.Errors != 0 || res.Warnings != 0 || len(res.Results) != 0 {
		t.Fatalf("示例应一条都不报：%+v", res.Results)
	}
	if res.Identity != "dsh-ee/demo-itsm@0.1.0" || res.Kind != KindDeclarative || len(res.Digest) != 64 || !slices.Equal(res.Servers, []string{"http://127.0.0.1:8080"}) {
		t.Fatalf("身份、形式、摘要、服务器地址不对：%+v", res)
	}
	got := map[string]ToolInfo{}
	for _, tool := range res.Tools {
		got[tool.Name] = tool
	}
	if len(got) != 4 || !got["get_summary"].ReadOnly || !got["list_tickets"].ReadOnly || !got["list_categories"].ReadOnly {
		t.Fatalf("应有 4 个工具，查询类的是只读：%+v", res.Tools)
	}
	if w := got["create_ticket"]; w.ReadOnly || w.Method != "POST" || w.Path != "/demo/itsm/tickets" || !w.Listed {
		t.Fatalf("新建工单是写操作：%+v", w)
	}
	// 改一个字，摘要就变
	raw, _ := os.ReadFile(exampleDir + "/" + ManifestFile)
	doc, _ := os.ReadFile(exampleDir + "/openapi.yaml")
	if Digest(raw, doc) != res.Digest || Digest(append(raw, ' '), doc) == res.Digest || Digest(raw, append(doc, ' ')) == res.Digest {
		t.Fatal("摘要应由清单和接口文档共同决定")
	}
}

const goodDoc = `{"openapi":"3.0.0","info":{"title":"OA"},"servers":[{"url":"https://oa.acme.internal/api"}],"paths":{
 "/forms":{"get":{"operationId":"list_forms","parameters":[{"name":"status","in":"query","schema":{"type":"string"}},{"name":"mine","in":"query","schema":{"type":"boolean"}}],
                  "responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"array","items":{"$ref":"#/components/schemas/Form"}}}}}}},
           "post":{"operationId":"submit_leave","requestBody":{"content":{"application/json":{"schema":{"type":"object","properties":{"tenant":{"type":"string"},"days":{"type":"number"},"reason":{"type":"string"}}}}}}}},
 "/forms/{id}":{"get":{"operationId":"get_form","parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}}]},
                "delete":{"operationId":"cancel_form","parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}}]}},
 "/search":{"post":{"operationId":"search_forms","requestBody":{"content":{"application/json":{"schema":{"type":"object"}}}}}}},
 "components":{"schemas":{"Form":{"type":"object","properties":{"id":{"type":"string"},"title":{"type":"string"},"state":{"type":"string"},"auditTrail":{"type":"array"}}}}}}`

func good() map[string]any {
	return map[string]any{
		"id": "acme/oa", "version": "1.2.0", "name": "Acme OA", "description": "查询和提交 OA 审批单", "kind": "declarative",
		"openapi": "openapi.json", "servers": []any{"https://oa.acme.internal/api"},
		"credential": map[string]any{"mode": "ee_jwt"},
		"tools": []any{
			map[string]any{"name": "list_forms", "readOnly": true, "keep": []any{"id", "title", "state"}, "params": []any{
				map[string]any{"name": "status", "fill": "model"},
				map[string]any{"name": "mine", "fill": "app", "label": "只看自己的", "default": true},
			}},
			map[string]any{"name": "get_form", "readOnly": true},
			map[string]any{"name": "submit_leave", "params": []any{
				map[string]any{"name": "body.tenant", "fill": "admin", "label": "租户", "default": "east-china"},
				map[string]any{"name": "body.days", "description": "请几天"},
			}},
		},
	}
}

func files(m any, extra map[string]string) fstest.MapFS {
	raw, ok := m.(string)
	if !ok {
		b, _ := json.Marshal(m)
		raw = string(b)
	}
	fsys := fstest.MapFS{ManifestFile: {Data: []byte(raw)}, "openapi.json": {Data: []byte(goodDoc)}}
	for name, data := range extra {
		if data == "" {
			delete(fsys, name)
		} else {
			fsys[name] = &fstest.MapFile{Data: []byte(data)}
		}
	}
	return fsys
}

func TestGoodManifest(t *testing.T) {
	res := Check(files(good(), nil))
	if !res.OK || len(res.Results) != 0 {
		t.Fatalf("应一条都不报：%+v", res.Results)
	}
	// 清单里没提到的接口按接口文档的默认：DELETE 和 POST 不是只读
	var order []string
	for _, tool := range res.Tools {
		order = append(order, tool.Name)
		want := tool.Name == "list_forms" || tool.Name == "get_form"
		if tool.ReadOnly != want {
			t.Fatalf("%s 的只读标记不对：%+v", tool.Name, tool)
		}
	}
	// 结果里把凭据、不由模型填的参数、保留的字段照实际生效的样子写出来，给人和 Agent 核对
	if cr := res.Credential; cr == nil || cr.Mode != CredEEJWT || cr.Header != "Authorization: Bearer" {
		t.Fatalf("凭据：%+v", res.Credential)
	}
	first, third := res.Tools[0], res.Tools[2]
	if !slices.Equal(first.Keep, []string{"id", "title", "state"}) || len(first.Fixed) != 1 || first.Fixed[0].Name != "mine" || first.Fixed[0].Fill != FillApp || string(first.Fixed[0].Default) != "true" {
		t.Fatalf("list_forms 的保留字段和固定参数：%+v", first)
	}
	if len(third.Fixed) != 1 || third.Fixed[0].Name != "body.tenant" || third.Fixed[0].Fill != FillAdmin || third.Fixed[0].Label != "租户" || string(third.Fixed[0].Default) != `"east-china"` {
		t.Fatalf("submit_leave 的固定参数：%+v", third)
	}
	// 清单里提到的按清单的顺序在前，其余按接口文档的顺序
	if !slices.Equal(order, []string{"list_forms", "get_form", "submit_leave", "cancel_form", "search_forms"}) {
		t.Fatalf("工具的顺序：%v", order)
	}
}

// 把清单弄坏成各种样子，每一种都要报出对应的检查项，位置和改法都不能空。
func TestCheckReports(t *testing.T) {
	set := func(path string, v any) func(map[string]any) {
		return func(m map[string]any) {
			keys := strings.Split(path, ".")
			cur := m
			for _, k := range keys[:len(keys)-1] {
				next, _ := cur[k].(map[string]any)
				if next == nil {
					next = map[string]any{}
					cur[k] = next
				}
				cur = next
			}
			if v == nil {
				delete(cur, keys[len(keys)-1])
			} else {
				cur[keys[len(keys)-1]] = v
			}
		}
	}
	tool := func(i int, k string, v any) func(map[string]any) {
		return func(m map[string]any) { m["tools"].([]any)[i].(map[string]any)[k] = v }
	}
	service := func(m map[string]any) {
		m["kind"] = "service"
		delete(m, "openapi")
		delete(m, "servers")
		m["service"] = map[string]any{"url": "https://oa-ext.acme.internal"}
	}
	cases := []struct {
		name  string
		code  string
		where string
		edit  func(map[string]any)
		raw   string
		extra map[string]string
	}{
		{name: "没有清单", code: "manifest.missing", where: ManifestFile, extra: map[string]string{ManifestFile: ""}},
		{name: "不是 JSON", code: "manifest.invalid_json", where: ManifestFile, raw: `{"id": "acme/oa",}`},
		{name: "最外层是数组", code: "manifest.invalid_json", where: ManifestFile, raw: `[]`},
		{name: "类型不对", code: "manifest.type_mismatch", where: "#/tools", edit: set("tools", "list_forms")},
		{name: "拼错的字段", code: "manifest.unknown_field", where: "#/tools/0/readonly", edit: tool(0, "readonly", true)},
		{name: "顶层拼错的字段", code: "manifest.unknown_field", where: "#/server", edit: set("server", []any{"https://x"})},
		{name: "id 大写", code: "id.invalid", where: "#/id", edit: set("id", "Acme/OA")},
		{name: "id 没有作者", code: "id.invalid", where: "#/id", edit: set("id", "oa")},
		{name: "版本号不对", code: "version.invalid", where: "#/version", edit: set("version", "1.2")},
		{name: "最低版本不对", code: "min_version.invalid", where: "#/minVersion", edit: set("minVersion", "v0.2")},
		{name: "没有名字", code: "name.missing", where: "#/name", edit: set("name", nil)},
		{name: "没有说明", code: "description.missing", where: "#/description", edit: set("description", nil)},
		{name: "形式不对", code: "kind.invalid", where: "#/kind", edit: set("kind", "plugin")},
		{name: "声明式却写了 service", code: "kind.field_mismatch", where: "#/kind", edit: set("service", map[string]any{"url": "https://x.example"})},
		{name: "服务扩展却写了 servers", code: "kind.field_mismatch", where: "#/kind", edit: func(m map[string]any) { service(m); m["servers"] = []any{"https://x.example"} }},
		{name: "服务扩展只查清单", code: "kind.not_checked", where: "#/kind", edit: service},
		{name: "仓库地址不对", code: "repo.invalid", where: "#/repo", edit: set("repo", "acme/dsh-ee-oa")},
		{name: "没指明接口文档", code: "openapi.missing", where: "#/openapi", edit: set("openapi", nil)},
		{name: "接口文档找不到", code: "openapi.missing", where: "#/openapi", extra: map[string]string{"openapi.json": ""}},
		{name: "接口文档路径跑到目录外", code: "openapi.missing", where: "#/openapi", edit: set("openapi", "../openapi.json")},
		{name: "接口文档读不了", code: "openapi.invalid", where: "openapi.json", extra: map[string]string{"openapi.json": `{"swagger":"2.0"}`}},
		{name: "没列服务器地址", code: "servers.missing", where: "#/servers", edit: set("servers", nil)},
		{name: "服务器地址不完整", code: "servers.invalid", where: "#/servers/0", edit: set("servers", []any{"oa.acme.internal"})},
		{name: "服务器地址带账号密码", code: "servers.invalid", where: "#/servers/0", edit: set("servers", []any{"https://user:pw@oa.acme.internal/api"})},
		{name: "接口文档的地址不在清单里", code: "servers.mismatch", where: "openapi.json#/servers/0/url", edit: set("servers", []any{"https://other.acme.internal"})},
		{name: "同主机但路径在外面", code: "servers.mismatch", where: "openapi.json#/servers/0/url", edit: set("servers", []any{"https://oa.acme.internal/api/v2"})},
		{name: "服务扩展没有地址", code: "service.url_invalid", where: "#/service/url", edit: func(m map[string]any) { service(m); delete(m, "service") }},
		{name: "端侧扩展包名不对", code: "package.invalid", where: "#/package", edit: func(m map[string]any) {
			m["kind"] = "local"
			delete(m, "openapi")
			delete(m, "servers")
			m["package"] = "Acme OA"
		}},
		{name: "凭据模式不对", code: "credential.mode_invalid", where: "#/credential/mode", edit: set("credential.mode", "jwt")},
		{name: "oauth 还不支持", code: "credential.unsupported", where: "#/credential/mode", edit: set("credential.mode", "oauth")},
		{name: "请求头名字不对", code: "credential.header_invalid", where: "#/credential/header", edit: set("credential.header", "X User")},
		{name: "两份凭据同一个请求头", code: "credential.header_conflict", where: "#/credential", edit: set("credential.shared", map[string]any{"type": "bearer"})},
		{name: "共享凭据类型不对", code: "credential.shared_invalid", where: "#/credential/shared/type", edit: set("credential.shared", map[string]any{"type": "basic"})},
		{name: "共享凭据请求头不对", code: "credential.shared_invalid", where: "#/credential/shared/header", edit: set("credential.shared", map[string]any{"type": "header", "header": ""})},
		{name: "带了不能带的员工信息", code: "credential.claims_invalid", where: "#/credential/claims/0", edit: set("credential.claims", []any{"salary"})},
		{name: "shared 模式又写了 shared", code: "credential.field_unused", where: "#/credential/shared", edit: set("credential", map[string]any{"mode": "shared", "shared": map[string]any{"type": "bearer"}})},
		{name: "shared 模式写了 audience", code: "credential.field_unused", where: "#/credential", edit: set("credential", map[string]any{"mode": "shared", "audience": "oa"})},
		{name: "ee_jwt 写了 hint", code: "credential.field_unused", where: "#/credential/hint", edit: set("credential.hint", "去 OA 里生成")},
		{name: "user_secret 没写去哪拿", code: "credential.hint_missing", where: "#/credential/hint", edit: set("credential", map[string]any{"mode": "user_secret"})},
		{name: "配置项 key 不对", code: "config.key_invalid", where: "#/config/0/key", edit: set("config", []any{map[string]any{"key": "base url", "type": "url", "label": "地址"}})},
		{name: "配置项重复", code: "config.key_duplicate", where: "#/config/1/key", edit: set("config", []any{map[string]any{"key": "a", "type": "text", "label": "A"}, map[string]any{"key": "a", "type": "text", "label": "A"}})},
		{name: "配置项类型不对", code: "config.type_invalid", where: "#/config/0/type", edit: set("config", []any{map[string]any{"key": "a", "type": "password", "label": "A"}})},
		{name: "配置项没有 label", code: "config.label_missing", where: "#/config/0/label", edit: set("config", []any{map[string]any{"key": "a", "type": "text"}})},
		{name: "密钥给了默认值", code: "config.secret_default", where: "#/config/0/default", edit: set("config", []any{map[string]any{"key": "apiKey", "type": "secret", "label": "Key", "default": "sk-123"}})},
		{name: "select 没有选项", code: "config.options_invalid", where: "#/config/0/options", edit: set("config", []any{map[string]any{"key": "env", "type": "select", "label": "环境"}})},
		{name: "text 写了选项", code: "config.options_invalid", where: "#/config/0/options", edit: set("config", []any{map[string]any{"key": "env", "type": "text", "label": "环境", "options": []any{"a"}}})},
		{name: "声明式用了动态选项", code: "config.dynamic_unsupported", where: "#/config/0/options", edit: set("config", []any{map[string]any{"key": "env", "type": "select", "label": "环境", "options": "dynamic"}})},
		{name: "不存在的权限", code: "permissions.invalid", where: "#/permissions/files", edit: func(m map[string]any) { service(m); m["permissions"] = map[string]any{"files": true} }},
		{name: "kb 权限写成 write", code: "permissions.invalid", where: "#/permissions/kb", edit: func(m map[string]any) { service(m); m["permissions"] = map[string]any{"kb": "write"} }},
		{name: "声明式申请了权限", code: "permissions.unused", where: "#/permissions", edit: set("permissions", map[string]any{"kb": "read"})},
		{name: "不存在的能力", code: "capabilities.invalid", where: "#/capabilities/0", edit: set("capabilities", []any{"models"})},
		{name: "声明式做不了知识源", code: "capabilities.invalid", where: "#/capabilities/1", edit: set("capabilities", []any{"tools", "knowledge.search"})},
		{name: "工具名不对", code: "tool.name_invalid", where: "#/tools/0/name", edit: tool(0, "name", "list forms")},
		{name: "工具重复", code: "tool.duplicate", where: "#/tools/1/name", edit: tool(1, "name", "list_forms")},
		{name: "接口文档里没有这个工具", code: "tool.unknown", where: "#/tools/0/name", edit: tool(0, "name", "list_form")},
		{name: "POST 标成只读", code: "tool.readonly_suspect", where: "#/tools/2", edit: tool(2, "readOnly", true)},
		{name: "服务扩展里名字像写操作却标只读", code: "tool.readonly_suspect", where: "#/tools/0", edit: func(m map[string]any) {
			service(m)
			m["tools"] = []any{map[string]any{"name": "deleteForm", "readOnly": true}}
		}},
		{name: "写明理由后 POST 当作只读，但留在结果里", code: "tool.readonly_override", where: "#/tools/3", edit: func(m map[string]any) {
			m["tools"] = append(m["tools"].([]any), map[string]any{"name": "search_forms", "readOnly": true})
			m["checkIgnore"] = []any{map[string]any{"code": "tool.readonly_suspect", "tool": "search_forms", "reason": "只查询"}}
		}},
		{name: "GET 没标只读", code: "tool.readonly_unmarked", where: "#/tools/1", edit: tool(1, "readOnly", false)},
		{name: "参数没有名字", code: "param.name_missing", where: "#/tools/0/params/0/name", edit: tool(0, "params", []any{map[string]any{"fill": "model"}})},
		{name: "参数重复", code: "param.duplicate", where: "#/tools/0/params/1/name", edit: tool(0, "params", []any{map[string]any{"name": "status"}, map[string]any{"name": "status"}})},
		{name: "接口没有这个参数", code: "param.unknown", where: "#/tools/0/params/0/name", edit: tool(0, "params", []any{map[string]any{"name": "state"}})},
		{name: "请求体里的字段没写 body. 前缀", code: "param.unknown", where: "#/tools/2/params/0/name", edit: tool(2, "params", []any{map[string]any{"name": "days"}})},
		{name: "请求体里没有这个字段", code: "param.unknown", where: "#/tools/2/params/0/name", edit: tool(2, "params", []any{map[string]any{"name": "body.hours"}})},
		{name: "模型填的参数写了默认值", code: "param.default_unused", where: "#/tools/0/params/0/default", edit: tool(0, "params", []any{map[string]any{"name": "status", "default": "pending"}})},
		{name: "布尔参数的默认值写成了字符串", code: "param.default_type", where: "#/tools/0/params/0/default", edit: tool(0, "params", []any{map[string]any{"name": "mine", "fill": "admin", "label": "只看自己的", "default": "true"}})},
		{name: "请求体字段的默认值类型不对", code: "param.default_type", where: "#/tools/2/params/0/default", edit: tool(2, "params", []any{map[string]any{"name": "body.days", "fill": "admin", "label": "天数", "default": "3"}})},
		{name: "同一个管理员参数默认值不一致", code: "param.default_conflict", where: "#/tools/2/params/0/default", edit: func(m map[string]any) {
			tool(0, "params", []any{map[string]any{"name": "status", "fill": "admin", "label": "状态", "default": "pending"}})(m)
			tool(2, "params", []any{map[string]any{"name": "body.status", "fill": "admin", "label": "状态", "default": "done"}})(m)
		}},
		{name: "keep 里写了返回里没有的字段", code: "keep.unknown", where: "#/tools/0/keep/1", edit: tool(0, "keep", []any{"id", "status"})},
		{name: "fill 不对", code: "param.fill_invalid", where: "#/tools/0/params/0/fill", edit: tool(0, "params", []any{map[string]any{"name": "status", "fill": "user"}})},
		{name: "参数类型不对", code: "param.type_invalid", where: "#/tools/0/params/0/type", edit: tool(0, "params", []any{map[string]any{"name": "status", "type": "text"}})},
		{name: "人填的参数没有 label", code: "param.label_missing", where: "#/tools/0/params/0/label", edit: tool(0, "params", []any{map[string]any{"name": "status", "fill": "app"}})},
		{name: "附带的应用找不到", code: "apps.missing", where: "#/apps/0", edit: set("apps", []any{"apps/oa-helper.yaml"})},
		{name: "图标找不到", code: "icon.missing", where: "#/icon", edit: set("icon", "icon.svg")},
		{name: "扩展字段还不处理", code: "fields.not_yet", where: "#/fields", edit: set("fields", []any{map[string]any{"key": "project"}})},
		{name: "忽略不存在的检查项", code: "ignore.invalid", where: "#/checkIgnore/0/code", edit: set("checkIgnore", []any{map[string]any{"code": "tool.readonly", "reason": "x"}})},
		{name: "忽略不能忽略的检查项", code: "ignore.invalid", where: "#/checkIgnore/0/code", edit: set("checkIgnore", []any{map[string]any{"code": "tool.unknown", "reason": "x"}})},
		{name: "忽略没写理由", code: "ignore.invalid", where: "#/checkIgnore/0/reason", edit: func(m map[string]any) {
			tool(2, "readOnly", true)(m)
			m["checkIgnore"] = []any{map[string]any{"code": "tool.readonly_suspect", "reason": " "}}
		}},
		{name: "列表包了一层，keep 却直接写每一项的字段", code: "keep.wrapped", where: "#/tools/0/keep", edit: tool(0, "keep", []any{"id", "title"}),
			extra: map[string]string{"openapi.json": strings.Replace(goodDoc, `"schema":{"type":"array","items":{"$ref":"#/components/schemas/Form"}}`,
				`"schema":{"type":"object","properties":{"total":{"type":"integer"},"items":{"type":"array","items":{"$ref":"#/components/schemas/Form"}}}}`, 1)}},
		{name: "忽略没对上任何结果", code: "ignore.unused", where: "#/checkIgnore/0", edit: set("checkIgnore", []any{map[string]any{"code": "tool.readonly_suspect", "reason": "已确认"}})},
	}
	covered := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m any = tc.raw
			if tc.raw == "" {
				g := good()
				if tc.edit != nil {
					tc.edit(g)
				}
				m = g
			}
			res := Check(files(m, tc.extra))
			var hit *Diagnostic
			for i, d := range res.Results {
				info, known := codeIndex[d.Code]
				if !known || info.Severity != d.Severity {
					t.Fatalf("报了错误码表里没有的检查项，或严重程度对不上：%+v", d)
				}
				if d.Where == "" || d.Message == "" || (d.Severity != SevInfo && d.Fix == "") {
					t.Fatalf("每条结果都要有位置、说明和改法：%+v", d)
				}
				if d.Code == tc.code && strings.HasSuffix(d.Where, tc.where) {
					hit = &res.Results[i]
				}
			}
			if hit == nil {
				t.Fatalf("应报 %s（位置以 %s 结尾），实际：%+v", tc.code, tc.where, res.Results)
			}
			covered[tc.code] = true
			if wantOK := hit.Severity != SevError; res.OK != wantOK && res.Errors == 0 {
				t.Fatalf("OK 应和有没有错误一致：%+v", res)
			}
		})
	}
	// 错误码表里的每一项都要有用例，表才不会和代码脱节
	var missing []string
	for _, c := range Codes {
		if !covered[c.Code] {
			missing = append(missing, c.Code)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("这些检查项没有用例：%v", missing)
	}
}

// 作者写明理由后，可以忽略的检查项不再报：确实只读的 POST 当作只读，但要留在结果里给装扩展的人看。
func TestCheckIgnore(t *testing.T) {
	m := good()
	m["tools"].([]any)[2].(map[string]any)["readOnly"] = true
	m["tools"] = append(m["tools"].([]any), map[string]any{"name": "search_forms", "readOnly": true})
	if res := Check(files(m, nil)); res.Errors != 2 {
		t.Fatalf("两个 POST 标成只读，应报两条：%+v", res.Results)
	}
	readOnly := func(res *Result) map[string]bool {
		out := map[string]bool{}
		for _, tool := range res.Tools {
			out[tool.Name] = tool.ReadOnly
		}
		return out
	}
	// 没写理由之前，两个都不算只读
	if ro := readOnly(Check(files(m, nil))); ro["search_forms"] || ro["submit_leave"] {
		t.Fatalf("POST 接口没有理由不能算只读：%v", ro)
	}
	// search_forms 用 POST 做查询，确实只读：按工具名、按位置（完整的 where 或只写 # 后面的部分）都能指到它
	for _, ig := range []map[string]any{
		{"tool": "search_forms"},
		{"where": "/tools/3"},
		{"where": ManifestFile + "#/tools/3"},
		{"tool": "search_forms", "where": "/tools/3"},
	} {
		ig["code"], ig["reason"] = "tool.readonly_suspect", "search 接口用 POST 传查询条件，不改数据"
		m["checkIgnore"] = []any{ig}
		res := Check(files(m, nil))
		if res.Errors != 1 || res.Results[0].Code != "tool.readonly_suspect" || !strings.HasSuffix(res.Results[0].Where, "#/tools/2") {
			t.Fatalf("%v：只应忽略写明的那一处，submit_leave 仍然要报：%+v", ig, res.Results)
		}
		var note *Diagnostic
		for i, d := range res.Results {
			if d.Code == "tool.readonly_override" {
				note = &res.Results[i]
			}
		}
		if note == nil || note.Severity != SevInfo || !strings.Contains(note.Message, "search_forms") || !strings.Contains(note.Message, "不改数据") {
			t.Fatalf("%v：当作只读要留在结果里，带上作者写的理由：%+v", ig, res.Results)
		}
		if ro := readOnly(res); !ro["search_forms"] || ro["submit_leave"] {
			t.Fatalf("%v：写了理由的当只读，没写的不算：%v", ig, ro)
		}
	}
	// 指到别的工具、别的位置：对不上，原来的错误照报，并提醒这一项没用上
	for _, ig := range []map[string]any{{"tool": "list_forms"}, {"where": "/tools/3/readOnly"}} {
		ig["code"], ig["reason"] = "tool.readonly_suspect", "x"
		m["checkIgnore"] = []any{ig}
		res := Check(files(m, nil))
		unused := slices.ContainsFunc(res.Results, func(d Diagnostic) bool { return d.Code == "ignore.unused" && d.Fix != "" })
		if res.Errors != 2 || !unused || readOnly(res)["search_forms"] {
			t.Fatalf("%v：对不上的忽略不应生效：%+v", ig, res.Results)
		}
	}
	// 「GET 没标只读」被忽略：作者确认这个 GET 会改数据，工具仍然是写操作
	m = good()
	m["tools"].([]any)[1].(map[string]any)["readOnly"] = false
	m["checkIgnore"] = []any{map[string]any{"code": "tool.readonly_unmarked", "tool": "get_form", "reason": "这个 GET 会记一次已读"}}
	if res := Check(files(m, nil)); len(res.Results) != 0 || readOnly(res)["get_form"] {
		t.Fatalf("确认会改数据的 GET 不再提醒，也不算只读：%+v", res)
	}
}

// 接口文档没写返回结构时，keep 查不了，不报；写了才查。
func TestKeepNeedsResponseSchema(t *testing.T) {
	m := good()
	m["tools"].([]any)[1].(map[string]any)["keep"] = []any{"anything"}
	if res := Check(files(m, nil)); len(res.Results) != 0 {
		t.Fatalf("get_form 的接口文档没写 responses，keep 不该报：%+v", res.Results)
	}
	if shape, known := responseShape([]byte(goodDoc), "GET", "/forms"); !known || !slices.Equal(shape.fields, []string{"auditTrail", "id", "state", "title"}) {
		t.Fatalf("list_forms 返回数组，字段取每一项的（跟着 $ref 找）：%v %v", shape, known)
	}
	if _, known := responseShape([]byte(goodDoc), "GET", "/forms/{id}"); known {
		t.Fatal("没写 responses 的接口不知道返回里有什么")
	}
	// 写法不对（超过两层）不管接口文档写没写返回结构都报
	m["tools"].([]any)[1].(map[string]any)["keep"] = []any{"a.b.c"}
	if res := Check(files(m, nil)); len(res.Results) != 1 || res.Results[0].Code != "keep.unknown" || !strings.Contains(res.Results[0].Fix, "items.id") {
		t.Fatalf("keep 最多写到第二层：%+v", res.Results)
	}
}

// 列表被包了一层（{items: [...], total}）是最常见的返回：keep 写成 items.id，直接写 id 会把返回清空。
func TestKeepWrappedList(t *testing.T) {
	const doc = `openapi: 3.0.3
info: { title: T, version: "1" }
paths:
  /tickets:
    get:
      operationId: list_tickets
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  total: { type: integer }
                  page: { type: object }
                  items:
                    type: array
                    items: { type: object, properties: { id: { type: string }, title: { type: string }, detail: { type: string } } }
`
	check := func(keep ...any) []Diagnostic {
		m := map[string]any{
			"id": "acme/t", "version": "1.0.0", "name": "T", "description": "d", "kind": "declarative",
			"openapi": "openapi.yaml", "servers": []any{"https://t.acme.internal"},
			"tools": []any{map[string]any{"name": "list_tickets", "readOnly": true, "keep": keep}},
		}
		raw, _ := json.Marshal(m)
		return Check(fstest.MapFS{ManifestFile: {Data: raw}, "openapi.yaml": {Data: []byte(doc)}}).Results
	}
	if got := check("items.id", "items.title", "total", "page.anything"); len(got) != 0 {
		t.Fatalf("items.id / total 是返回里有的；page 的结构接口文档没写，查不了不报：%+v", got)
	}
	got := check("items.id", "items.status")
	if len(got) != 1 || got[0].Where != ManifestFile+"#/tools/0/keep/1" || !strings.Contains(got[0].Fix, "detail、id、title") {
		t.Fatalf("items 里没有 status：%+v", got)
	}
	// 直接写了每一项的字段：返回会被清空。按错误报，只报一条，并照他写的字段给出改法
	got = check("id", "title")
	if len(got) != 1 || got[0].Code != "keep.wrapped" || got[0].Severity != SevError || got[0].Where != ManifestFile+"#/tools/0/keep" ||
		!strings.Contains(got[0].Message, "items") || !strings.Contains(got[0].Fix, `"items.id"`) {
		t.Fatalf("包了一层却直接写每一项的字段：%+v", got)
	}
	// 写的字段哪里都没有：这是普通的写错，照常逐条提醒
	if got = check("nothing"); len(got) != 1 || got[0].Code != "keep.unknown" {
		t.Fatalf("哪里都没有的字段：%+v", got)
	}
}

func TestText(t *testing.T) {
	var m struct {
		A, B, C Text
	}
	if err := json.Unmarshal([]byte(`{"A":"你好","B":{"zh":"你好","en":"hi"},"C":{"en":"hi"}}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.A.ZH != "你好" || m.A.EN != "" || m.B.EN != "hi" || m.C.Any() != "hi" || m.A.Any() != "你好" || (Text{}).Any() != "" || !(Text{}).Empty() {
		t.Fatalf("解析不对：%+v", m)
	}
	if json.Unmarshal([]byte(`{"A":{"zh":"x","jp":"y"}}`), &m) == nil || json.Unmarshal([]byte(`{"A":3}`), &m) == nil {
		t.Fatal("别的语言和别的类型应报错")
	}
	a, _ := json.Marshal(m.A)
	b, _ := json.Marshal(m.B)
	if string(a) != `"你好"` || string(b) != `{"zh":"你好","en":"hi"}` {
		t.Fatalf("写回去的样子不对：%s %s", a, b)
	}
}

// 规格文件和 Go 类型要一致：规格里有的字段类型里都有，反过来也一样。
func TestSchemaMatchesTypes(t *testing.T) {
	raw, ok := Schema("manifest")
	if !ok {
		t.Fatal("没有 manifest 规格")
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("规格不是合法的 JSON：%v", err)
	}
	var walk func(node map[string]any, typ reflect.Type, at string)
	walk = func(node map[string]any, typ reflect.Type, at string) {
		for typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if typ.Kind() == reflect.Slice && typ != reflect.TypeOf(json.RawMessage{}) && typ.Elem().Kind() != reflect.Interface {
			items, _ := node["items"].(map[string]any)
			if items == nil {
				t.Errorf("%s：规格里应是数组（有 items）", at)
				return
			}
			walk(items, typ.Elem(), at+"[]")
			return
		}
		if typ.Kind() != reflect.Struct || typ == reflect.TypeOf(Text{}) {
			return
		}
		props, _ := node["properties"].(map[string]any)
		if node["additionalProperties"] != false {
			t.Errorf("%s：规格里要写 additionalProperties: false", at)
		}
		var inType []string
		for i := 0; i < typ.NumField(); i++ {
			name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
			inType = append(inType, name)
			child, _ := props[name].(map[string]any)
			if child == nil {
				t.Errorf("%s：类型里有 %s，规格里没有", at, name)
				continue
			}
			if _, has := child["description"]; !has && child["$ref"] == nil && child["properties"] == nil && child["enum"] == nil && child["type"] != "boolean" && at == "manifest" {
				t.Errorf("%s.%s：顶层字段要有说明", at, name)
			}
			walk(child, typ.Field(i).Type, at+"."+name)
		}
		var inSchema []string
		for name := range props {
			inSchema = append(inSchema, name)
		}
		sort.Strings(inType)
		sort.Strings(inSchema)
		if !slices.Equal(inType, inSchema) {
			t.Errorf("%s：规格和类型的字段不一致\n  类型：%v\n  规格：%v", at, inType, inSchema)
		}
	}
	walk(schema, reflect.TypeOf(Manifest{}), "manifest")

	// 枚举值和代码里用的是同一份
	enumAt := func(path ...string) []string {
		var cur any = schema
		for _, p := range path {
			cur = cur.(map[string]any)[p]
		}
		var out []string
		for _, v := range cur.([]any) {
			out = append(out, v.(string))
		}
		return out
	}
	for _, c := range []struct {
		name string
		got  []string
		want []string
	}{
		{"kind", enumAt("properties", "kind", "enum"), kinds},
		{"credential.mode", enumAt("properties", "credential", "properties", "mode", "enum"), credModes},
		{"credential.claims", enumAt("properties", "credential", "properties", "claims", "items", "enum"), claimChoices},
		{"capabilities", enumAt("properties", "capabilities", "items", "enum"), capabilities},
		{"config.type", enumAt("properties", "config", "items", "properties", "type", "enum"), configTypes},
		{"param.fill", enumAt("properties", "tools", "items", "properties", "params", "items", "properties", "fill", "enum"), fills},
		{"param.type", enumAt("properties", "tools", "items", "properties", "params", "items", "properties", "type", "enum"), paramTypes},
	} {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%s 的枚举值：规格 %v，代码 %v", c.name, c.got, c.want)
		}
	}
	var perms []string
	for k := range schema["properties"].(map[string]any)["permissions"].(map[string]any)["properties"].(map[string]any) {
		perms = append(perms, k)
	}
	sort.Strings(perms)
	want := slices.Clone(permissions)
	sort.Strings(want)
	if !slices.Equal(perms, want) {
		t.Errorf("permissions：规格 %v，代码 %v", perms, want)
	}
}

// 仓库里公开的那份规格和编译进二进制的是同一份。
func TestPublishedSchemaInSync(t *testing.T) {
	embedded, _ := Schema("manifest")
	published, err := os.ReadFile("../../../extensions/schemas/manifest.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(embedded, published) {
		t.Fatal("extensions/schemas/manifest.schema.json 和 server/internal/extension/schemas/ 里的不一样：改规格时两处一起改")
	}
}

func TestSchemas(t *testing.T) {
	list := Schemas()
	if len(list) != 2 || list[0].Name != "errors" || list[1].Name != "manifest" || list[1].Title == "" {
		t.Fatalf("规格列表：%+v", list)
	}
	raw, ok := Schema("errors")
	var doc struct {
		Codes []CodeInfo
	}
	if !ok || json.Unmarshal(raw, &doc) != nil || len(doc.Codes) != len(Codes) {
		t.Fatalf("错误码规格：%s", raw)
	}
	for _, name := range []string{"nope", "../manifest", "manifest.schema", ""} {
		if _, ok := Schema(name); ok {
			t.Fatalf("不存在的规格 %q 不应取到", name)
		}
	}
	seen := map[string]bool{}
	for _, c := range Codes {
		if seen[c.Code] || c.Meaning == "" || !slices.Contains([]string{SevError, SevWarning, SevInfo}, c.Severity) {
			t.Fatalf("错误码表里的 %s 重复或不完整", c.Code)
		}
		seen[c.Code] = true
	}
}

// shared 模式下，header 说的是共享凭据放在哪个请求头：对方要求 X-API-Key 是最常见的情况，要能写在清单里。
func TestSharedCredentialHeader(t *testing.T) {
	m := good()
	m["credential"] = map[string]any{"mode": "shared", "header": "x-api-key"}
	res := Check(files(m, nil))
	if len(res.Results) != 0 || res.Credential == nil || res.Credential.SharedHeader != "X-Api-Key" || res.Credential.Header != "" {
		t.Fatalf("shared 模式写 header 应当通过，并在结果里写明放在哪：%+v %+v", res.Results, res.Credential)
	}
	m["credential"] = map[string]any{"mode": "shared"}
	if res = Check(files(m, nil)); res.Credential.SharedHeader != "Authorization: Bearer" {
		t.Fatalf("不写 header 就是 Authorization: Bearer：%+v", res.Credential)
	}
}
