package extension

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing/fstest"

	"github.com/hikariming/dsh-ee/server/internal/openapi"
)

// 这个文件是「装」的那一半：检查通过之后，把清单和接口文档变成 dsh-ee 真正拿去用的东西——
// 给模型看的工具定义、调用时要补上的参数、返回里要留的字段、安装表单里要管理员填的项。
// 仍然不联网、不运行任何东西；发请求在 api 包里。

// CheckInstalled 检查一份已经装进来的声明式扩展。装进来之后只存清单和接口文档，
// 图标、附带的应用这些文件在安装前已经查过，这里当它们都在。
func CheckInstalled(manifest, openapiDoc []byte) *Result {
	files := fstest.MapFS{ManifestFile: &fstest.MapFile{Data: manifest}}
	var m struct {
		OpenAPI string   `json:"openapi"`
		Icon    string   `json:"icon"`
		Apps    []string `json:"apps"`
	}
	_ = json.Unmarshal(manifest, &m)
	if m.OpenAPI != "" && m.OpenAPI != ManifestFile && fs.ValidPath(m.OpenAPI) {
		files[m.OpenAPI] = &fstest.MapFile{Data: openapiDoc}
	}
	for _, p := range append(m.Apps, m.Icon) {
		if p != "" && fs.ValidPath(p) && files[p] == nil {
			files[p] = &fstest.MapFile{}
		}
	}
	return Check(files)
}

// BoundParam 是一个不由模型填的参数，调用时由 dsh-ee 补上。
type BoundParam struct {
	// 清单里写的名字：路径 / 查询参数名，或 body.<字段名>
	Name string `json:"name"`
	// 取值用的名字（去掉 body.）；同一个 Key 在几个工具上共用一个值
	Key  string `json:"key"`
	Fill string `json:"fill"`
	// 接口要求必填：没有值就不能调
	Required bool `json:"required,omitempty"`
}

// BoundTool 是装好之后的一个工具。
type BoundTool struct {
	Name        string
	Description string
	// 给模型看的参数定义：不由模型填的参数已经拿掉，清单里补充的说明、类型、可选值已经写进去
	InputSchema json.RawMessage
	Op          openapi.Operation
	ReadOnly    bool
	Fixed       []BoundParam
	// 返回结果只保留这些字段；空表示原样返回
	Keep []string
}

// Bind 把一份检查通过的声明式扩展变成工具。res 要是 Check / CheckInstalled 的结果。
func Bind(res *Result) ([]BoundTool, error) {
	if res == nil || res.Manifest == nil || !res.OK {
		return nil, fmt.Errorf("扩展没有通过检查")
	}
	m := res.Manifest
	if m.Kind != KindDeclarative {
		return nil, fmt.Errorf("只有声明式扩展的工具来自接口文档")
	}
	spec, err := openapi.Parse(res.OpenAPIDoc)
	if err != nil {
		return nil, fmt.Errorf("解析接口文档：%w", err)
	}
	ops := map[string]*openapi.Tool{}
	for i := range spec.Tools {
		ops[spec.Tools[i].Name] = &spec.Tools[i]
	}
	listed := map[string]*Tool{}
	for i := range m.Tools {
		if listed[m.Tools[i].Name] == nil {
			listed[m.Tools[i].Name] = &m.Tools[i]
		}
	}
	out := make([]BoundTool, 0, len(res.Tools))
	for _, info := range res.Tools {
		op := ops[info.Name]
		if op == nil {
			continue
		}
		bt := BoundTool{Name: op.Name, Description: op.Description, InputSchema: op.InputSchema, Op: op.Op, ReadOnly: info.ReadOnly}
		if t := listed[info.Name]; t != nil {
			if d := t.Description.Any(); d != "" {
				bt.Description = d
			}
			bt.Keep = t.Keep
			if bt.InputSchema, bt.Fixed, err = bindSchema(op.InputSchema, t); err != nil {
				return nil, fmt.Errorf("工具 %s：%w", t.Name, err)
			}
		}
		out = append(out, bt)
	}
	return out, nil
}

// bindSchema 按清单改写一个工具的参数定义：由人填的参数拿掉（模型看不到也改不了），
// 由模型填的参数写上清单补充的说明、类型和可选值。
func bindSchema(raw json.RawMessage, t *Tool) (json.RawMessage, []BoundParam, error) {
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, nil, err
	}
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		return raw, nil, nil
	}
	body, _ := props["body"].(map[string]any)
	bodyProps, _ := body["properties"].(map[string]any)
	var fixed []BoundParam
	hidBody := false
	for _, p := range t.Params {
		in, name, holder := props, p.Name, schema
		if field, ok := strings.CutPrefix(p.Name, "body."); ok {
			in, name, holder = bodyProps, field, body
		}
		cur, ok := in[name].(map[string]any)
		if !ok {
			continue
		}
		required := stringList(holder["required"])
		if p.Fill == FillApp || p.Fill == FillAdmin {
			fixed = append(fixed, BoundParam{Name: p.Name, Key: name, Fill: p.Fill, Required: slices.Contains(required, name)})
			delete(in, name)
			setRequired(holder, slices.DeleteFunc(required, func(s string) bool { return s == name }))
			hidBody = hidBody || name != p.Name
			continue
		}
		if p.Type != "" {
			cur["type"] = p.Type
		}
		if d := p.Description.Any(); d != "" {
			cur["description"] = d
		}
		if len(p.Enum) > 0 {
			cur["enum"] = p.Enum
		}
		if p.Required && !slices.Contains(required, name) {
			setRequired(holder, append(required, name))
		}
	}
	// 请求体里的字段全由人填：模型不用再传 body
	if hidBody && len(bodyProps) == 0 {
		delete(props, "body")
		setRequired(schema, slices.DeleteFunc(stringList(schema["required"]), func(s string) bool { return s == "body" }))
	}
	out, err := json.Marshal(schema)
	return out, fixed, err
}

