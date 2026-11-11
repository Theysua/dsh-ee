package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hikariming/dsh-ee/server/internal/extension"
)

func TestKeepFields(t *testing.T) {
	cases := []struct {
		name, body string
		keep       []string
		want       string // 空表示原样返回（ok 为 false）
	}{
		{"对象：留第一层的字段", `{"id":1,"name":"a","secret":"x"}`, []string{"id", "name", "missing"}, `{"id":1,"name":"a"}`},
		{"数组：留每一项的字段", `[{"id":1,"x":2},{"id":3,"x":4},"不是对象的原样留着"]`, []string{"id"}, `[{"id":1},{"id":3},"不是对象的原样留着"]`},
		{"包了一层的列表", `{"items":[{"id":1,"big":"…"},{"id":2,"big":"…"}],"total":2,"trace":"t"}`, []string{"items.id", "total"}, `{"items":[{"id":1},{"id":2}],"total":2}`},
		{"第二层是对象", `{"page":{"no":1,"size":20,"cursor":"abc"},"rows":[]}`, []string{"page.no", "page.size"}, `{"page":{"no":1,"size":20}}`},
		{"整个留和只留一部分都写了：整个留", `{"items":[{"id":1,"x":2}]}`, []string{"items", "items.id"}, `{"items":[{"id":1,"x":2}]}`},
		{"数组里每一项再往里一层", `[{"id":1,"owner":{"name":"a","phone":"1"}}]`, []string{"id", "owner.name"}, `[{"id":1,"owner":{"name":"a"}}]`},
		{"大数不走样", `{"id":12345678901234567890,"amount":0.10}`, []string{"id", "amount"}, `{"amount":0.10,"id":12345678901234567890}`},
		{"包了一层却直接写每一项的字段：什么都留不下", `{"items":[{"id":1}]}`, []string{"id"}, `{}`},
		{"不是 JSON", `<html>502</html>`, []string{"id"}, ""},
		{"既不是对象也不是数组", `"ok"`, []string{"id"}, ""},
		{"被截断的 JSON", `{"items":[{"id":1},{"id"`, []string{"items.id"}, ""},
	}
	for _, c := range cases {
		got, ok := keepFields([]byte(c.body), c.keep)
		if c.want == "" {
			if ok {
				t.Errorf("%s：应当原样返回，却得到 %s", c.name, got)
			}
			continue
		}
		if !ok || string(got) != c.want {
			t.Errorf("%s：得到 %s（ok=%v），期望 %s", c.name, got, ok, c.want)
		}
	}
}

func TestApplyFixed(t *testing.T) {
	fixed := []extension.BoundParam{
		{Name: "tenant", Key: "tenant", Fill: "admin", Required: true},
		{Name: "page_size", Key: "page_size", Fill: "app"},
		{Name: "body.company", Key: "company", Fill: "admin"},
	}
	values := map[string]json.RawMessage{"tenant": json.RawMessage(`"t-1"`), "company": json.RawMessage(`"A01"`)}
	args := func(s string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	show := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return string(b)
	}

	// 模型带了这些参数：一律换成管理员设置的；没设置的可选参数，模型带的也要拿掉
	m := args(`{"status":"done","tenant":"other","page_size":9999,"body":{"days":2,"company":"B02"}}`)
	if err := applyFixed(m, fixed, values); err != nil {
		t.Fatal(err)
	}
	if got := show(m); got != `{"body":{"company":"A01","days":2},"status":"done","tenant":"t-1"}` {
		t.Fatalf("补完参数 = %s", got)
	}
	// 模型没带请求体：补出来
	m = args(`{}`)
	if err := applyFixed(m, fixed, values); err != nil || show(m) != `{"body":{"company":"A01"},"tenant":"t-1"}` {
		t.Fatalf("没带请求体时 = %s %v", show(m), err)
	}
	// 模型把请求体传成了别的东西：不能借此绕过
	m = args(`{"body":"company=B02"}`)
	if err := applyFixed(m, fixed, values); err != nil || show(m) != `{"body":{"company":"A01"},"tenant":"t-1"}` {
		t.Fatalf("请求体不是对象时 = %s %v", show(m), err)
	}
	// 必填的还没设置：不发请求，告诉模型去找管理员，而不是让它自己填
	m = args(`{"tenant":"other"}`)
	err := applyFixed(m, fixed, map[string]json.RawMessage{"company": json.RawMessage(`"A01"`)})
	if err == nil || !strings.Contains(err.Error(), "tenant") || !strings.Contains(err.Error(), "管理员") {
		t.Fatalf("必填参数没设置应当报错：%v", err)
	}
}
