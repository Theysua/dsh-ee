package extension

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/hikariming/dsh-ee/server/internal/openapi"
)

// Severity 是一条检查结果的轻重：error 不改就装不上；warning 能装，但多半不是作者想要的；info 只是说明。
const (
	SevError   = "error"
	SevWarning = "warning"
	SevInfo    = "info"
)

// Diagnostic 是一条检查结果。Where 精确到文件和位置（JSON 指针），Fix 给出具体改法，Agent 照着改再跑一遍。
type Diagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Where    string `json:"where"`
	Message  string `json:"message"`
	Fix      string `json:"fix,omitempty"`
}

// ToolInfo 是检查时认出来的一个工具，给安装前的确认页和交付说明用。
type ToolInfo struct {
	Name     string `json:"name"`
	ReadOnly bool   `json:"readOnly"`
	Method   string `json:"method,omitempty"`
	Path     string `json:"path,omitempty"`
	// 清单里提到了这个工具（没提到的声明式工具按接口文档的默认：只有 GET 算只读）
	Listed bool `json:"listed"`
	// 不由模型填的参数：谁来填、预先填好的值
	Fixed []FixedParam `json:"fixed,omitempty"`
	// 返回结果只保留这些字段
	Keep []string `json:"keep,omitempty"`
}

// FixedParam 是一个不由模型填的参数。
type FixedParam struct {
	Name    string          `json:"name"`
	Fill    string          `json:"fill"`
	Label   string          `json:"label,omitempty"`
	Default json.RawMessage `json:"default,omitempty"`
}

// CredentialInfo 是清单声明的凭据用法，按实际生效的样子写出来。
type CredentialInfo struct {
	Mode string `json:"mode"`
	// 员工身份放在哪个请求头（shared 模式没有）
	Header   string   `json:"header,omitempty"`
	Audience string   `json:"audience,omitempty"`
	Claims   []string `json:"claims,omitempty"`
	// 共享凭据放在哪个请求头：shared 模式下就是那一份凭据，其他模式下是员工身份之外另带的一份
	SharedHeader string `json:"sharedHeader,omitempty"`
}

// Result 是一次检查的全部结果。
type Result struct {
	OK       bool     `json:"ok"`
	Identity string   `json:"identity,omitempty"`
	Kind     string   `json:"kind,omitempty"`
	Digest   string   `json:"digest,omitempty"`
	Servers  []string `json:"servers,omitempty"`
	// 用谁的身份；清单没写 credential 时为空（不带凭据）
	Credential *CredentialInfo `json:"credential,omitempty"`
	Tools      []ToolInfo      `json:"tools,omitempty"`
	Errors     int             `json:"errors"`
	Warnings   int             `json:"warnings"`
	Results    []Diagnostic    `json:"results"`
	// 解析出来的清单；清单读不出来时是 nil
	Manifest *Manifest `json:"-"`
	// 声明式扩展的接口文档原文
	OpenAPIDoc []byte `json:"-"`
}

