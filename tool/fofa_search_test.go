package tool

// fofa_search_test.go —— 契约测试：参数钳制/未配置行为/注入钩子/输出形。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestFofaSearchDisabledWithoutHook(t *testing.T) {
	old := FofaQueryFunc
	FofaQueryFunc = nil
	defer func() { FofaQueryFunc = old }()
	res, err := runFofaSearch(context.Background(), json.RawMessage(`{"query":"domain=\"x\""}`), nil)
	if err == nil {
		t.Fatal("未配置应报错（不静默）")
	}
	if !res.IsError {
		t.Fatal("未配置应是错误结果形")
	}
}

func TestFofaSearchClampsSize(t *testing.T) {
	old := FofaQueryFunc
	defer func() { FofaQueryFunc = old }()
	var gotSize int
	FofaQueryFunc = func(_ context.Context, _ string, _ []string, size int) ([][]string, error) {
		gotSize = size
		return nil, nil
	}
	// 超限钳制 ≤10000。
	if _, err := runFofaSearch(context.Background(), json.RawMessage(`{"query":"q","size":999999}`), nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotSize != fofaSearchMaxSize {
		t.Fatalf("size 应钳制到 %d: got %d", fofaSearchMaxSize, gotSize)
	}
	// 缺省 100。
	if _, err := runFofaSearch(context.Background(), json.RawMessage(`{"query":"q"}`), nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotSize != 100 {
		t.Fatalf("缺省 size=100: got %d", gotSize)
	}
}

func TestFofaSearchEmptyQueryRejected(t *testing.T) {
	old := FofaQueryFunc
	FofaQueryFunc = func(context.Context, string, []string, int) ([][]string, error) { return nil, nil }
	defer func() { FofaQueryFunc = old }()
	if _, err := runFofaSearch(context.Background(), json.RawMessage(`{"query":"   "}`), nil); err == nil {
		t.Fatal("空 query 应报错")
	}
}

func TestFofaSearchInjectionHookAndOutput(t *testing.T) {
	old := FofaQueryFunc
	defer func() { FofaQueryFunc = old }()
	var gotQuery string
	var gotFields []string
	FofaQueryFunc = func(_ context.Context, query string, fields []string, size int) ([][]string, error) {
		gotQuery, gotFields = query, fields
		return [][]string{{"a.example", "1.2.3.4", "443"}}, nil
	}
	res, err := runFofaSearch(context.Background(), json.RawMessage(`{"query":"domain=\"example.com\""}`), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotQuery != `domain="example.com"` {
		t.Fatalf("query 应原样透传: %q", gotQuery)
	}
	if strings.Join(gotFields, ",") != "host,ip,port,title" {
		t.Fatalf("缺省 fields: %v", gotFields)
	}
	if res.IsError || len(res.Content) == 0 {
		t.Fatalf("成功形: %+v", res)
	}
	// 输出含结果行+围栏提示（URL 不自动访问）。
	out := res.Content[0].Text
	if !strings.Contains(out, "a.example") || !strings.Contains(out, "WebFetch") {
		t.Fatalf("输出应含结果+围栏提示: %q", out)
	}
}

func TestFofaSearchSpecReadonlyContract(t *testing.T) {
	if !FofaSearchSpec.ReadOnly(nil) {
		t.Fatal("只读契约：查询工具必须 ReadOnly=true")
	}
	if !FofaSearchSpec.Concurrent(nil) {
		t.Fatal("并发契约：查询互不相扰")
	}
	if FofaSearchSpec.Name != "fofa_search" {
		t.Fatalf("工具名契约: %q", FofaSearchSpec.Name)
	}
}
