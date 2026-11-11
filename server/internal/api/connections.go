package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/hikariming/dsh-ee/server/internal/extension"
	"github.com/hikariming/dsh-ee/server/internal/mcp"
	"github.com/hikariming/dsh-ee/server/internal/openapi"
	"github.com/hikariming/dsh-ee/server/internal/secret"
	"github.com/hikariming/dsh-ee/server/internal/store"
)

// 企业连接：上游是 MCP 服务或 OpenAPI 描述的接口。凭据只存在 dsh-ee（主密钥加密），
// 员工的 DSH 通过 dsh-ee 的 /mcp 调用，dsh-ee 按人校验后带上凭据转发。

const (
	connResultLimit    = 64 << 10
	defaultConnTimeout = 30 * time.Second
)

// connKey 区分上游 MCP 客户端：只有共享凭据的连接所有人共用一个（user 为 0）；
// 带员工身份的连接每人一个，各自的会话，免得上游把一个人的会话认成另一个人。
type connKey struct {
	asset, user int64
}

type connClients struct {
	mu sync.Mutex
	m  map[connKey]*cachedConn
}

type cachedConn struct {
	key    string
	client *mcp.Client
}

// dropConnClient 丢掉一个连接的全部上游客户端（配置、共享凭据变了，或上游出错要重连）。
func (s *Server) dropConnClient(id int64) {
	s.conns.mu.Lock()
	for k := range s.conns.m {
		if k.asset == id {
			delete(s.conns.m, k)
		}
	}
	s.conns.mu.Unlock()
}

// dropUserConnClient 丢掉一位员工在一个连接上的上游客户端（他的凭据变了）。
func (s *Server) dropUserConnClient(id, userID int64) {
	s.conns.mu.Lock()
	delete(s.conns.m, connKey{id, userID})
	s.conns.mu.Unlock()
}

func (s *Server) saveConnSecret(r *http.Request, id int64, plain string) error {
	plain = strings.TrimSpace(plain)
	if plain == "" {
		return s.store.SetAssetSecret(r.Context(), id, nil, "")
	}
	sealed, err := s.box.Seal([]byte(plain))
	if err != nil {
		return err
	}
	s.dropConnClient(id)
	return s.store.SetAssetSecret(r.Context(), id, sealed, secret.Hint(plain))
}

// connHeader 按认证方式拼出访问上游要带的共享凭据请求头。
func (s *Server) connHeader(ctx context.Context, assetID int64, sp *assetSpec, override *string) (http.Header, error) {
	h := http.Header{}
	if sp.Auth == nil || sp.Auth.Type == "none" {
		return h, nil
	}
	var plain string
	if override != nil {
		plain = strings.TrimSpace(*override)
	} else if assetID > 0 {
		enc, err := s.store.AssetSecret(ctx, assetID)
		if err != nil {
			return nil, err
		}
		if len(enc) > 0 {
			b, err := s.box.Open(enc)
			if err != nil {
				return nil, fmt.Errorf("解密连接凭据（主密钥是否换过？）：%w", err)
			}
			plain = string(b)
		}
	}
	if plain == "" {
		return nil, errors.New("这个连接需要凭据，但还没有填写")
	}
	switch sp.Auth.Type {
	case "bearer":
		h.Set("Authorization", "Bearer "+plain)
	case "header":
		h.Set(sp.Auth.Header, plain)
	}
	return h, nil
}

func (s *Server) connHTTP(sp *assetSpec) *http.Client {
	t := defaultConnTimeout
	if sp.TimeoutSec > 0 {
		t = time.Duration(sp.TimeoutSec) * time.Second
	}
	base := s.upstream
	return &http.Client{Transport: base.Transport, Timeout: t, CheckRedirect: sameHostRedirect}
}

