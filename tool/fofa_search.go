package tool

// fofa_search.go —— fofa_search LLM 工具。
//
// 为什么：FOFA 被动发现从 enrich 管道（自动补图）升级为 **agent 可调工具**——
// 模型在侦察/扩面阶段自主查询资产面（子域/端口/指纹），比人肉预设触发更贴
// 节奏。薄工具形：查询逻辑在 FOFA 客户端（凭据掩码/节流/审计四全
// 纪律不动），本文件只做参数钳制+归一化输出。
//
// 分层纪律：norma/tool **不 import enrich**（norma 是内化 SDK，依赖方向不反）——
// 查询面走包级注入钩子（照 DefaultHTTPUserAgent 前例），server/manager.go 装配。
//
// 域内围栏：结果 URL **不自动跟抓**（WebFetch 围栏口径——发现≠授权访问，模型
// 如需进一步取证走 WebFetch 由 intercept 面裁决）。
//
// ⚠️ intercept 覆盖评估登记（§7.2 铁律）：见本文件末评估记录注记。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Slzdude/norma/permission"
)

// FofaQueryFunc 是注入钩子形（宿主侧绑定 FOFA 客户端查询）：
// (query, fields, size) → rows。nil=工具未配置（报错不静默）。
var FofaQueryFunc func(ctx context.Context, query string, fields []string, size int) ([][]string, error)

// fofaSearchMaxSize 是单次返回钳制（FOFA 契约 size≤10000）。
const fofaSearchMaxSize = 10000

// FofaSearchSpec 是工具声明（Build 补 fail-closed 缺省）。
var FofaSearchSpec = Spec{
	Name:        "fofa_search",
	Description: "FOFA 网络空间测绘查询：按 FOFA 语法发现资产（子域/IP/端口/指纹）。只读被动查询——结果 URL 不自动访问（如需进一步取证用 WebFetch）。",
	Prompt: `用 FOFA 查询语法发现资产。常用形：domain="example.com"（子域）、host="example.com"（主机面）、ip="1.2.3.4"（IP 面）、port="443" && app="Nginx"（指纹）。
频率纪律：间隔 ≥2s、单次 size ≤10000、能用时间过滤（after="2024-01-01"）省额度就用；拿不准总数时用 A-B 两次查询对照校验。`,
	Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "FOFA 查询语法（如 domain=\"example.com\"）",
			},
			"fields": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "返回字段（host,ip,port,title,app…）；缺省 host,ip,port,title",
			},
			"size": map[string]any{
				"type":        "integer",
				"description": "返回条数上限（缺省 100，钳制 ≤10000）",
			},
			"full": map[string]any{
				"type":        "boolean",
				"description": "检索全部历史（默认仅一年；省额度请配合时间过滤）",
			},
		},
		"required": []string{"query"},
	},
	ReadOnly:   func(json.RawMessage) bool { return true }, // 只读被动查询
	Concurrent: func(json.RawMessage) bool { return true }, // 查询互不相扰
	Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
		return permission.Allowed()
	},
	Run: runFofaSearch,
}

func runFofaSearch(ctx context.Context, input json.RawMessage, _ *ToolContext) (Result, error) {
	if FofaQueryFunc == nil {
		return Errorf("fofa_search 工具未配置（凭据未就绪或装配缺位）"), fmt.Errorf("fofa_search: disabled")
	}
	var args struct {
		Query  string   `json:"query"`
		Fields []string `json:"fields"`
		Size   int      `json:"size"`
		Full   bool     `json:"full"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return Errorf("参数解析失败"), err
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return Errorf("query 不能为空"), fmt.Errorf("fofa_search: empty query")
	}
	size := args.Size
	if size <= 0 {
		size = 100
	}
	if size > fofaSearchMaxSize {
		size = fofaSearchMaxSize // 钳制：FOFA 契约上限
	}
	fields := args.Fields
	if len(fields) == 0 {
		fields = []string{"host", "ip", "port", "title"}
	}
	if args.Full && !strings.Contains(query, "after=") && !strings.Contains(query, "before=") {
		query += " && is_honeypot=false" // full 面的最低限度真实性过滤（时间窗由用户按需加）
	}
	rows, err := FofaQueryFunc(ctx, query, fields, size)
	if err != nil {
		return Errorf("查询失败：" + err.Error()), err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "FOFA 查询 %q（fields=%s，返回 %d 行）：\n", query, strings.Join(fields, ","), len(rows))
	for i, r := range rows {
		fmt.Fprintf(&b, "%d. %s\n", i+1, strings.Join(r, " | "))
	}
	if len(rows) == 0 {
		b.WriteString("（无结果——检查语法或放宽条件）\n")
	}
	b.WriteString("提示：结果 URL 不自动访问；取证走 WebFetch（授权面裁决）。")
	return Text(b.String()), nil
}

/*
工具授权覆盖评估登记：

	工具：fofa_search（LLM 可调）
	评估结论：**allow + 审计档**（预判）——依据：
	  1) 只读被动查询：零直连目标面（只出站 FOFA API），无写入/执行能力；
	  2) 频控既有：FofaClient 并发 ≤2 + 429/5xx 退避（额度友好）；
	  3) 审计既有：每次查询 log 一行（query/fields/rows/cost，凭据掩码）；
	  4) 结果围栏：URL 不自动跟抓（发现≠授权访问，WebFetch 围栏口径）。
	登记面取舍：norma 内建族口径（Spec.Build 的 Permissions=Allow 即登记态）——
	  不占 tools 表行（与 TodoWrite/Read 等内建族同口径）；
	  若部署要求显式 tools 表登记，可在管理面补行（两者等效，取简）。
	残余风险：查询语义本身即「对第三方数据面的读取」——F 点消耗=用户额度，
	  Prompt 里频率纪律已给（≥2s 间隔/时间过滤省额度）。
*/