var (
	idRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}/[a-z0-9][a-z0-9-]{0,62}$`)
	semverRe  = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	keyRe     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
	toolRe    = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	headerRe  = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	packageRe = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)
	// 名字像写操作的工具：开头是这些动词（后面跟下划线、大写字母或结束）
	writeNameRe = regexp.MustCompile(`^(?i:create|submit|delete|remove|update|set|add|send|post|put|patch|cancel|approve|reject|write|insert|upload|import|reset|revoke|close|archive)(_|[A-Z]|$)`)
)

var (
	kinds        = []string{KindDeclarative, KindService, KindLocal}
	credModes    = []string{CredShared, CredUserSecret, CredEEJWT, CredOAuth}
	fills        = []string{FillModel, FillApp, FillAdmin}
	paramTypes   = []string{"string", "number", "integer", "boolean", "array", "object"}
	configTypes  = []string{"text", "secret", "url", "select", "boolean", "kb", "app"}
	claimChoices = []string{"dept", "roles", "idp_sub"}
	capabilities = []string{"tools", "validate", "options", "health", "knowledge.documents", "knowledge.files", "knowledge.search", "endpoint", "oauth"}
	permissions  = []string{"kb", "approval", "audit", "notify", "endpoint"}
)

type checker struct {
	files   fs.FS
	out     []Diagnostic
	ignores []Ignore
	used    map[int]bool
}

func (c *checker) add(code, where, msg, fix string) { c.addFor("", code, where, msg, fix) }

// addFor 报一条和某个工具有关的结果。作者在 checkIgnore 里确认过的不再报，返回他写的理由；
// 只有标了「可以忽略」的检查项才行，而且要写理由。
func (c *checker) addFor(tool, code, where, msg, fix string) (ignoredFor string) {
	sev := SevError
	if info, ok := codeIndex[code]; ok {
		sev = info.Severity
	}
	if codeIndex[code].Ignorable {
		for i, ig := range c.ignores {
			if ig.Code != code || strings.TrimSpace(ig.Reason) == "" {
				continue
			}
			if ig.Tool != "" && ig.Tool != tool {
				continue
			}
			if ig.Where != "" && ig.Where != where && !strings.HasSuffix(where, "#"+ig.Where) {
				continue
			}
			c.used[i] = true
			return strings.TrimSpace(ig.Reason)
		}
	}
	c.out = append(c.out, Diagnostic{Code: code, Severity: sev, Where: where, Message: msg, Fix: fix})
	return ""
}

func at(parts ...any) string {
	var b strings.Builder
	b.WriteString(ManifestFile + "#")
	for _, p := range parts {
		fmt.Fprintf(&b, "/%v", p)
	}
	return b.String()
}

// Check 检查一个扩展目录（或 zip 包解开后的文件）。不联网，不运行任何东西。
func Check(files fs.FS) *Result {
	c := &checker{files: files, used: map[int]bool{}}
	res := &Result{}
	raw, err := fs.ReadFile(files, ManifestFile)
	if err != nil {
		c.add("manifest.missing", ManifestFile, "找不到清单文件 "+ManifestFile, "在扩展的根目录下建一个 "+ManifestFile+"；规格见 `dsh-ee schema get manifest`")
		return c.finish(res)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		c.add("manifest.invalid_json", ManifestFile, "清单不是合法的 JSON："+err.Error(), "修正 JSON 语法（多余的逗号、没闭合的括号、注释都不行）")
		return c.finish(res)
	}
	if _, ok := generic.(map[string]any); !ok {
		c.add("manifest.invalid_json", ManifestFile, "清单的最外层要是一个 JSON 对象", "写成 { \"id\": …, \"version\": …, … }")
		return c.finish(res)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		where := ManifestFile
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			where = at(strings.ReplaceAll(te.Field, ".", "/"))
		}
		c.add("manifest.type_mismatch", where, "清单里有字段的类型不对："+err.Error(), "对照 `dsh-ee schema get manifest` 改成规格要求的类型")
		return c.finish(res)
	}
	res.Manifest = &m
	c.ignores = m.CheckIgnore
	c.unknownFields(generic, reflect.TypeOf(m), ManifestFile+"#")

	c.identity(&m)
	res.Kind = m.Kind
	if idRe.MatchString(m.ID) && semverRe.MatchString(m.Version) {
		res.Identity = m.Identity()
	}
	var doc []byte
	var spec *openapi.Spec
	switch m.Kind {
	case KindDeclarative:
		doc, spec = c.declarative(&m)
		res.Servers = m.Servers
	case KindService:
		c.service(&m)
	case KindLocal:
		c.local(&m)
	}
	res.Credential = c.credential(&m)
	c.config(&m)
	c.permissions(&m)
	c.capabilities(&m)
	res.Tools = c.tools(&m, spec, doc)
	c.misc(&m)
	c.checkIgnores(&m)
	res.OpenAPIDoc = doc
	res.Digest = Digest(raw, doc)
	return c.finish(res)
}

func (c *checker) finish(res *Result) *Result {
	order := map[string]int{SevError: 0, SevWarning: 1, SevInfo: 2}
	sort.SliceStable(c.out, func(i, j int) bool { return order[c.out[i].Severity] < order[c.out[j].Severity] })
	res.Results = c.out
	if res.Results == nil {
		res.Results = []Diagnostic{}
	}
	for _, d := range c.out {
		switch d.Severity {
		case SevError:
			res.Errors++
		case SevWarning:
			res.Warnings++
		}
	}
	res.OK = res.Errors == 0
	return res
}

// unknownFields 找出清单里规格没有的字段：多半是拼错了，写了也不会生效。
func (c *checker) unknownFields(v any, t reflect.Type, ptr string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok || t == reflect.TypeOf(Text{}) {
			return
		}
		known := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
			if name != "" && name != "-" {
				known[name] = t.Field(i).Type
			}
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ft, ok := known[k]
			if !ok {
				names := make([]string, 0, len(known))
				for n := range known {
					names = append(names, n)
				}
				sort.Strings(names)
				c.add("manifest.unknown_field", ptr+"/"+k, fmt.Sprintf("规格里没有字段 %q，写了也不会生效", k), "这一层能写的字段："+strings.Join(names, "、"))
				continue
			}
			c.unknownFields(obj[k], ft, ptr+"/"+k)
		}
	case reflect.Slice:
		arr, ok := v.([]any)
		if !ok || t == reflect.TypeOf(json.RawMessage{}) {
			return
		}
		for i, item := range arr {
			c.unknownFields(item, t.Elem(), fmt.Sprintf("%s/%d", ptr, i))
		}
	}
}

func (c *checker) identity(m *Manifest) {
	if !idRe.MatchString(m.ID) {
		c.add("id.invalid", at("id"), fmt.Sprintf("id %q 不对：要写成「作者/名字」，只用小写字母、数字和连字符", m.ID), "例如 \"acme/oa\"")
	}
	if !semverRe.MatchString(m.Version) {
		c.add("version.invalid", at("version"), fmt.Sprintf("version %q 不是合法的语义化版本号", m.Version), "写成 主版本.次版本.补丁，例如 \"1.0.0\"")
	}
	if m.MinVersion != "" && !semverRe.MatchString(m.MinVersion) {
		c.add("min_version.invalid", at("minVersion"), fmt.Sprintf("minVersion %q 不是合法的语义化版本号", m.MinVersion), "写成能装这个扩展的最低 dsh-ee 版本，例如 \"0.2.0\"；不限就删掉这一行")
	}
	if m.Name.Empty() {
		c.add("name.missing", at("name"), "没有 name", "写上给管理员和员工看的名字：\"name\": { \"zh\": \"…\", \"en\": \"…\" }，或直接写一个字符串")
	}
	if m.Description.Empty() {
		c.add("description.missing", at("description"), "没有 description，管理员安装时不知道它是做什么的", "用一句话写清楚这个扩展能做什么")
	}
	if !slices.Contains(kinds, m.Kind) {
		c.add("kind.invalid", at("kind"), fmt.Sprintf("kind %q 不对", m.Kind), "只能是 "+strings.Join(kinds, " / ")+"。有接口文档、不写代码的用 declarative")
	}
	if m.Repo != "" {
		if u, err := url.Parse(m.Repo); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			c.add("repo.invalid", at("repo"), "repo 要是一个完整的 http(s) 地址", "例如 \"https://github.com/acme/dsh-ee-oa\"；没有就删掉这一行")
		}
	}
}

func (c *checker) declarative(m *Manifest) ([]byte, *openapi.Spec) {
	if m.Service != nil || m.Package != "" {
		c.add("kind.field_mismatch", at("kind"), "声明式扩展没有 service 和 package", "删掉 service / package；要写代码的扩展把 kind 改成 service 或 local")
	}
	c.servers(m)
	if m.OpenAPI == "" {
		c.add("openapi.missing", at("openapi"), "声明式扩展要指明接口文档", "加上 \"openapi\": \"openapi.yaml\"，把接口文档放在扩展目录里")
		return nil, nil
	}
	if !fs.ValidPath(m.OpenAPI) {
		c.add("openapi.missing", at("openapi"), fmt.Sprintf("接口文档的路径 %q 不对：要是扩展目录里的相对路径", m.OpenAPI), "写成 \"openapi.yaml\" 这样的相对路径，不能用 .. 或绝对路径")
		return nil, nil
	}
	doc, err := fs.ReadFile(c.files, m.OpenAPI)
	if err != nil {
		c.add("openapi.missing", at("openapi"), "找不到接口文档 "+m.OpenAPI, "把 OpenAPI 3.x 文档（JSON 或 YAML）放到扩展目录的 "+m.OpenAPI)
		return nil, nil
	}
	spec, err := openapi.Parse(doc)
	if err != nil {
		c.add("openapi.invalid", m.OpenAPI, "接口文档读不了："+err.Error(), "要是 OpenAPI 3.x，paths 下至少有一个接口")
		return doc, nil
	}
	// 接口文档里写了完整的 servers 地址时，它必须落在清单允许的服务器地址之内
	if u, err := url.Parse(spec.BaseURL); err == nil && u.Scheme != "" && u.Host != "" && len(m.Servers) > 0 && !slices.ContainsFunc(m.Servers, func(s string) bool { return underServer(spec.BaseURL, s) }) {
		c.add("servers.mismatch", m.OpenAPI+"#/servers/0/url", fmt.Sprintf("接口文档的 servers 地址 %s 不在清单的 servers 之内，请求会被拦下", spec.BaseURL),
			"把这个地址加进清单的 servers，或把接口文档的 servers 改成清单里的地址")
	}
	return doc, spec
}

func (c *checker) servers(m *Manifest) {
	if len(m.Servers) == 0 {
		c.add("servers.missing", at("servers"), "声明式扩展要列出请求会发往的服务器地址", "加上 \"servers\": [\"https://oa.acme.internal/api\"]；dsh-ee 只往这些地址发请求")
		return
	}
	for i, s := range m.Servers {
		u, err := url.Parse(s)
		switch {
		case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
			c.add("servers.invalid", at("servers", i), fmt.Sprintf("服务器地址 %q 要是 http(s):// 开头的完整地址", s), "例如 \"https://oa.acme.internal/api\"")
		case u.User != nil:
			c.add("servers.invalid", at("servers", i), "服务器地址里不能带账号密码", "凭据不写在地址里：用 credential 声明凭据模式，由管理员或员工在 dsh-ee 里填")
		case u.RawQuery != "" || u.Fragment != "":
			c.add("servers.invalid", at("servers", i), "服务器地址里不能带 ? 或 #", "只写到路径为止")
		}
	}
}

// underServer 判断地址 target 是不是在服务器地址 server 之内：协议、主机相同，路径以它开头。
func underServer(target, server string) bool {
	t, err1 := url.Parse(target)
	s, err2 := url.Parse(server)
	if err1 != nil || err2 != nil || !strings.EqualFold(t.Scheme, s.Scheme) || !strings.EqualFold(t.Host, s.Host) {
		return false
	}
	base := strings.TrimRight(s.Path, "/")
	return base == "" || t.Path == base || strings.HasPrefix(t.Path, base+"/")
}

func (c *checker) service(m *Manifest) {
	if m.OpenAPI != "" || len(m.Servers) > 0 || m.Package != "" {
		c.add("kind.field_mismatch", at("kind"), "服务扩展没有 openapi、servers 和 package", "删掉这些字段：服务扩展的工具走 MCP，地址写在 service.url")
	}
	var u *url.URL
	var err error
	if m.Service != nil {
		u, err = url.Parse(m.Service.URL)
	}
	if m.Service == nil || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		c.add("service.url_invalid", at("service", "url"), "服务扩展要写服务地址：http(s):// 开头的完整地址，不带账号密码", "加上 \"service\": { \"url\": \"https://oa-ext.acme.internal\" }")
	}
	c.add("kind.not_checked", at("kind"), "服务扩展这里只查了清单；清单和 MCP 工具是否一致、约定接口是否合格要把服务跑起来测", "")
}

func (c *checker) local(m *Manifest) {
	if m.OpenAPI != "" || len(m.Servers) > 0 || m.Service != nil {
		c.add("kind.field_mismatch", at("kind"), "端侧扩展没有 openapi、servers 和 service", "删掉这些字段：端侧扩展是一个 npm 包，包名写在 package")
	}
	if !packageRe.MatchString(m.Package) {
		c.add("package.invalid", at("package"), fmt.Sprintf("package %q 不是合法的 npm 包名", m.Package), "例如 \"@acme/oa-local\"")
	}
	c.add("kind.not_checked", at("kind"), "端侧扩展这里只查了清单", "")
}

func (c *checker) credential(m *Manifest) *CredentialInfo {
	cr := m.Credential
	if cr == nil {
		return nil
	}
	if !slices.Contains(credModes, cr.Mode) {
		c.add("credential.mode_invalid", at("credential", "mode"), fmt.Sprintf("凭据模式 %q 不对", cr.Mode),
			"只能是 "+strings.Join(credModes, " / ")+"。对方能验 JWT 就用 ee_jwt；员工各有 token 用 user_secret；只有一份公共凭据用 shared")
		return nil
	}
	info := &CredentialInfo{Mode: cr.Mode}
	if cr.Mode != CredShared {
		info.Header = "Authorization: Bearer"
		if cr.Header != "" {
			info.Header = canonical(cr.Header)
		}
	}
	if cr.Mode == CredEEJWT {
		info.Audience, info.Claims = cr.Audience, cr.Claims
	}
	if cr.Mode == CredOAuth {
		c.add("credential.unsupported", at("credential", "mode"), "这个版本的 dsh-ee 还不支持 oauth", "先用 user_secret（员工自己填 token）或 ee_jwt")
	}
	if cr.Header != "" && !headerRe.MatchString(cr.Header) {
		c.add("credential.header_invalid", at("credential", "header"), fmt.Sprintf("请求头名字 %q 不对", cr.Header), "例如 \"X-User-Token\"；留空就是 Authorization: Bearer")
	}
	if cr.Mode == CredShared {
		// 只有一份共享凭据：header 说的就是它放在哪个请求头
		info.SharedHeader = "Authorization: Bearer"
		if cr.Header != "" {
			info.SharedHeader = canonical(cr.Header)
		}
		if cr.Shared != nil {
			c.add("credential.field_unused", at("credential", "shared"), "shared 模式只有一份共享凭据，不用再写 shared", "删掉 shared；这份凭据要放在特定的请求头里就写 \"header\": \"X-API-Key\"")
		}
	}
	if cr.Mode != CredEEJWT && (cr.Audience != "" || len(cr.Claims) > 0) {
		c.add("credential.field_unused", at("credential"), "audience 和 claims 只在 ee_jwt 模式下有用", "删掉它们，或把 mode 改成 ee_jwt")
	}
	if cr.Mode != CredUserSecret && !cr.Hint.Empty() {
		c.add("credential.field_unused", at("credential", "hint"), "hint 只在 user_secret 模式下有用", "删掉 hint，或把 mode 改成 user_secret")
	}
	if cr.Mode == CredUserSecret && cr.Hint.Empty() {
		c.add("credential.hint_missing", at("credential", "hint"), "没告诉员工这个凭据去哪里拿", "加上 \"hint\": \"在 OA 的「个人设置 → 开放接口」里生成一个 token\"")
	}
	for i, cl := range cr.Claims {
		if !slices.Contains(claimChoices, cl) {
			c.add("credential.claims_invalid", at("credential", "claims", i), fmt.Sprintf("ee_jwt 不能带 %q", cl), "能额外带的只有 "+strings.Join(claimChoices, " / ")+"；id、用户名、姓名、邮箱默认就带")
		}
	}
	if s := cr.Shared; s != nil && cr.Mode != CredShared {
		userHeader := "Authorization"
		if cr.Header != "" {
			userHeader = canonical(cr.Header)
		}
		sharedHeader := "Authorization"
		switch s.Type {
		case "bearer":
			if s.Header != "" {
				c.add("credential.shared_invalid", at("credential", "shared", "header"), "bearer 固定放在 Authorization 里，不用写 header", "删掉 header，或把 type 改成 header")
			}
		case "header":
			if !headerRe.MatchString(s.Header) {
				c.add("credential.shared_invalid", at("credential", "shared", "header"), "共享凭据的请求头名字不对", "例如 \"X-API-Key\"")
			}
			sharedHeader = canonical(s.Header)
		default:
			c.add("credential.shared_invalid", at("credential", "shared", "type"), fmt.Sprintf("共享凭据的 type %q 不对", s.Type), "只能是 bearer 或 header")
			return info
		}
		info.SharedHeader = sharedHeader
		if s.Type == "bearer" {
			info.SharedHeader = "Authorization: Bearer"
		}
		if sharedHeader == userHeader {
			c.add("credential.header_conflict", at("credential"), "共享凭据和员工身份放在了同一个请求头（"+userHeader+"）里", "给其中一个换个请求头，例如共享凭据用 X-API-Key，员工身份留在 Authorization")
		}
	}
	return info
}

func canonical(h string) string {
	parts := strings.Split(strings.ToLower(h), "-")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "-")
}

func (c *checker) config(m *Manifest) {
	seen := map[string]bool{}
	for i, f := range m.Config {
		if !keyRe.MatchString(f.Key) {
			c.add("config.key_invalid", at("config", i, "key"), fmt.Sprintf("配置项的 key %q 不对", f.Key), "用字母开头的字母、数字、下划线，例如 \"baseUrl\"")
		} else if seen[f.Key] {
			c.add("config.key_duplicate", at("config", i, "key"), fmt.Sprintf("配置项 %q 重复了", f.Key), "每个 key 只能出现一次")
		}
		seen[f.Key] = true
		if !slices.Contains(configTypes, f.Type) {
			c.add("config.type_invalid", at("config", i, "type"), fmt.Sprintf("配置项 %q 的类型 %q 不对", f.Key, f.Type), "只能是 "+strings.Join(configTypes, " / "))
		}
		if f.Label.Empty() {
			c.add("config.label_missing", at("config", i, "label"), fmt.Sprintf("配置项 %q 没有 label，管理员不知道要填什么", f.Key), "加上 \"label\": { \"zh\": \"…\" }")
		}
		if f.Type == "secret" && len(f.Default) > 0 && string(f.Default) != "null" {
			c.add("config.secret_default", at("config", i, "default"), fmt.Sprintf("配置项 %q 是密钥，不能在清单里给默认值", f.Key), "删掉 default：密钥由管理员安装时填，不随扩展分发")
		}
		dynamic := strings.TrimSpace(string(f.Options)) == `"dynamic"`
		switch {
		case f.Type == "select" && len(f.Options) == 0:
			c.add("config.options_invalid", at("config", i, "options"), fmt.Sprintf("配置项 %q 是 select，要给出选项", f.Key), "加上 \"options\": [\"a\", \"b\"]")
		case len(f.Options) > 0 && f.Type != "select":
			c.add("config.options_invalid", at("config", i, "options"), fmt.Sprintf("只有 select 类型才有 options（%q 是 %s）", f.Key, f.Type), "删掉 options，或把 type 改成 select")
		case dynamic && m.Kind == KindDeclarative:
			c.add("config.dynamic_unsupported", at("config", i, "options"), "声明式扩展没有代码，给不出动态选项", "把选项直接写成数组；确实要动态取的，改用服务扩展")
		case len(f.Options) > 0 && !dynamic:
			var opts []any
			if json.Unmarshal(f.Options, &opts) != nil || len(opts) == 0 {
				c.add("config.options_invalid", at("config", i, "options"), fmt.Sprintf("配置项 %q 的 options 要是一个非空数组，或 \"dynamic\"", f.Key), "例如 \"options\": [\"prod\", \"test\"]")
			}
		}
	}
}

func (c *checker) permissions(m *Manifest) {
	if len(m.Permissions) == 0 || string(m.Permissions) == "null" {
		return
	}
	var perms map[string]any
	if err := json.Unmarshal(m.Permissions, &perms); err != nil {
		c.add("permissions.invalid", at("permissions"), "permissions 要是一个对象", "例如 \"permissions\": { \"kb\": \"read\", \"approval\": true }")
		return
	}
	keys := make([]string, 0, len(perms))
	for k := range perms {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := perms[k]
		switch {
		case !slices.Contains(permissions, k):
			c.add("permissions.invalid", at("permissions", k), fmt.Sprintf("没有 %q 这项权限", k), "能申请的只有 "+strings.Join(permissions, " / "))
		case k == "kb" && v != "read":
			c.add("permissions.invalid", at("permissions", k), "kb 权限只能是 \"read\"", "写成 \"kb\": \"read\"")
		case k != "kb" && v != true:
			c.add("permissions.invalid", at("permissions", k), fmt.Sprintf("%s 权限要写成 true", k), fmt.Sprintf("写成 %q: true；不需要就删掉", k))
		}
	}
	if m.Kind == KindDeclarative && len(perms) > 0 {
		c.add("permissions.unused", at("permissions"), "声明式扩展没有代码，用不上反过来调 dsh-ee 的权限；申请了只会让管理员多一层顾虑", "删掉 permissions")
	}
}

func (c *checker) capabilities(m *Manifest) {
	for i, cp := range m.Capabilities {
		switch {
		case !slices.Contains(capabilities, cp):
			c.add("capabilities.invalid", at("capabilities", i), fmt.Sprintf("没有 %q 这项能力", cp), "能写的只有 "+strings.Join(capabilities, " / "))
		case m.Kind == KindDeclarative && cp != "tools":
			c.add("capabilities.invalid", at("capabilities", i), fmt.Sprintf("声明式扩展只有工具，做不了 %s", cp), "删掉它；要做知识源、回调、动态选项，改用服务扩展")
		}
	}
}

// tools 检查清单里的工具。声明式扩展对着接口文档查；返回最终会有哪些工具、各自是不是只读。
func (c *checker) tools(m *Manifest, spec *openapi.Spec, doc []byte) []ToolInfo {
	byName := map[string]*openapi.Tool{}
	var names []string
	if spec != nil {
		for i := range spec.Tools {
			byName[spec.Tools[i].Name] = &spec.Tools[i]
			names = append(names, spec.Tools[i].Name)
		}
	}
	listed := map[string]*Tool{}
	// 接口方法不是 GET、但作者写明理由确认只读的工具
	override := map[string]bool{}
	for i := range m.Tools {
		t := &m.Tools[i]
		where := at("tools", i)
		if !toolRe.MatchString(t.Name) {
			c.add("tool.name_invalid", where+"/name", fmt.Sprintf("工具名 %q 不对：只能用字母、数字、下划线，最多 64 个字符", t.Name), "例如 \"list_forms\"")
			continue
		}
		if listed[t.Name] != nil {
			c.add("tool.duplicate", where+"/name", fmt.Sprintf("工具 %s 在清单里出现了两次", t.Name), "合并成一项")
			continue
		}
		listed[t.Name] = t
		op := byName[t.Name]
		if m.Kind == KindDeclarative && spec != nil && op == nil {
			c.add("tool.unknown", where+"/name", fmt.Sprintf("接口文档里没有工具 %s（工具名来自接口的 operationId）", t.Name), "接口文档里有的工具："+strings.Join(names, "、"))
			continue
		}
		method := ""
		if op != nil {
			method = op.Op.Method
		}
		switch {
		case t.ReadOnly && op != nil && !op.ReadOnly:
			reason := c.addFor(t.Name, "tool.readonly_suspect", where, fmt.Sprintf("工具 %s 标了 readOnly，但对应的接口方法是 %s", t.Name, strings.ToUpper(method)),
				fmt.Sprintf("去掉 tools[%d] 的 \"readOnly\": true；如果这个接口确实不改数据（比如用 POST 传查询条件），在 checkIgnore 里加 {\"code\": \"tool.readonly_suspect\", \"tool\": %q, \"reason\": \"…\"}", i, t.Name))
			if reason != "" {
				// 照作者说的当只读，但要留在结果里：装扩展的管理员得看到「这个 POST 被当成了只读」和理由
				override[t.Name] = true
				c.add("tool.readonly_override", where, fmt.Sprintf("工具 %s 的接口方法是 %s，按作者写的理由当作只读：%s", t.Name, strings.ToUpper(method), reason), "")
			}
		case t.ReadOnly && writeNameRe.MatchString(t.Name):
			c.addFor(t.Name, "tool.readonly_suspect", where, fmt.Sprintf("工具 %s 标了 readOnly，但名字像写操作", t.Name),
				fmt.Sprintf("去掉 tools[%d] 的 \"readOnly\": true；如果确实只读，在 checkIgnore 里加 {\"code\": \"tool.readonly_suspect\", \"tool\": %q, \"reason\": \"…\"}", i, t.Name))
		case !t.ReadOnly && op != nil && op.ReadOnly:
			c.addFor(t.Name, "tool.readonly_unmarked", where, fmt.Sprintf("工具 %s 对应的是 GET 接口，但清单里没标 readOnly：会被当成写操作，每次调用都要员工确认", t.Name),
				fmt.Sprintf("确实只读就给 tools[%d] 加上 \"readOnly\": true；这个 GET 确实会改数据的话，在 checkIgnore 里加 {\"code\": \"tool.readonly_unmarked\", \"tool\": %q, \"reason\": \"…\"}", i, t.Name))
		}
		c.params(t, op, where)
		if op != nil && len(t.Keep) > 0 {
			c.keep(t, op, doc, where)
		}
	}
	c.adminDefaults(m)
	var out []ToolInfo
	if m.Kind == KindDeclarative && spec != nil {
		// 清单里提到的工具按清单的顺序排在前面（和检查结果里的位置对得上），其余按接口文档的顺序
		for i := range m.Tools {
			t := &m.Tools[i]
			st := byName[t.Name]
			if st == nil || listed[t.Name] != t {
				continue
			}
			// 只有标了 readOnly 才算只读；写接口要作者在 checkIgnore 里写明理由才行
			ro := t.ReadOnly && (st.ReadOnly || override[t.Name])
			out = append(out, ToolInfo{Name: st.Name, ReadOnly: ro, Method: strings.ToUpper(st.Op.Method), Path: st.Op.Path, Listed: true, Fixed: fixedParams(t), Keep: t.Keep})
		}
		for _, st := range spec.Tools {
			if listed[st.Name] == nil {
				out = append(out, ToolInfo{Name: st.Name, ReadOnly: st.ReadOnly, Method: strings.ToUpper(st.Op.Method), Path: st.Op.Path})
			}
		}
		return out
	}
	for i := range m.Tools {
		if t := &m.Tools[i]; listed[t.Name] == t {
			out = append(out, ToolInfo{Name: t.Name, ReadOnly: t.ReadOnly, Listed: true, Fixed: fixedParams(t), Keep: t.Keep})
		}
	}
	return out
}

func fixedParams(t *Tool) []FixedParam {
	var out []FixedParam
	for _, p := range t.Params {
		if p.Fill == FillApp || p.Fill == FillAdmin {
			out = append(out, FixedParam{Name: p.Name, Fill: p.Fill, Label: p.Label.Any(), Default: p.Default})
		}
	}
	return out
}

// keep 检查「只保留这些字段」写的是不是返回里真有的字段。写法不对（超过两层）什么时候都报；
// 字段在不在返回里，要接口文档写了返回结构才查得了。
func (c *checker) keep(t *Tool, op *openapi.Tool, doc []byte, where string) {
	shape, known := responseShape(doc, op.Op.Method, op.Op.Path)
	// 最容易犯的错：返回把列表包了一层（{items: [...], total}），keep 却直接写了里面每一项的字段。
	// 没写进 keep 的字段一律丢掉，这样写返回会被清空，所以按错误报，并且只报这一条。
	if known && !slices.ContainsFunc(t.Keep, func(k string) bool {
		parent, _, _ := strings.Cut(k, ".")
		return slices.Contains(shape.fields, parent)
	}) {
		for _, parent := range shape.fields {
			subs := shape.sub[parent]
			if i := slices.IndexFunc(t.Keep, func(k string) bool { return slices.Contains(subs, k) }); i >= 0 {
				c.add("keep.wrapped", where+"/keep", fmt.Sprintf("工具 %s 的返回把内容包在 %s 里，keep 写的却是 %s 里面的字段：照现在的写法，返回给模型的会是空的", t.Name, parent, parent),
					fmt.Sprintf("写成 \"%s.<字段名>\"，例如把 %q 改成 \"%s.%s\"；%s 之外还要留的第一层字段（比如总数）也要写上", parent, t.Keep[i], parent, t.Keep[i], parent))
				return
			}
		}
	}
	for i, k := range t.Keep {
		at := fmt.Sprintf("%s/keep/%d", where, i)
		parent, child, dotted := strings.Cut(k, ".")
		if parent == "" || (dotted && (child == "" || strings.Contains(child, "."))) {
			c.add("keep.unknown", at, fmt.Sprintf("工具 %s 的 keep 里 %q 写法不对：只能写第一层的字段名，或「字段.子字段」（到第二层为止）", t.Name, k),
				"返回是 {\"items\": [...], \"total\": 1} 这样包了一层的列表时，写成 \"items.id\"、\"items.title\"、\"total\"")
			continue
		}
		if !known {
			continue
		}
		if !slices.Contains(shape.fields, parent) {
			c.add("keep.unknown", at, fmt.Sprintf("工具 %s 的返回里没有字段 %s，写了也留不下任何东西", t.Name, parent), "接口文档里这个接口返回的字段："+strings.Join(shape.fields, "、"))
			continue
		}
		if subs, ok := shape.sub[parent]; dotted && ok && !slices.Contains(subs, child) {
			c.add("keep.unknown", at, fmt.Sprintf("工具 %s 返回的 %s 里没有字段 %s，写了也留不下任何东西", t.Name, parent, child), fmt.Sprintf("接口文档里 %s 的字段：%s", parent, strings.Join(subs, "、")))
		}
	}
}

// adminDefaults 检查管理员统一设置的参数：同一个参数在几个工具上出现时，安装时只填一次，预先填的值要一致。
func (c *checker) adminDefaults(m *Manifest) {
	first := map[string]string{}
	for i, t := range m.Tools {
		for j, p := range t.Params {
			if p.Fill != FillAdmin {
				continue
			}
			key := strings.TrimPrefix(p.Name, "body.")
			cur := strings.TrimSpace(string(p.Default))
			if prev, ok := first[key]; ok && prev != cur {
				c.add("param.default_conflict", at("tools", i, "params", j, "default"), fmt.Sprintf("管理员统一设置的参数 %s 在几个工具上的 default 不一样；安装时它只填一次", key), "把这几处的 default 改成同一个值")
			} else if !ok {
				first[key] = cur
			}
		}
	}
}

func (c *checker) params(t *Tool, op *openapi.Tool, where string) {
	seen := map[string]bool{}
	var known []string
	if op != nil {
		for _, p := range op.Op.Params {
			known = append(known, p.Name)
		}
		if op.Op.Body {
			// 请求体里的字段写成 body.<字段名>（只到第一层）
			for _, f := range bodyFields(op.InputSchema) {
				known = append(known, "body."+f)
			}
		}
	}
	for j, p := range t.Params {
		pw := fmt.Sprintf("%s/params/%d", where, j)
		switch {
		case p.Name == "":
			c.add("param.name_missing", pw+"/name", fmt.Sprintf("工具 %s 的第 %d 个参数没有 name", t.Name, j+1), "写上参数名")
			continue
		case seen[p.Name]:
			c.add("param.duplicate", pw+"/name", fmt.Sprintf("工具 %s 的参数 %s 出现了两次", t.Name, p.Name), "合并成一项")
			continue
		}
		seen[p.Name] = true
		if op != nil && !slices.Contains(known, p.Name) {
			fix := "这个接口没有可以补充说明的参数，删掉这一项"
			if len(known) > 0 {
				fix = "这个接口能写的参数名：" + strings.Join(known, "、")
			}
			if slices.Contains(known, "body."+p.Name) {
				fix = fmt.Sprintf("%s 在请求体里，写成 \"name\": \"body.%s\"", p.Name, p.Name)
			} else if op.Op.Body && strings.HasPrefix(p.Name, "body") {
				fix += "。请求体里只能写到第一层的字段（body.<字段名>），更深的字段在接口文档里改说明"
			}
			c.add("param.unknown", pw+"/name", fmt.Sprintf("工具 %s 对应的接口没有参数 %s", t.Name, p.Name), fix)
		}
		if p.Fill != "" && !slices.Contains(fills, p.Fill) {
			c.add("param.fill_invalid", pw+"/fill", fmt.Sprintf("参数 %s 的 fill %q 不对", p.Name, p.Fill),
				"只能是 model（模型填）/ app（搭应用的人固定，模型看不到）/ admin（管理员统一设置）")
		}
		if p.Type != "" && !slices.Contains(paramTypes, p.Type) {
			c.add("param.type_invalid", pw+"/type", fmt.Sprintf("参数 %s 的类型 %q 不对", p.Name, p.Type), "只能是 "+strings.Join(paramTypes, " / "))
		}
		if want := paramType(p, op); want != "" && len(p.Default) > 0 && string(p.Default) != "null" && !jsonIs(p.Default, want) {
			c.add("param.default_type", pw+"/default", fmt.Sprintf("参数 %s 的类型是 %s，default 写的不是这个类型", p.Name, want),
				"default 用和参数一样的 JSON 类型：字符串加引号，数字和 true / false 不加")
		}
		if len(p.Default) > 0 && string(p.Default) != "null" && p.Fill != FillApp && p.Fill != FillAdmin {
			c.add("param.default_unused", pw+"/default", fmt.Sprintf("参数 %s 由模型填，default 不起作用", p.Name),
				"这个值如果不该由模型决定，把 fill 改成 admin（管理员统一设置）或 app（搭应用的人固定）；否则删掉 default")
		}
		if (p.Fill == FillApp || p.Fill == FillAdmin) && p.Label.Empty() {
			c.add("param.label_missing", pw+"/label", fmt.Sprintf("参数 %s 由人来填（fill: %s），但没有 label，填的人不知道它是什么", p.Name, p.Fill), "加上 \"label\": { \"zh\": \"…\" }")
		}
	}
}

func (c *checker) misc(m *Manifest) {
	for i, app := range m.Apps {
		if !fs.ValidPath(app) {
			c.add("apps.missing", at("apps", i), fmt.Sprintf("应用文件的路径 %q 不对：要是扩展目录里的相对路径", app), "例如 \"apps/hr-helper.yaml\"")
		} else if _, err := fs.Stat(c.files, app); err != nil {
			c.add("apps.missing", at("apps", i), "找不到附带的应用文件 "+app, "把文件放进扩展目录，或从 apps 里去掉这一项")
		}
	}
	if m.Icon != "" && m.Kind != KindService {
		if !fs.ValidPath(m.Icon) {
			c.add("icon.missing", at("icon"), "图标的路径不对：要是扩展目录里的相对路径", "例如 \"icon.svg\"")
		} else if _, err := fs.Stat(c.files, m.Icon); err != nil {
			c.add("icon.missing", at("icon"), "找不到图标文件 "+m.Icon, "把图标放进扩展目录，或删掉 icon")
		}
	}
	if len(m.Fields) > 0 && string(m.Fields) != "null" {
		c.add("fields.not_yet", at("fields"), "扩展字段（fields）这个版本的 dsh-ee 还不处理，安装时会忽略", "")
	}
}

func (c *checker) checkIgnores(m *Manifest) {
	for i, ig := range m.CheckIgnore {
		info, ok := codeIndex[ig.Code]
		switch {
		case !ok:
			c.add("ignore.invalid", at("checkIgnore", i, "code"), fmt.Sprintf("没有 %q 这个检查项", ig.Code), "检查项的列表见 `dsh-ee schema get errors`")
		case !info.Ignorable:
			c.add("ignore.invalid", at("checkIgnore", i, "code"), fmt.Sprintf("检查项 %s 不能忽略，只能改掉", ig.Code), "从 checkIgnore 里去掉这一项，按检查结果里的 fix 修改")
		case strings.TrimSpace(ig.Reason) == "":
			c.add("ignore.invalid", at("checkIgnore", i, "reason"), "忽略一个检查项要写明理由，给审这个扩展的人看", "加上 \"reason\": \"这个接口虽然是 POST，但只做查询\"")
		case !c.used[i]:
			c.add("ignore.unused", at("checkIgnore", i), fmt.Sprintf("checkIgnore 里的 %s 没有对上任何检查结果", ig.Code),
				"没有这个问题了就删掉这一项；还有的话，用 \"tool\": \"<工具名>\" 指明是哪个工具，或让 where 和检查结果里的 where 一致（可以只写 # 后面的部分）")
		}
	}
}

// bodyFields 取 JSON 请求体第一层的字段名（请求体不是对象、或没写 properties 时为空）。
func bodyFields(inputSchema json.RawMessage) []string {
	var schema struct {
		Properties struct {
			Body struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"body"`
		} `json:"properties"`
	}
	if json.Unmarshal(inputSchema, &schema) != nil {
		return nil
	}
	out := make([]string, 0, len(schema.Properties.Body.Properties))
	for name := range schema.Properties.Body.Properties {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// paramType 是一个参数应有的类型：清单里写了就用清单的，否则看接口文档。不知道时返回空。
func paramType(p Param, op *openapi.Tool) string {
	if p.Type != "" {
		return p.Type
	}
	if op == nil {
		return ""
	}
	var schema struct {
		Properties map[string]struct {
			Type       string `json:"type"`
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
		} `json:"properties"`
	}
	if json.Unmarshal(op.InputSchema, &schema) != nil {
		return ""
	}
	if field, ok := strings.CutPrefix(p.Name, "body."); ok {
		return schema.Properties["body"].Properties[field].Type
	}
	return schema.Properties[p.Name].Type
}

// jsonIs 判断一个 JSON 值是不是指定的类型。
func jsonIs(raw json.RawMessage, typ string) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	switch typ {
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == float64(int64(f))
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	}
	return true
}

