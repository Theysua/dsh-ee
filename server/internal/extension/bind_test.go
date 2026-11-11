package extension

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

const bindOpenAPI = `openapi: 3.0.3
info: { title: OA, version: "1" }
paths:
  /forms:
    get:
      operationId: list_forms
      summary: 列出审批单
      parameters:
        - { name: status, in: query, schema: { type: string } }
        - { name: tenant, in: query, required: true, schema: { type: string } }
        - { name: page_size, in: query, schema: { type: integer } }
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: array
                items: { type: object, properties: { id: { type: string }, title: { type: string }, raw: { type: string } } }
  /forms/search:
    post:
      operationId: search_forms
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [tenant, keyword]
              properties:
                tenant: { type: string }
                keyword: { type: string }
  /leave:
    post:
      operationId: submit_leave
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [company]
              properties:
                company: { type: string }
  /ping:
    get:
      operationId: ping
`

const bindManifest = `{
  "id": "acme/oa", "version": "1.2.0", "name": "Acme OA", "description": "查审批单",
  "kind": "declarative", "openapi": "openapi.yaml", "servers": ["https://oa.acme.internal/api"],
  "credential": { "mode": "ee_jwt" },
  "tools": [
    { "name": "list_forms", "readOnly": true, "description": "列出当前员工的审批单",
      "params": [
        { "name": "status", "enum": ["pending", "done"], "description": "只看这个状态的" },
        { "name": "tenant", "fill": "admin", "label": "租户" },
        { "name": "page_size", "fill": "app", "label": "每页条数", "default": 20 }
      ],
      "keep": ["id", "title"] },
    { "name": "search_forms", "readOnly": true,
      "params": [ { "name": "body.tenant", "fill": "admin", "label": "租户" } ] },
    { "name": "submit_leave",
      "params": [ { "name": "body.company", "fill": "admin", "label": "公司代码", "default": "A01", "enum": ["A01", "B02"] } ] }
  ],
  "checkIgnore": [ { "code": "tool.readonly_suspect", "tool": "search_forms", "reason": "用 POST 传查询条件，不改数据" } ]
}`

func bound(t *testing.T) (*Result, map[string]BoundTool) {
	t.Helper()
	res := CheckInstalled([]byte(bindManifest), []byte(bindOpenAPI))
	if !res.OK {
		t.Fatalf("样例没通过检查：%+v", res.Results)
	}
	tools, err := Bind(res)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]BoundTool{}
	for _, tl := range tools {
		out[tl.Name] = tl
	}
	return res, out
}

func schemaOf(t *testing.T, raw json.RawMessage) (props map[string]any, required []string) {
	t.Helper()
	var s struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s.Properties, s.Required
}

func TestBind(t *testing.T) {
	_, tools := bound(t)
	if len(tools) != 4 {
		t.Fatalf("接口文档里有 4 个接口，装出来 %d 个工具", len(tools))
	}

	lf := tools["list_forms"]
	if !lf.ReadOnly || lf.Description != "列出当前员工的审批单" || !slices.Equal(lf.Keep, []string{"id", "title"}) {
		t.Fatalf("list_forms 没照清单来：%+v", lf)
	}
	props, required := schemaOf(t, lf.InputSchema)
	if _, ok := props["tenant"]; ok {
		t.Fatal("管理员设置的参数 tenant 不该出现在给模型的定义里")
	}
	if _, ok := props["page_size"]; ok {
		t.Fatal("应用固定的参数 page_size 不该出现在给模型的定义里")
	}
	if slices.Contains(required, "tenant") {
		t.Fatal("拿掉的参数还留在 required 里，模型会被要求填一个它看不到的参数")
	}
	status, _ := props["status"].(map[string]any)
	if status["description"] != "只看这个状态的" || len(status["enum"].([]any)) != 2 {
		t.Fatalf("模型填的参数没写上清单补充的说明和可选值：%v", status)
	}
	want := []BoundParam{{Name: "tenant", Key: "tenant", Fill: FillAdmin, Required: true}, {Name: "page_size", Key: "page_size", Fill: FillApp}}
	if !slices.Equal(lf.Fixed, want) {
		t.Fatalf("list_forms 要补的参数 = %+v", lf.Fixed)
	}

	// 请求体里的字段：拿掉一个，剩下的还由模型填
	sf := tools["search_forms"]
	if !sf.ReadOnly {
		t.Fatal("作者写明理由的 POST 应当按只读处理")
	}
	props, required = schemaOf(t, sf.InputSchema)
	body, _ := props["body"].(map[string]any)
	if body == nil || !slices.Contains(required, "body") {
		t.Fatalf("search_forms 还有模型要填的请求体字段，body 应当保留：%s", sf.InputSchema)
	}
	if _, ok := body["properties"].(map[string]any)["tenant"]; ok {
		t.Fatal("请求体里由管理员设置的字段不该给模型看")
	}
	if got := stringList(body["required"]); !slices.Equal(got, []string{"keyword"}) {
		t.Fatalf("请求体的 required = %v", got)
	}
	if !slices.Equal(sf.Fixed, []BoundParam{{Name: "body.tenant", Key: "tenant", Fill: FillAdmin, Required: true}}) {
		t.Fatalf("search_forms 要补的参数 = %+v", sf.Fixed)
	}

	// 请求体的字段全由人填：模型不用传 body
	sl := tools["submit_leave"]
	if sl.ReadOnly {
		t.Fatal("没标 readOnly 的工具是写操作")
	}
	props, required = schemaOf(t, sl.InputSchema)
	if _, ok := props["body"]; ok || slices.Contains(required, "body") {
		t.Fatalf("请求体里没有模型能填的字段了，不该再要求模型传 body：%s", sl.InputSchema)
	}

	// 清单没提到的接口照接口文档：GET 算只读，参数原样
	if p := tools["ping"]; !p.ReadOnly || len(p.Fixed) != 0 || p.Keep != nil {
		t.Fatalf("清单没提到的工具 = %+v", p)
	}
}

