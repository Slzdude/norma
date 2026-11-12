package tool

import "testing"

// TestHTTPUserAgentContract：出站 UA 契约——
// 缺省浏览器伪装（无工具链指纹）、可注入覆盖、恢复缺省。
func TestHTTPUserAgentContract(t *testing.T) {
	if DefaultHTTPUserAgent != "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0" {
		t.Fatalf("缺省 UA 应为浏览器伪装，got %q", DefaultHTTPUserAgent)
	}
	for _, bad := range []string{"norma/", "curl/", "bot"} {
		if contains(DefaultHTTPUserAgent, bad) {
			t.Fatalf("缺省 UA 不得含工具链指纹 %q: %q", bad, DefaultHTTPUserAgent)
		}
	}
	defer func() { HTTPUserAgent = DefaultHTTPUserAgent }()
	HTTPUserAgent = "custom-agent/1.0"
	if HTTPUserAgent != "custom-agent/1.0" {
		t.Fatal("注入覆盖失败")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