// sameHostRedirect 不跟随跳到别的主机的重定向。Go 只会在跨域时去掉 Authorization，
// 自定义请求头（X-API-Key、放员工身份的头）会原样带过去；上游一个重定向就能把凭据送到别处。
func sameHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("重定向次数太多")
	}
	if req.URL.Host != via[0].URL.Host {
		return fmt.Errorf("上游把请求重定向到了另一个地址（%s），为了不把凭据带过去，已停止", req.URL.Host)
	}
	return nil
}

// connCaller 是这一次调用的发起人。放在 context 里交给上游客户端：带员工身份的连接每人缓存一个客户端，
// 但员工的部门、角色会变，所以请求头每次按当前的身份现取，不用建客户端时的那一份。
type connCaller struct {
	v    viewer
	base string
}

type connCallerKey struct{}

func withCaller(ctx context.Context, v viewer, base string) context.Context {
	return context.WithValue(ctx, connCallerKey{}, connCaller{v, base})
}

// connClient 取一个连接的上游 MCP 客户端；配置或凭据变了就新建。
// 连接带员工身份时，v 是发起调用的员工、base 是他访问 dsh-ee 的地址；员工身份的请求头每次请求现取
// （ee_jwt 只有几分钟有效）。
func (s *Server) connClient(ctx context.Context, a *store.Asset, version int, sp *assetSpec, v viewer, base string) (*mcp.Client, error) {
	key := fmt.Sprintf("%d|%s|%s", version, a.SecretHint, sp.URL)
	ck := connKey{asset: a.ID}
	if sp.UserAuth != nil {
		if v.user == nil {
			return nil, errors.New("这个连接按员工本人的身份调用，需要知道是谁在调")
		}
		ck.user = v.user.ID
	}
	s.conns.mu.Lock()
	if s.conns.m == nil {
		s.conns.m = map[connKey]*cachedConn{}
	}
	if c := s.conns.m[ck]; c != nil && c.key == key {
		s.conns.mu.Unlock()
		return c.client, nil
	}
	s.conns.mu.Unlock()
	h, err := s.connHeader(ctx, a.ID, sp, nil)
	if err != nil {
		return nil, err
	}
	c := &mcp.Client{URL: sp.URL, Header: h, HC: s.connHTTP(sp)}
	if sp.UserAuth != nil {
		// 先取一次：没填凭据、签不了令牌这类问题在这里就报出来，不留下一个建不起会话的客户端
		if _, err := s.userAuthHeader(ctx, a, sp, v, base); err != nil {
			return nil, err
		}
		uid := v.user.ID
		c.Dynamic = func(ctx context.Context) (http.Header, error) {
			who, ok := ctx.Value(connCallerKey{}).(connCaller)
			if !ok || who.v.user == nil || who.v.user.ID != uid {
				return nil, errors.New("这个连接按员工本人的身份调用，调用时没有带上是谁")
			}
			return s.userAuthHeader(ctx, a, sp, who.v, who.base)
		}
	}
	s.conns.mu.Lock()
	s.conns.m[ck] = &cachedConn{key: key, client: c}
	s.conns.mu.Unlock()
	return c, nil
}