func TestBindRefusesFailedCheck(t *testing.T) {
	res := CheckInstalled([]byte(strings.Replace(bindManifest, `"acme/oa"`, `"Acme OA"`, 1)), []byte(bindOpenAPI))
	if res.OK {
		t.Fatal("id 不对的清单不该通过")
	}
	if _, err := Bind(res); err == nil {
		t.Fatal("没通过检查的扩展不能装出工具")
	}
}

func TestFormParamsAndValues(t *testing.T) {
	res, _ := bound(t)
	params := FormParams(res)
	if len(params) != 3 {
		t.Fatalf("要人填的参数应当是 3 项（tenant 在两个工具上只算一项），得到 %+v", params)
	}
	tenant, page, company := params[0], params[1], params[2]
	if tenant.Key != "tenant" || !tenant.Required || !slices.Equal(tenant.Tools, []string{"list_forms", "search_forms"}) || tenant.Type != "string" {
		t.Fatalf("tenant = %+v", tenant)
	}
	if page.Key != "page_size" || page.Fill != FillApp || page.Required || string(page.Default) != "20" || page.Type != "integer" {
		t.Fatalf("page_size = %+v", page)
	}
	if company.Key != "company" || !company.Required || len(company.Choices) != 2 {
		t.Fatalf("company = %+v", company)
	}

	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	cases := []struct {
		name   string
		values map[string]json.RawMessage
		want   string // 错误里应当出现的字；空表示应当通过
	}{
		{"必填的没填", nil, "租户"},
		{"填了空字符串也算没填", map[string]json.RawMessage{"tenant": raw(`""`)}, "必填"},
		{"类型不对", map[string]json.RawMessage{"tenant": raw(`"t1"`), "page_size": raw(`"20"`)}, "integer"},
		{"不在选项里", map[string]json.RawMessage{"tenant": raw(`"t1"`), "company": raw(`"C03"`)}, "选项"},
		{"清单里没有的参数", map[string]json.RawMessage{"tenant": raw(`"t1"`), "status": raw(`"done"`)}, "status"},
		{"通过", map[string]json.RawMessage{"tenant": raw(`"t1"`), "company": raw(`"B02"`)}, ""},
	}
	for _, c := range cases {
		got, msg := CheckValues(params, c.values)
		if c.want == "" {
			if msg != "" {
				t.Fatalf("%s：不该报错：%s", c.name, msg)
			}
			// 没填的用清单预先填好的值补上；填了的用填的
			if string(got["tenant"]) != `"t1"` || string(got["page_size"]) != "20" || string(got["company"]) != `"B02"` {
				t.Fatalf("%s：规范化后的值 = %v", c.name, got)
			}
			continue
		}
		if !strings.Contains(msg, c.want) {
			t.Fatalf("%s：错误应当提到 %q，得到 %q", c.name, c.want, msg)
		}
	}
}

// 仓库里的声明式示例装得出来：这是给写扩展的人照着抄的，不能只是「检查通过」。
func TestBindExample(t *testing.T) {
	dir := "../../../extensions/examples/demo-declarative/"
	manifest, err := os.ReadFile(dir + ManifestFile)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile(dir + "openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	res := CheckInstalled(manifest, doc)
	if !res.OK {
		t.Fatalf("示例没通过检查：%+v", res.Results)
	}
	tools, err := Bind(res)
	if err != nil {
		t.Fatal(err)
	}
	var create *BoundTool
	for i := range tools {
		if tools[i].Name == "create_ticket" {
			create = &tools[i]
		}
	}
	if create == nil || create.ReadOnly {
		t.Fatalf("示例里的 create_ticket 应当是写操作：%+v", create)
	}
	props, _ := schemaOf(t, create.InputSchema)
	body, _ := props["body"].(map[string]any)
	if _, ok := body["properties"].(map[string]any)["priority"]; ok {
		t.Fatal("示例里 priority 由管理员设置，不该给模型看")
	}
	params := FormParams(res)
	if len(params) != 1 || params[0].Key != "priority" || string(params[0].Default) != `"中"` {
		t.Fatalf("示例要人填的参数 = %+v", params)
	}
	if vals, msg := CheckValues(params, nil); msg != "" || string(vals["priority"]) != `"中"` {
		t.Fatalf("示例不填任何值也应当能装（用预先填好的值）：%v %s", vals, msg)
	}
}
