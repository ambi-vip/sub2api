package service

import (
	"net/http"
	"time"
)

func (s *OpenAIGatewayService) SetPluginManager(manager *PluginManager) {
	s.pluginManager = manager
}

// doOpenAIUpstream 只在 OpenAI OAuth 能力绑定已启用时把真实请求交给插件。
// 插件返回标准 http.Response，响应解析、错误映射、SSE 和计费仍由现有核心链处理。
func (s *OpenAIGatewayService) doOpenAIUpstream(request *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	ticket, _ := request.Context().Value(openAICodexTicketRequestKey{}).(*openAICodexTicket)
	started := time.Now()
	pluginHandled := false
	if ticket != nil {
		proxyURL = ticket.ProxyURL
		request = request.WithContext(WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(request.Context(), HTTPUpstreamProfileOpenAIHarvest)))
		setOpenAICodexTicketHeaders(request.Header, ticket)
	}
	wrap := func(response *http.Response, err error) (*http.Response, error) {
		if ticket != nil {
			observation := &codexTicketGenerationLog{account: account, ticket: ticket, transport: "http", started: started, pluginHandled: pluginHandled,
				drop: func(reason string) { s.invalidateOpenAICodexTicketSignal(account, ticket, reason, "") }}
			if response != nil {
				observation.status = response.StatusCode
			}
			if err == nil && response != nil && response.Body != nil && response.StatusCode == http.StatusOK {
				response.Body = &openAICodexTicketResponseBody{ReadCloser: response.Body, observe: func(payload []byte) {
					observation.observe(payload)
					s.invalidateOpenAICodexTicket(account, ticket, payload)
				}, finish: observation.finish}
			} else {
				observation.finish(err)
			}
		}
		return response, err
	}
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			pluginHandled = true
			return wrap(response, err)
		}
	}
	return wrap(s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency))
}

// doOpenAIAccountTestUpstream 让 OpenAI OAuth 账号测试与真实转发使用同一插件路径。
// API Key 和未命中插件的账号保持各自原有的 HTTPUpstream 行为。
func (s *AccountTestService) doOpenAIAccountTestUpstream(
	request *http.Request,
	proxyURL string,
	account *Account,
	useTLSFallback bool,
) (*http.Response, error) {
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			return response, err
		}
	}
	if useTLSFallback {
		return s.httpUpstream.DoWithTLS(
			request,
			proxyURL,
			account.ID,
			account.Concurrency,
			s.tlsFPProfileService.ResolveTLSProfile(account),
		)
	}
	return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
}