// discoverTools 向上游取工具清单（MCP）或解析接口文档（OpenAPI）。
func (s *Server) discoverTools(ctx context.Context, sp *assetSpec, client *mcp.Client) ([]*store.ConnectionTool, string, error) {
	var out []*store.ConnectionTool
	switch sp.Type {
	case "mcp":
		tools, err := client.ListTools(ctx)
		if err != nil {
			return nil, "", err
		}
		for _, t := range tools {
			name := openapi.ToolName(t.Name)
			schema := t.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			desc := t.Description
			if desc == "" {
				desc = t.Title
			}
			// MCP 工具默认启用；管理员可以逐个关掉或限定角色
			out = append(out, &store.ConnectionTool{Name: name, Description: desc, InputSchema: schema, ReadOnly: t.ReadOnly(), Enabled: true})
		}
		return out, client.Instructions, nil
	case "openapi":
		spec, err := openapi.Parse([]byte(sp.OpenAPI))
		if err != nil {
			return nil, "", fmt.Errorf("解析接口文档：%w", err)
		}
		if sp.Extension != nil {
			// 扩展：工具的说明、只读标记、模型能填哪些参数都照清单来
			bound, err := extension.Bind(extension.CheckInstalled([]byte(sp.Extension.Manifest), []byte(sp.OpenAPI)))
			if err != nil {
				return nil, "", err
			}
			for _, t := range bound {
				op, _ := json.Marshal(connOp{Operation: t.Op, Fixed: t.Fixed, Keep: t.Keep})
				out = append(out, &store.ConnectionTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema, Operation: op, ReadOnly: t.ReadOnly, Enabled: t.ReadOnly})
			}
			return out, "", nil
		}
		for _, t := range spec.Tools {
			op, _ := json.Marshal(t.Op)
			// 只有只读（GET）的接口默认启用，写操作要管理员确认后打开
			out = append(out, &store.ConnectionTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema, Operation: op, ReadOnly: t.ReadOnly, Enabled: t.ReadOnly})
		}
		return out, "", nil
	}
	return nil, "", errors.New("未知的连接类型")
}

// openAPIBase 是 OpenAPI 连接的请求根地址：填了就用填的，否则用文档里 servers 的第一项。
func openAPIBase(sp *assetSpec) (string, error) {
	if sp.URL != "" {
		return sp.URL, nil
	}
	spec, err := openapi.Parse([]byte(sp.OpenAPI))
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(spec.BaseURL, "http://") && !strings.HasPrefix(spec.BaseURL, "https://") {
		return "", errors.New("接口文档里没有完整的 servers 地址，请在连接里填写接口根地址")
	}
	return spec.BaseURL, nil
}

// syncConnectionTools 重新发现工具并替换清单，返回工具数。
// 连接带员工身份时，用 v（点同步的管理员）自己的身份去取工具清单；base 是他访问 dsh-ee 的地址。
func (s *Server) syncConnectionTools(ctx context.Context, id int64, v viewer, base string) (int, error) {
	a, err := s.store.GetAsset(ctx, id)
	if err != nil {
		return 0, err
	}
	lv, err := s.store.LiveAssetVersion(ctx, id)
	if err != nil {
		return 0, err
	}
	sp := parseSpec(lv.Spec)
	var client *mcp.Client
	if sp.Type == "mcp" {
		if client, err = s.connClient(ctx, a, lv.Version, sp, v, base); err != nil {
			if errors.Is(err, errNeedCredential) {
				return 0, errors.New("这个连接按员工本人的凭据调用：请先在连接页面填上你自己的凭据，再同步工具")
			}
			return 0, err
		}
	}
	ctx, cancel := context.WithTimeout(withCaller(ctx, v, base), 60*time.Second)
	defer cancel()
	tools, _, err := s.discoverTools(ctx, sp, client)
	if err != nil {
		s.dropConnClient(id)
		return 0, err
	}
	if err := s.store.ReplaceConnectionTools(ctx, id, tools); err != nil {
		return 0, err
	}
	s.hub.notify()
	return len(tools), nil
}