func stringList(v any) []string {
	xs, _ := v.([]any)
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func setRequired(holder map[string]any, required []string) {
	if len(required) == 0 {
		delete(holder, "required")
		return
	}
	slices.Sort(required)
	holder["required"] = required
}

// FormParam 是安装时由人来填的一个参数（fill 是 admin 或 app）。同一个参数在几个工具上出现时只有一项。
type FormParam struct {
	Key         string `json:"key"`
	Fill        string `json:"fill"`
	Label       Text   `json:"label"`
	Description Text   `json:"description,omitempty"`
	Type        string `json:"type,omitempty"`
	// 可选的值；空表示随便填
	Choices  []any           `json:"choices,omitempty"`
	Default  json.RawMessage `json:"default,omitempty"`
	Required bool            `json:"required"`
	// 用到它的工具
	Tools []string `json:"tools"`
}

// FormParams 列出安装时要人填的参数，按清单里出现的顺序。
func FormParams(res *Result) []FormParam {
	if res == nil || res.Manifest == nil {
		return nil
	}
	ops := map[string]*openapi.Tool{}
	if spec, err := openapi.Parse(res.OpenAPIDoc); err == nil {
		for i := range spec.Tools {
			ops[spec.Tools[i].Name] = &spec.Tools[i]
		}
	}
	var out []FormParam
	at := map[string]int{}
	for i := range res.Manifest.Tools {
		t := &res.Manifest.Tools[i]
		op := ops[t.Name]
		var fixed []BoundParam
		if op != nil {
			_, fixed, _ = bindSchema(op.InputSchema, t)
		}
		for _, p := range t.Params {
			if p.Fill != FillApp && p.Fill != FillAdmin {
				continue
			}
			key := strings.TrimPrefix(p.Name, "body.")
			required := slices.ContainsFunc(fixed, func(b BoundParam) bool { return b.Name == p.Name && b.Required })
			if j, ok := at[key]; ok {
				out[j].Required = out[j].Required || required
				if !slices.Contains(out[j].Tools, t.Name) {
					out[j].Tools = append(out[j].Tools, t.Name)
				}
				continue
			}
			fp := FormParam{Key: key, Fill: p.Fill, Label: p.Label, Description: p.Description, Type: paramType(p, op), Choices: p.Enum, Required: required, Tools: []string{t.Name}}
			if len(p.Default) > 0 && string(p.Default) != "null" {
				fp.Default = p.Default
			}
			if len(fp.Choices) == 0 && len(p.Options) > 0 {
				_ = json.Unmarshal(p.Options, &fp.Choices)
			}
			at[key] = len(out)
			out = append(out, fp)
		}
	}
	return out
}

// CheckValues 核对安装时填的值：只能填清单里要人填的参数，类型和可选值要对，必填的不能空。
// 没填的用清单里预先填好的值补上。返回规范化后的值和第一个问题（给填的人看）。
func CheckValues(params []FormParam, values map[string]json.RawMessage) (map[string]json.RawMessage, string) {
	known := map[string]bool{}
	for _, p := range params {
		known[p.Key] = true
	}
	for k := range values {
		if !known[k] {
			return nil, fmt.Sprintf("这个扩展没有要填的参数 %s", k)
		}
	}
	out := map[string]json.RawMessage{}
	for _, p := range params {
		name := p.Label.Any()
		if name == "" {
			name = p.Key
		}
		v := bytes.TrimSpace(values[p.Key])
		if blank(v) {
			v = bytes.TrimSpace(p.Default)
		}
		if blank(v) {
			if p.Required {
				return nil, fmt.Sprintf("「%s」必填：接口要求带这个参数，而它不由模型填", name)
			}
			continue
		}
		if p.Type != "" && !jsonIs(json.RawMessage(v), p.Type) {
			return nil, fmt.Sprintf("「%s」要填 %s 类型的值", name, p.Type)
		}
		if len(p.Choices) > 0 && !slices.ContainsFunc(p.Choices, func(c any) bool { return sameJSON(c, v) }) {
			return nil, fmt.Sprintf("「%s」只能从给出的选项里选", name)
		}
		out[p.Key] = json.RawMessage(v)
	}
	if len(out) == 0 {
		return nil, ""
	}
	return out, ""
}

func blank(v []byte) bool {
	return len(v) == 0 || string(v) == "null" || string(v) == `""`
}

func sameJSON(choice any, raw []byte) bool {
	a, err := json.Marshal(choice)
	if err != nil {
		return false
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	b, _ := json.Marshal(v)
	return bytes.Equal(a, b)
}
