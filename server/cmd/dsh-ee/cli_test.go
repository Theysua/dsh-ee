package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const exampleDir = "../../../extensions/examples/demo-declarative"

func cli(args ...string) (handled bool, code int, out, errOut string) {
	var o, e bytes.Buffer
	handled, code = runCLI(args, &o, &e)
	return handled, code, o.String(), e.String()
}

func TestCLI(t *testing.T) {
	// 没有子命令、或不是这里的子命令：交回去启动服务
	if handled, _, _, _ := cli(); handled {
		t.Fatal("没有参数时应启动服务")
	}
	if handled, _, _, _ := cli("serve"); handled {
		t.Fatal("不认识的命令不归这里管")
	}

	// 示例扩展：通过，退出码 0；--json 给出身份、摘要和工具
	_, code, out, _ := cli("extension", "check", exampleDir)
	if code != 0 || !strings.Contains(out, "dsh-ee/demo-itsm@0.1.0") || !strings.Contains(out, "通过：0 个错误，0 个提醒") || !strings.Contains(out, "create_ticket  POST /demo/itsm/tickets  （写操作，调用前要员工确认）") {
		t.Fatalf("示例应通过：%d\n%s", code, out)
	}
	_, code, out, _ = cli("extension", "check", "--json", exampleDir)
	var res struct {
		OK       bool
		Identity string
		Digest   string
		Errors   int
		Tools    []struct {
			Name     string
			ReadOnly bool `json:"readOnly"`
		}
		Results []map[string]string
	}
	if code != 0 || json.Unmarshal([]byte(out), &res) != nil || !res.OK || res.Identity != "dsh-ee/demo-itsm@0.1.0" || len(res.Digest) != 64 || len(res.Tools) != 4 || res.Results == nil {
		t.Fatalf("--json 输出不对：%d\n%s", code, out)
	}

	// 弄坏的扩展：退出码 1，每条结果都有位置和改法
	dir := t.TempDir()
	for _, name := range []string{"dsh-ee.extension.json", "openapi.yaml"} {
		raw, err := os.ReadFile(filepath.Join(exampleDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "dsh-ee.extension.json" {
			raw = bytes.Replace(raw, []byte(`"name": "create_ticket",`), []byte(`"name": "create_ticket", "readOnly": true,`), 1)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, code, out, _ = cli("extension", "check", dir, "--json")
	if code != 1 || json.Unmarshal([]byte(out), &res) != nil || res.OK || res.Errors != 1 || len(res.Results) != 1 {
		t.Fatalf("把写操作标成只读应报错：%d\n%s", code, out)
	}
	if d := res.Results[0]; d["code"] != "tool.readonly_suspect" || d["severity"] != "error" || d["where"] != "dsh-ee.extension.json#/tools/3" || !strings.Contains(d["message"], "POST") || !strings.Contains(d["fix"], "tools[3]") {
		t.Fatalf("结果要说清楚是哪个工具、怎么改：%v", d)
	}
	_, code, out, _ = cli("extension", "check", dir)
	if code != 1 || !strings.Contains(out, "错误  tool.readonly_suspect") || !strings.Contains(out, "改法：") || !strings.Contains(out, "没通过：1 个错误") {
		t.Fatalf("给人看的输出：%d\n%s", code, out)
	}

	// 用法不对：退出码 2，说明怎么用
	for _, args := range [][]string{{"extension"}, {"extension", "check"}, {"extension", "check", "a", "b"}, {"extension", "check", exampleDir, "--verbose"}, {"extension", "check", filepath.Join(dir, "nope")}, {"schema"}, {"schema", "get"}, {"schema", "dump"}} {
		if _, code, _, errOut := cli(args...); code != 2 || errOut == "" {
			t.Fatalf("%v：应是退出码 2 并说明用法，实际 %d %q", args, code, errOut)
		}
	}

	// 规格
	_, code, out, _ = cli("schema", "list")
	if code != 0 || !strings.Contains(out, "manifest") || !strings.Contains(out, "errors") {
		t.Fatalf("schema list：%d\n%s", code, out)
	}
	_, code, out, _ = cli("schema", "get", "manifest")
	var schema map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &schema) != nil || schema["title"] == nil {
		t.Fatalf("schema get manifest：%d\n%s", code, out)
	}
	_, code, out, _ = cli("schema", "get", "errors")
	if code != 0 || !strings.Contains(out, "tool.readonly_suspect") {
		t.Fatalf("schema get errors：%d\n%s", code, out)
	}
	if _, code, _, errOut := cli("schema", "get", "nope"); code != 1 || !strings.Contains(errOut, "schema list") {
		t.Fatalf("不存在的规格：%d %q", code, errOut)
	}
	if _, code, out, _ := cli("extension", "check", "--help"); code != 0 || !strings.Contains(out, "extension check") {
		t.Fatalf("子命令后面的 --help 也应当给用法：%d\n%s", code, out)
	}
	if _, code, out, _ := cli("help"); code != 0 || !strings.Contains(out, "extension check") {
		t.Fatalf("help：%d\n%s", code, out)
	}
}