// callConnectionTool 以员工身份调用连接里的一个工具，返回 MCP 工具结果。
// v 是发起调用的员工，base 是他访问 dsh-ee 的地址（签 ee_jwt、给授权链接时用）。
func (s *Server) callConnectionTool(ctx context.Context, a *store.Asset, t *store.ConnectionTool, v viewer, base string, args json.RawMessage) json.RawMessage {
	start := time.Now()
	result, meta, err := s.doConnectionCall(ctx, a, t, v, base, args)
	call := store.ConnectionCall{AssetID: a.ID, Tool: t.Name, UserID: v.user.ID, OK: err == nil, LatencyMS: int(time.Since(start).Milliseconds()),
		AuthMode: meta.mode, Extension: meta.extension, Digest: meta.digest}
	if err != nil {
		call.Error = err.Error()
		result = mcp.TextResult("调用 "+a.Title+" 失败："+err.Error(), true)
		if errors.Is(err, errNeedCredential) {
			// 还没发请求：告诉模型该让员工去哪里填，别当成对方系统的故障去重试
			call.Error = "员工还没填自己的凭据"
			result = mcp.TextResult("调用 "+a.Title+" 需要你本人的凭据，你还没有填。请先打开 "+base+"/assets/"+strconv.FormatInt(a.ID, 10)+
				" ，在「我的凭据」里填好，再重试。不要用别人的凭据，也不要改用其他工具绕过。", true)
		} else if !t.ReadOnly && outcomeUnknown(err) {
			// 请求已经发出去了，只是没等到回复：对方可能已经执行。
			// 若照常说"失败"，模型会很自然地重试，写操作就可能做两遍（重复建单、重复发消息）。
			call.Error = "结果不确定：" + err.Error()
			result = mcp.TextResult("调用 "+a.Title+" 的结果不确定：请求已经发出，但没有等到回复（"+err.Error()+"）。"+
				"对方系统可能已经执行了这次操作。请先用查询类工具确认是否已经生效，不要直接重试；确认不了就把情况告诉用户。", true)
		}
	} else if isErrorResult(result) {
		call.OK = false
		call.Error = "上游返回错误"
	}
	if rerr := s.store.RecordConnectionCall(context.WithoutCancel(ctx), call); rerr != nil {
		s.log.Warn("记录连接调用失败", "err", rerr)
	}
	return result
}

