package tool

// websearch_searxng_test.go —— 契约测试：searchProvider 切换（缺省保现
// 行为/searxng 后端解析/结果归一化）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchProviderDefaultStaysDDGS(t *testing.T) {
	p, err := newSearchProvider(WebSearchConfig{})
	if err != nil {
		t.Fatalf("空配置不应报错: %v", err)
	}
	if p.Name() != "ddgs" {
		t.Fatalf("缺省后端应保持 ddgs（现行为不漂移）: got %q", p.Name())
	}
}

func TestSearchProviderSearXNGSelected(t *testing.T) {
	p, err := newSearchProvider(WebSearchConfig{Backend: "searxng"})
	if err != nil {
		t.Fatalf("searxng 后端应可构造: %v", err)
	}
	if p.Name() != "searxng" {
		t.Fatalf("后端名应为 searxng: got %q", p.Name())
	}
	sp, ok := p.(*searxngProvider)
	if !ok {
		t.Fatalf("工厂应返回 *searxngProvider: %T", p)
	}
	if sp.endpoint != searxngEndpointDefault {
		t.Fatalf("空 endpoint 应落缺省位: got %q", sp.endpoint)
	}
}

func TestSearxngSearchParsesResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("路径应为 /search: %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("format") != "json" {
			t.Errorf("format=json 必须带: %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"T1","url":"https://a.example","content":"C1"},
			{"title":"T2","url":"https://b.example","content":"C2"},
			{"title":"无URL","url":"","content":"跳过"}
		]}`))
	}))
	defer srv.Close()

	p := &searxngProvider{client: srv.Client(), endpoint: srv.URL}
	hits, err := p.Search(context.Background(), "测试查询", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("空 URL 行应跳过: got %d 行", len(hits))
	}
	if hits[0].URL != "https://a.example" || hits[0].Position != 1 || hits[1].Position != 2 {
		t.Fatalf("归一化契约不符: %+v", hits)
	}
}

func TestSearxngSearchLimitAndError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"title":"x","url":"https://x","content":""}]}`))
	}))
	defer srv.Close()
	p := &searxngProvider{client: srv.Client(), endpoint: srv.URL}
	hits, err := p.Search(context.Background(), "q", 1)
	if err != nil || len(hits) != 1 {
		t.Fatalf("limit=1 应只取 1 行: hits=%d err=%v", len(hits), err)
	}

	// 错误面：非 200=报错不 panic（接口契约）。
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	p2 := &searxngProvider{client: bad.Client(), endpoint: bad.URL}
	if _, err := p2.Search(context.Background(), "q", 10); err == nil {
		t.Fatal("500 应返回 error（不 panic 不静默）")
	}
}
