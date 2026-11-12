package tool

// websearch_searxng.go —— SearXNG 搜索后端。
//
// 为什么：自托管元搜索（SearXNG）= 无 key、可私有部署的搜索源；对目标面调查
// 的价值=查询不出本地实例范围，且结果聚合多引擎。
//
// 契约：SearXNG JSON API `GET {endpoint}/search?q=…&format=json` →
// {results:[{title,url,content,prefix}…]}；Search 语义与 searchProvider 接口
// 同构（只搜不取内容）。
//
// ⚠️ 出站隔离口径（放宽须注释取舍）：SearXNG 部署形态=本地/私有
// 实例，**显式允许环回/私有地址**——这是有意放宽：搜索后端
// 是用户自选的基础设施端点（与 LLM base URL 同级信任），不是不可信目标响应面；
// 外部输入（搜索结果 URL）仍走 WebFetch 的既有围栏。若部署公网 SearXNG，行为
// 与普通 HTTPS API 相同（证书校验保持开启，不做 InsecureSkipVerify）。
//
// 错误面取舍：Search 失败=返回 error（与接口契约一致「不 panic」）——上层
// （web_search 工具）已有失败不炸 run 的语义（工具错误回给模型改道）；**不做
// 自动回退 default**（静默换后端=用户不知情的行为漂移，宁可见错）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// searxngEndpointDefault 是本机 SearXNG 实例的缺省位（用户可在配置覆盖）。
const searxngEndpointDefault = "http://127.0.0.1:8888"

type searxngProvider struct {
	client   *http.Client
	endpoint string
}

// searxngResponse 是 SearXNG format=json 的响应面（只取 searchProvider 需要的字段）。
type searxngResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

func (p *searxngProvider) Name() string { return "searxng" }

func (p *searxngProvider) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	endpoint := strings.TrimRight(p.endpoint, "/")
	u, err := url.Parse(endpoint + "/search")
	if err != nil {
		return nil, fmt.Errorf("web_search: searxng endpoint: %w", err)
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	q.Set("language", "auto")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web_search: searxng request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("web_search: searxng status %d", resp.StatusCode)
	}
	var out searxngResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("web_search: searxng decode: %w", err)
	}
	hits := make([]SearchResult, 0, limit)
	for i, r := range out.Results {
		if i >= limit {
			break
		}
		if r.URL == "" {
			continue
		}
		hits = append(hits, SearchResult{
			Title:       r.Title,
			URL:         r.URL,
			Description: r.Content,
			Position:    i + 1,
		})
	}
	return hits, nil
}