// outcomeUnknown 判断错误是否发生在请求发出之后、拿到回复之前（超时、连接被断开）。
// 连不上（拨号失败）说明请求根本没发出去，不算。
func outcomeUnknown(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func isErrorResult(raw json.RawMessage) bool {
	var r struct {
		IsError bool `json:"isError"`
	}
	return json.Unmarshal(raw, &r) == nil && r.IsError
}

// callMeta 是一次调用要记进调用记录的背景：带的员工身份（空 / ee_jwt / user_secret），
// 以及这个连接是扩展装出来的话，是哪个扩展的哪个版本、清单的哈希。
type callMeta struct {
	mode, extension, digest string
}

// doConnectionCall 发出一次调用。
func (s *Server) doConnectionCall(ctx context.Context, a *store.Asset, t *store.ConnectionTool, v viewer, base string, args json.RawMessage) (json.RawMessage, callMeta, error) {
	var meta callMeta
	lv, err := s.store.LiveAssetVersion(ctx, a.ID)
	if err != nil {
		return nil, meta, err
	}
	sp := parseSpec(lv.Spec)
	if sp.UserAuth != nil {
		meta.mode = sp.UserAuth.Mode
	}
	if x := sp.Extension; x != nil {
		meta.extension, meta.digest = x.ID+"@"+x.Version, x.Digest
	}
	switch sp.Type {
	case "mcp":
		client, err := s.connClient(ctx, a, lv.Version, sp, v, base)
		if err != nil {
			return nil, meta, err
		}
		res, err := client.CallTool(withCaller(ctx, v, base), t.Name, args)
		if err != nil {
			if sp.UserAuth != nil {
				s.dropUserConnClient(a.ID, v.user.ID)
			} else {
				s.dropConnClient(a.ID)
			}
			return nil, meta, err
		}
		return limitMCPResult(res, connResultLimit), meta, nil
	case "openapi":
		var op connOp
		if err := json.Unmarshal(t.Operation, &op); err != nil {
			return nil, meta, errors.New("工具缺少接口信息，请重新同步")
		}
		root, err := openAPIBase(sp)
		if err != nil {
			return nil, meta, err
		}
		h, err := s.connHeader(ctx, a.ID, sp, nil)
		if err != nil {
			return nil, meta, err
		}
		uh, err := s.userAuthHeader(ctx, a, sp, v, base)
		if err != nil {
			return nil, meta, err
		}
		for k, vs := range uh {
			h[k] = vs
		}
		var m map[string]any
		dec := json.NewDecoder(strings.NewReader(string(args)))
		dec.UseNumber()
		if len(args) > 0 {
			if err := dec.Decode(&m); err != nil {
				return nil, meta, errors.New("参数不是 JSON 对象")
			}
		}
		if len(op.Fixed) > 0 {
			// 不由模型填的参数：模型带了也不算，一律用管理员设置的值
			if m == nil {
				m = map[string]any{}
			}
			var values map[string]json.RawMessage
			if sp.Extension != nil {
				values = sp.Extension.Values
			}
			if err := applyFixed(m, op.Fixed, values); err != nil {
				return nil, meta, err
			}
		}
		limit := int64(connResultLimit + 1)
		if len(op.Keep) > 0 {
			// 要先把整个返回解析出来才能挑字段，多读一些；挑完再按统一的上限截断
			limit = keepReadLimit
		}
		status, body, err := openapi.Call(ctx, s.connHTTP(sp), root, op.Operation, m, h, limit)
		if err != nil {
			return nil, meta, err
		}
		if len(op.Keep) > 0 && status < 400 {
			if kept, ok := keepFields(body, op.Keep); ok {
				body = kept
			}
		}
		text := string(body)
		if len(body) > connResultLimit {
			text = cutUTF8(string(body), connResultLimit) + "\n…（结果过长，已截断）"
		}
		if status >= 400 {
			return mcp.TextResult(fmt.Sprintf("HTTP %d\n%s", status, text), true), meta, nil
		}
		return mcp.TextResult(text, false), meta, nil
	}
	return nil, meta, errors.New("未知的连接类型")
}

// limitMCPResult 让 MCP 连接的结果与 OpenAPI 连接一样受 connResultLimit 约束。
// 上游一次返回几 MB（整张表、整份日志）会直接撑爆模型上下文或白烧 token，
// 所以超长时文字块按剩余额度截断，非文字块（如 base64 图片）和 structuredContent 丢弃，
// 末尾提示模型缩小查询范围；isError 原样保留。
func limitMCPResult(raw json.RawMessage, limit int) json.RawMessage {
	if len(raw) <= limit {
		return raw
	}
	var r struct {
		Content []map[string]any `json:"content"`
		IsError bool             `json:"isError"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return mcp.TextResult("结果过长且无法解析，已丢弃；请缩小查询范围后重试", true)
	}
	var b strings.Builder
	left := limit
	dropped := 0
	for _, c := range r.Content {
		text, ok := c["text"].(string)
		if c["type"] != "text" || !ok {
			dropped++
			continue
		}
		if left <= 0 {
			break
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		if len(text) > left {
			text = cutUTF8(text, left)
		}
		b.WriteString(text)
		left -= len(text)
	}
	note := fmt.Sprintf("\n…（结果过长，只保留前 %d KB", limit>>10)
	if dropped > 0 {
		note += fmt.Sprintf("，另有 %d 个非文字内容未返回", dropped)
	}
	note += "；请缩小查询范围或分页后再查）"
	return mcp.TextResult(b.String()+note, r.IsError)
}

// cutUTF8 截到不超过 n 字节，且不切断多字节字符。
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// ---- 管理接口 ----

func (s *Server) loadConnection(w http.ResponseWriter, r *http.Request) (*store.Asset, bool) {
	a, ok := s.loadAsset(w, r)
	if !ok {
		return nil, false
	}
	if a.Kind != store.KindConnection {
		writeErr(w, http.StatusNotFound, "连接不存在")
		return nil, false
	}
	return a, true
}

// POST /api/admin/connections/{id}/sync：重新发现工具。
func (s *Server) handleConnSync(w http.ResponseWriter, r *http.Request) {
	a, ok := s.loadConnection(w, r)
	if !ok {
		return
	}
	s.dropConnClient(a.ID)
	n, err := s.syncConnectionTools(r.Context(), a.ID, viewerOf(r), s.callerBase(r))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tools": n})
}

type connToolReq struct {
	Enabled bool    `json:"enabled"`
	Roles   []int64 `json:"roles"`
}

// PATCH /api/admin/connections/{id}/tools/{name}：启用 / 停用工具，限定角色。
func (s *Server) handleConnTool(w http.ResponseWriter, r *http.Request) {
	a, ok := s.loadConnection(w, r)
	if !ok {
		return
	}
	var req connToolReq
	if !readJSON(w, r, &req) {
		return
	}
	name := r.PathValue("name")
	if err := s.store.SetConnectionTool(r.Context(), a.ID, name, req.Enabled, dedupe(req.Roles)); err != nil {
		if notFound(err) {
			writeErr(w, http.StatusNotFound, "工具不存在")
			return
		}
		s.internalErr(w, r, err)
		return
	}
	s.audit(r, auditConnTool, a.Slug+"__"+name, map[string]any{"enabled": req.Enabled, "roles": req.Roles})
	s.hub.notify()
	w.WriteHeader(http.StatusNoContent)
}

type connTestReq struct {
	Spec   json.RawMessage `json:"spec"`
	Secret *string         `json:"secret"`
	// 编辑已有连接时传 id：没填凭据就用已存的
	ID int64 `json:"id"`
}

// POST /api/admin/connections/test：用还没保存的配置试连，返回能发现的工具。
func (s *Server) handleConnTest(w http.ResponseWriter, r *http.Request) {
	var req connTestReq
	if !readJSONLimit(w, r, &req, 6<<20) {
		return
	}
	sp, _, ok := s.decodeSpec(w, r, store.KindConnection, req.Spec)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	h, err := s.connHeader(ctx, req.ID, sp, req.Secret)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var client *mcp.Client
	if sp.Type == "mcp" {
		client = &mcp.Client{URL: sp.URL, Header: h, HC: s.connHTTP(sp)}
		if sp.UserAuth != nil {
			// 试连时用点「测试」的管理员自己的身份；user_secret 的连接还没保存时没法填凭据，只带共享凭据去试
			var probe *store.Asset
			if req.ID > 0 {
				probe, _ = s.store.GetAsset(ctx, req.ID)
			}
			if probe == nil {
				probe = &store.Asset{Slug: "test"}
			}
			v, base := viewerOf(r), s.callerBase(r)
			if _, err := s.userAuthHeader(ctx, probe, sp, v, base); err == nil {
				client.Dynamic = func(ctx context.Context) (http.Header, error) { return s.userAuthHeader(ctx, probe, sp, v, base) }
			} else if !errors.Is(err, errNeedCredential) {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
				return
			}
		}
	}
	start := time.Now()
	tools, instructions, err := s.discoverTools(ctx, sp, client)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "latency_ms": time.Since(start).Milliseconds()})
		return
	}
	resp := map[string]any{"ok": true, "tools": tools, "instructions": instructions, "latency_ms": time.Since(start).Milliseconds()}
	if sp.Type == "openapi" {
		if base, err := openAPIBase(sp); err == nil {
			resp["base_url"] = base
		} else {
			resp["ok"], resp["error"] = false, err.Error()
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// callerBase 是这次请求的人访问 dsh-ee 用的地址：系统设置里填了「访问地址」就用它，否则按请求推断。
// 签 ee_jwt 时作签发方，所以要稳定：生产环境应当在系统设置里填好。
func (s *Server) callerBase(r *http.Request) string {
	base, err := s.connectBase(r.Context(), r)
	if err != nil {
		return ""
	}
	return base
}
