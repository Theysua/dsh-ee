package extension

// CodeInfo 是一个检查项：会报什么、有多严重、能不能由作者说明理由后忽略。
type CodeInfo struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Meaning  string `json:"meaning"`
	// 作者确认过没问题时，可以在清单的 checkIgnore 里写明理由，之后不再报
	Ignorable bool `json:"ignorable,omitempty"`
}

// Codes 是 `dsh-ee extension check` 会报的全部检查项。`dsh-ee schema get errors` 输出的就是这张表。
var Codes = []CodeInfo{
	{Code: "manifest.missing", Severity: SevError, Meaning: "扩展目录里没有 dsh-ee.extension.json"},
	{Code: "manifest.invalid_json", Severity: SevError, Meaning: "清单不是合法的 JSON 对象"},
	{Code: "manifest.type_mismatch", Severity: SevError, Meaning: "清单里有字段的类型和规格不一致"},
	{Code: "manifest.unknown_field", Severity: SevWarning, Meaning: "清单里有规格没有的字段，多半是拼错了，写了不会生效"},
	{Code: "id.invalid", Severity: SevError, Meaning: "id 不是「作者/名字」的形式"},
	{Code: "version.invalid", Severity: SevError, Meaning: "version 不是语义化版本号"},
	{Code: "min_version.invalid", Severity: SevError, Meaning: "minVersion 不是语义化版本号"},
	{Code: "name.missing", Severity: SevError, Meaning: "没有 name"},
	{Code: "description.missing", Severity: SevWarning, Meaning: "没有 description"},
	{Code: "kind.invalid", Severity: SevError, Meaning: "kind 不是 declarative / service / local"},
	{Code: "kind.field_mismatch", Severity: SevError, Meaning: "写了别的扩展形式才有的字段"},
	{Code: "kind.not_checked", Severity: SevInfo, Meaning: "服务扩展和端侧扩展这里只查清单，协议是否合格要跑起来测"},
	{Code: "repo.invalid", Severity: SevError, Meaning: "repo 不是完整的 http(s) 地址"},
	{Code: "openapi.missing", Severity: SevError, Meaning: "声明式扩展没有指明接口文档，或文件找不到"},
	{Code: "openapi.invalid", Severity: SevError, Meaning: "接口文档不是能用的 OpenAPI 3.x"},
	{Code: "servers.missing", Severity: SevError, Meaning: "声明式扩展没有列出请求会发往的服务器地址"},
	{Code: "servers.invalid", Severity: SevError, Meaning: "服务器地址不是完整的 http(s) 地址，或带了账号密码、查询参数"},
	{Code: "servers.mismatch", Severity: SevError, Meaning: "接口文档里的 servers 地址不在清单允许的服务器地址之内"},
	{Code: "service.url_invalid", Severity: SevError, Meaning: "服务扩展没有写服务地址，或地址不对"},
	{Code: "package.invalid", Severity: SevError, Meaning: "端侧扩展的 package 不是合法的 npm 包名"},
	{Code: "credential.mode_invalid", Severity: SevError, Meaning: "凭据模式不是 shared / user_secret / ee_jwt / oauth"},
	{Code: "credential.unsupported", Severity: SevError, Meaning: "这个版本的 dsh-ee 还不支持这种凭据模式"},
	{Code: "credential.header_invalid", Severity: SevError, Meaning: "员工身份的请求头名字不对"},
	{Code: "credential.header_conflict", Severity: SevError, Meaning: "共享凭据和员工身份放在了同一个请求头里"},
	{Code: "credential.shared_invalid", Severity: SevError, Meaning: "credential.shared 写得不对"},
	{Code: "credential.claims_invalid", Severity: SevError, Meaning: "ee_jwt 要带的员工信息不在可选范围内"},
	{Code: "credential.field_unused", Severity: SevWarning, Meaning: "写了当前凭据模式用不上的字段"},
	{Code: "credential.hint_missing", Severity: SevWarning, Meaning: "user_secret 模式没告诉员工凭据去哪里拿"},
	{Code: "config.key_invalid", Severity: SevError, Meaning: "配置项的 key 不对"},
	{Code: "config.key_duplicate", Severity: SevError, Meaning: "配置项的 key 重复"},
	{Code: "config.type_invalid", Severity: SevError, Meaning: "配置项的类型不在可选范围内"},
	{Code: "config.label_missing", Severity: SevError, Meaning: "配置项没有 label"},
	{Code: "config.secret_default", Severity: SevError, Meaning: "密钥类型的配置项在清单里给了默认值"},
	{Code: "config.options_invalid", Severity: SevError, Meaning: "select 没有选项，或别的类型写了 options"},
	{Code: "config.dynamic_unsupported", Severity: SevError, Meaning: "声明式扩展用了动态选项"},
	{Code: "permissions.invalid", Severity: SevError, Meaning: "申请了不存在的反向权限，或值写得不对"},
	{Code: "permissions.unused", Severity: SevWarning, Meaning: "声明式扩展申请了用不上的反向权限"},
	{Code: "capabilities.invalid", Severity: SevError, Meaning: "写了不存在的能力，或这种扩展形式做不了的能力"},
	{Code: "tool.name_invalid", Severity: SevError, Meaning: "工具名不对"},
	{Code: "tool.duplicate", Severity: SevError, Meaning: "同一个工具在清单里出现了两次"},
	{Code: "tool.unknown", Severity: SevError, Meaning: "清单里的工具在接口文档里找不到"},
	{Code: "tool.readonly_suspect", Severity: SevError, Meaning: "工具标了 readOnly，但接口方法不是 GET，或名字像写操作", Ignorable: true},
	{Code: "tool.readonly_override", Severity: SevInfo, Meaning: "接口方法不是 GET 的工具，按作者在 checkIgnore 里写的理由当作只读；装扩展的人要看一眼理由"},
	{Code: "tool.readonly_unmarked", Severity: SevWarning, Meaning: "GET 接口对应的工具没标 readOnly，会被当成写操作", Ignorable: true},
	{Code: "param.name_missing", Severity: SevError, Meaning: "参数没有 name"},
	{Code: "param.duplicate", Severity: SevError, Meaning: "同一个参数出现了两次"},
	{Code: "param.unknown", Severity: SevError, Meaning: "清单里的参数在对应的接口上找不到"},
	{Code: "param.fill_invalid", Severity: SevError, Meaning: "参数的 fill 不是 model / app / admin"},
	{Code: "param.type_invalid", Severity: SevError, Meaning: "参数的类型不在可选范围内"},
	{Code: "param.default_type", Severity: SevError, Meaning: "default 的 JSON 类型和参数的类型不一致"},
	{Code: "param.default_conflict", Severity: SevError, Meaning: "管理员统一设置的同一个参数，在几个工具上预先填的值不一样"},
	{Code: "param.default_unused", Severity: SevWarning, Meaning: "由模型填的参数写了 default"},
	{Code: "param.label_missing", Severity: SevWarning, Meaning: "由人来填的参数没有 label"},
	{Code: "keep.unknown", Severity: SevWarning, Meaning: "keep 里写了返回里没有的字段，或写法不对（最多写到「字段.子字段」）"},
	{Code: "keep.wrapped", Severity: SevError, Meaning: "返回把列表包了一层（{items: [...]}），keep 却直接写了里面每一项的字段，返回会被清空"},
	{Code: "apps.missing", Severity: SevError, Meaning: "附带的应用文件找不到"},
	{Code: "icon.missing", Severity: SevWarning, Meaning: "图标文件找不到"},
	{Code: "fields.not_yet", Severity: SevInfo, Meaning: "扩展字段这个版本还不处理"},
	{Code: "ignore.invalid", Severity: SevError, Meaning: "checkIgnore 里的检查项不存在、不能忽略，或没写理由"},
	{Code: "ignore.unused", Severity: SevInfo, Meaning: "checkIgnore 里有一项没有对上任何检查结果"},
}

var codeIndex = func() map[string]CodeInfo {
	out := map[string]CodeInfo{}
	for _, c := range Codes {
		out[c.Code] = c
	}
	return out
}()
