package suricata

import (
	"fmt"
	"strings"
	"testing"
)

const ruleraw = `alert http any any -> any any (msg:httptest;content:"/";http.uri;content:"/";http.uri.raw;content:GET;http.method;content:HTTP/1.1;http.protocol;content:"GET / HTTP/1.1|0d 0a|";http.request_line;content:"Mozilla/5.0 (Windows NT; Windows NT 10.0; zh-CN) WindowsPowerShell/5.1.22621.1778";http.user_agent;endswith;content:"|0d 0a|Accept-Encoding|0d 0a|Host|0d 0a|User-Agent|0d 0a 0d 0a|";http.header_names;)`

func TestNewSurigen(t *testing.T) {
	rules := strings.SplitN(ruleraw, "\n", -1)
	for _, rule := range rules {
		r, err := Parse(rule)
		if err != nil {
			t.Error(err)
		}
		surigen, err := NewPloadgen(r[0].ContentRuleConfig.ContentRules)
		if err != nil {
			t.Error(err)
		}
		gen, err := surigen.Gen()
		if err != nil {
			return
		}
		fmt.Println(string(gen))
	}
}