// respShape 是一个接口成功返回的样子，只到第二层。
type respShape struct {
	// 第一层的字段：返回对象就是它的字段，返回数组就是每一项的字段
	fields []string
	// 第一层字段 → 它里面的字段（它是对象），或它每一项的字段（它是数组）；接口文档没写到这一层的不在里面
	sub map[string][]string
}

// responseShape 从接口文档里取一个接口成功返回的结构。
// 接口文档没写返回结构（没有 responses、不是 JSON、没有 properties）时 known 为 false。
func responseShape(doc []byte, method, path string) (shape respShape, known bool) {
	var root map[string]any
	if yaml.Unmarshal(doc, &root) != nil {
		return shape, false
	}
	get := func(v any, keys ...string) any {
		for _, k := range keys {
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[k]
		}
		return v
	}
	deref := func(v any) map[string]any {
		for i := 0; i < 8; i++ {
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			ref, ok := m["$ref"].(string)
			if !ok {
				return m
			}
			name, ok := strings.CutPrefix(ref, "#/")
			if !ok {
				return nil
			}
			v = get(root, strings.Split(name, "/")...)
		}
		return nil
	}
	// propsOf 取一个结构的字段：是数组就看它每一项
	propsOf := func(v any) map[string]any {
		schema := deref(v)
		if schema == nil {
			return nil
		}
		if items := deref(schema["items"]); schema["type"] == "array" && items != nil {
			schema = items
		}
		props, _ := schema["properties"].(map[string]any)
		return props
	}
	names := func(props map[string]any) []string {
		out := make([]string, 0, len(props))
		for name := range props {
			out = append(out, name)
		}
		sort.Strings(out)
		return out
	}
	responses, _ := get(root, "paths", path, strings.ToLower(method), "responses").(map[string]any)
	var codes []string
	for code := range responses {
		if strings.HasPrefix(code, "2") {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	for _, code := range codes {
		props := propsOf(get(deref(responses[code]), "content", "application/json", "schema"))
		if len(props) == 0 {
			continue
		}
		shape.fields, shape.sub = names(props), map[string][]string{}
		for name, p := range props {
			if sub := propsOf(p); len(sub) > 0 {
				shape.sub[name] = names(sub)
			}
		}
		return shape, true
	}
	return shape, false
}
