package webui

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"tradeforge/internal/agent"
)

func TestHandleSettingsShowWhenUnconfigured(t *testing.T) {
	srv := newTestServerWithAgent(t, newFakeStore(), nil)
	w := getPage(srv, "/settings")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "尚未配置") {
		t.Errorf("未配置时应提示尚未配置，实际：%s", w.Body.String())
	}
}

func TestHandleSettingsSaveConfiguresAgentAndEnablesWizard(t *testing.T) {
	srv := newTestServerWithAgent(t, newFakeStore(), nil)

	w := postForm(srv, "/settings", url.Values{
		"action": {"save"}, "provider": {string(agent.ProviderOpenAI)},
		"api_key": {"sk-test-1234567890"}, "model": {""},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "已保存") {
		t.Errorf("保存成功应提示已保存，实际：%s", body)
	}
	if !strings.Contains(body, agent.ProviderOpenAI.Label()) {
		t.Errorf("应展示当前生效的供应商，实际：%s", body)
	}
	if !strings.Contains(body, agent.DefaultOpenAIModel) {
		t.Errorf("未指定模型时应展示供应商默认模型，实际：%s", body)
	}
	if strings.Contains(body, "sk-test-1234567890") {
		t.Error("绝不能把完整 API key 回显到页面上")
	}

	if currentAgent(srv) == nil {
		t.Fatal("保存成功后 Server 应持有一个可用的 Agent")
	}

	// 向导现在应该认为 Agent 已就绪。
	wizard := getPage(srv, "/wizard")
	if strings.Contains(wizard.Body.String(), "未就绪") {
		t.Errorf("配置成功后向导页面不该再显示未就绪，实际：%s", wizard.Body.String())
	}
}

func TestHandleSettingsSaveHonorsExplicitModel(t *testing.T) {
	srv := newTestServerWithAgent(t, newFakeStore(), nil)
	w := postForm(srv, "/settings", url.Values{
		"action": {"save"}, "provider": {string(agent.ProviderAnthropic)},
		"api_key": {"sk-ant-test-1234"}, "model": {"claude-sonnet-5"},
	})
	if !strings.Contains(w.Body.String(), "claude-sonnet-5") {
		t.Errorf("应展示用户指定的模型，实际：%s", w.Body.String())
	}
}

func TestHandleSettingsSaveRejectsEmptyKey(t *testing.T) {
	srv := newTestServerWithAgent(t, newFakeStore(), nil)
	w := postForm(srv, "/settings", url.Values{
		"action": {"save"}, "provider": {string(agent.ProviderAnthropic)}, "api_key": {""},
	})
	if !strings.Contains(w.Body.String(), "banner-err") {
		t.Errorf("空 key 应报错，实际：%s", w.Body.String())
	}
	if currentAgent(srv) != nil {
		t.Error("空 key 不该配置出一个可用的 Agent")
	}
}

func TestHandleSettingsSaveCustomProviderRequiresBaseURL(t *testing.T) {
	srv := newTestServerWithAgent(t, newFakeStore(), nil)
	w := postForm(srv, "/settings", url.Values{
		"action": {"save"}, "provider": {string(agent.ProviderCustom)},
		"api_key": {"some-key"}, "model": {"my-local-model"},
		// base_url 故意留空。
	})
	if !strings.Contains(w.Body.String(), "banner-err") {
		t.Errorf("自定义供应商缺 API 地址应报错，实际：%s", w.Body.String())
	}

	w = postForm(srv, "/settings", url.Values{
		"action": {"save"}, "provider": {string(agent.ProviderCustom)},
		"api_key": {"some-key"}, "model": {"my-local-model"},
		"base_url": {"http://localhost:11434/v1"},
	})
	if !strings.Contains(w.Body.String(), "已保存") {
		t.Errorf("参数齐全时应保存成功，实际：%s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "my-local-model") {
		t.Errorf("应展示用户填写的模型名，实际：%s", w.Body.String())
	}
}

func TestHandleSettingsSaveRejectsUnknownProvider(t *testing.T) {
	srv := newTestServerWithAgent(t, newFakeStore(), nil)
	w := postForm(srv, "/settings", url.Values{
		"action": {"save"}, "provider": {"gemini"}, "api_key": {"some-key"},
	})
	if !strings.Contains(w.Body.String(), "banner-err") {
		t.Errorf("未知供应商应报错，实际：%s", w.Body.String())
	}
}

// 一次失败的保存尝试不该把已经在正常工作的 Agent 顶掉。
func TestHandleSettingsSaveFailureDoesNotClearWorkingAgent(t *testing.T) {
	stub := newTestAgent(configJSON)
	srv := newTestServerWithAgent(t, newFakeStore(), stub)

	w := postForm(srv, "/settings", url.Values{
		"action": {"save"}, "provider": {string(agent.ProviderAnthropic)}, "api_key": {""},
	})
	if !strings.Contains(w.Body.String(), "banner-err") {
		t.Fatalf("期望报错，响应：%s", w.Body.String())
	}
	if currentAgent(srv) != stub {
		t.Error("失败的保存尝试不该替换掉原本正常工作的 Agent")
	}
}

func TestHandleSettingsClearRemovesAgent(t *testing.T) {
	srv := newTestServerWithAgent(t, newFakeStore(), newTestAgent(configJSON))
	if currentAgent(srv) == nil {
		t.Fatal("测试前提：应先有一个已配置的 Agent")
	}

	w := postForm(srv, "/settings", url.Values{"action": {"clear"}})
	if !strings.Contains(w.Body.String(), "已清除") {
		t.Errorf("应提示已清除，实际：%s", w.Body.String())
	}
	if currentAgent(srv) != nil {
		t.Error("清除后 currentAgent 应为 nil")
	}
}

// 启动时通过环境变量配置的 Agent，设置页面应能看到"已配置"但不知道具体供应商。
func TestSettingsShowsEnvConfiguredAgentAsUnknownProvider(t *testing.T) {
	srv := newTestServerWithAgent(t, newFakeStore(), newTestAgent(configJSON))
	w := getPage(srv, "/settings")
	body := w.Body.String()
	if !strings.Contains(body, "已配置") {
		t.Errorf("应展示已配置，实际：%s", body)
	}
	if strings.Contains(body, "尚未配置") {
		t.Errorf("不该同时展示未配置，实际：%s", body)
	}
}
