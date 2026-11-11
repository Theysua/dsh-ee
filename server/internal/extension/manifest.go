// Package extension 是 dsh-ee 扩展（docs/plugins.md）的清单：类型、静态检查、规格文件。
//
// 这里只有「不运行任何东西」的那部分：读清单和接口文档，判断它是不是一个合格的扩展，
// 说清楚哪里错、怎么改（给人看，也给 Agent 看）。服务端装扩展、命令行 `dsh-ee extension check`
// 用的是同一份代码。
package extension

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// ManifestFile 是清单的文件名。
const ManifestFile = "dsh-ee.extension.json"

// 扩展的三种形式（docs/plugins.md §4）。内置扩展写在 dsh-ee 里，没有清单文件。
const (
	KindDeclarative = "declarative"
	KindService     = "service"
	KindLocal       = "local"
)

// 凭据模式（docs/plugins.md §7.2）。
const (
	CredShared     = "shared"
	CredUserSecret = "user_secret"
	CredEEJWT      = "ee_jwt"
	CredOAuth      = "oauth"
)

// 参数由谁填（docs/plugins.md §6.1）。
const (
	FillModel = "model"
	FillApp   = "app"
	FillAdmin = "admin"
)

// Text 是给人看的文字：可以写一个字符串，也可以写 {zh, en}。
type Text struct {
	ZH string `json:"zh,omitempty"`
	EN string `json:"en,omitempty"`
}

func (t *Text) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		t.ZH, t.EN = s, ""
		return nil
	}
	var both struct {
		ZH *string `json:"zh"`
		EN *string `json:"en"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&both); err != nil {
		return fmt.Errorf("要写成字符串或 {\"zh\": …, \"en\": …}")
	}
	t.ZH, t.EN = "", ""
	if both.ZH != nil {
		t.ZH = *both.ZH
	}
	if both.EN != nil {
		t.EN = *both.EN
	}
	return nil
}

// MarshalJSON 只有一种语言时写成字符串，和作者写的样子一致。
func (t Text) MarshalJSON() ([]byte, error) {
	if t.EN == "" {
		return json.Marshal(t.ZH)
	}
	type both Text
	return json.Marshal(both(t))
}

// Empty 报告两种语言是不是都没写。
func (t Text) Empty() bool { return t.ZH == "" && t.EN == "" }

// Any 取一种有内容的语言，中文优先。
func (t Text) Any() string {
	if t.ZH != "" {
		return t.ZH
	}
	return t.EN
}

// Manifest 是 dsh-ee.extension.json。字段说明和例子见规格文件 schemas/manifest.schema.json。
type Manifest struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	MinVersion  string `json:"minVersion,omitempty"`
	Name        Text   `json:"name"`
	Description Text   `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Repo        string `json:"repo,omitempty"`
	Kind        string `json:"kind"`

	// 声明式：接口文档的文件名，以及请求允许发往的服务器地址
	OpenAPI string   `json:"openapi,omitempty"`
	Servers []string `json:"servers,omitempty"`
	// 服务扩展：服务地址
	Service *Service `json:"service,omitempty"`
	// 端侧扩展：npm 包名
	Package string `json:"package,omitempty"`

	Capabilities []string        `json:"capabilities,omitempty"`
	Permissions  json.RawMessage `json:"permissions,omitempty"`
	Config       []ConfigField   `json:"config,omitempty"`
	Credential   *Credential     `json:"credential,omitempty"`
	Tools        []Tool          `json:"tools,omitempty"`
	// 扩展带的扩展字段（docs/access-control.md §6.0），第 3 批才生效；这里先原样留着
	Fields json.RawMessage `json:"fields,omitempty"`
	// 附带的应用（文件路径）
	Apps []string `json:"apps,omitempty"`
	// 作者确认过、不需要再报的检查项
	CheckIgnore []Ignore `json:"checkIgnore,omitempty"`
}

type Service struct {
	URL string `json:"url"`
}

// ConfigField 是管理员安装时填的一项配置。
type ConfigField struct {
	Key      string          `json:"key"`
	Type     string          `json:"type"`
	Label    Text            `json:"label"`
	Help     Text            `json:"help,omitempty"`
	Required bool            `json:"required,omitempty"`
	Default  json.RawMessage `json:"default,omitempty"`
	// select 的选项：一个数组，或 "dynamic"（由服务扩展的 options 接口给出）
	Options json.RawMessage `json:"options,omitempty"`
}

// Credential 是调用时用谁的身份。
type Credential struct {
	Mode string `json:"mode"`
	// 凭据放在哪个请求头；空则 Authorization: Bearer。shared 模式下说的是共享凭据，其他模式下是员工身份
	Header string `json:"header,omitempty"`
	// ee_jwt：令牌的 aud；空则用扩展的连接名
	Audience string `json:"audience,omitempty"`
	// ee_jwt：额外带哪些员工信息
	Claims []string `json:"claims,omitempty"`
	// user_secret：告诉员工这个凭据去哪里拿
	Hint Text `json:"hint,omitempty"`
	// 同时要带的共享凭据（比如服务账号的 API Key）放在哪个请求头；不需要就不写
	Shared *SharedCredential `json:"shared,omitempty"`
}

type SharedCredential struct {
	// bearer / header
	Type   string `json:"type"`
	Header string `json:"header,omitempty"`
}

// Tool 是一个工具。声明式扩展里，它是对接口文档里一个接口的补充。
type Tool struct {
	Name        string `json:"name"`
	Description Text   `json:"description,omitempty"`
	// 只读的工具才标 true；没标的一律当写操作
	ReadOnly bool    `json:"readOnly,omitempty"`
	Params   []Param `json:"params,omitempty"`
	// 返回结果只保留这些字段（控制返回给模型的大小）：第一层的字段名，或「字段.子字段」到第二层
	Keep []string `json:"keep,omitempty"`
}

// Param 是工具的一个参数。Name 是接口的路径 / 查询参数名；请求体（JSON 对象）里的字段写成 body.<字段名>。
type Param struct {
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`
	Description Text   `json:"description,omitempty"`
	Label       Text   `json:"label,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Enum        []any  `json:"enum,omitempty"`
	Fill        string `json:"fill,omitempty"`
	// fill 是 app 或 admin 时，预先填好的值（填的人可以改）
	Default json.RawMessage `json:"default,omitempty"`
	Options json.RawMessage `json:"options,omitempty"`
}

// Ignore 是作者对某一条检查的说明：确认过没问题，不用再报。
type Ignore struct {
	Code string `json:"code"`
	// 只忽略这个工具上的（按工具名，调整 tools 的顺序不受影响）
	Tool string `json:"tool,omitempty"`
	// 只忽略这个位置的：检查结果里的 where，可以只写 # 后面的部分
	Where  string `json:"where,omitempty"`
	Reason string `json:"reason"`
}

// Identity 是 `作者/名字@版本`。
func (m *Manifest) Identity() string { return m.ID + "@" + m.Version }

// Digest 是清单（声明式扩展连同接口文档）的 sha256，和 Identity 一起确定一个具体的版本。
func Digest(manifest, openapi []byte) string {
	h := sha256.New()
	h.Write(manifest)
	h.Write([]byte{0})
	h.Write(openapi)
	return hex.EncodeToString(h.Sum(nil))
}
