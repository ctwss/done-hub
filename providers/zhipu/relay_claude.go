package zhipu

import (
	"done-hub/common"
	"done-hub/common/requester"
	"done-hub/providers/claude"
	"done-hub/types"
	"fmt"
	"net/http"
)

// Claude API 相关常量
const (
	ClaudeBaseURL  = "https://open.bigmodel.cn/api/anthropic"
	ClaudeMessages = "/v1/messages"
)

// CreateClaudeChat 实现 Claude 聊天接口
func (p *ZhipuProvider) CreateClaudeChat(request *claude.ClaudeRequest) (*claude.ClaudeResponse, *types.OpenAIErrorWithStatusCode) {
	req, errWithCode := p.getClaudeRequest(request)
	if errWithCode != nil {
		return nil, errWithCode
	}
	defer req.Body.Close()

	claudeResponse := &claude.ClaudeResponse{}
	// 发送请求
	_, errWithCode = p.Requester.SendRequest(req, claudeResponse, false)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 设置用量信息
	usage := p.GetUsage()
	claude.ClaudeUsageToOpenaiUsage(&claudeResponse.Usage, usage)

	return claudeResponse, nil
}

// CreateClaudeChatStream 实现 Claude 流式聊天接口
func (p *ZhipuProvider) CreateClaudeChatStream(request *claude.ClaudeRequest) (requester.StreamReaderInterface[string], *types.OpenAIErrorWithStatusCode) {
	req, errWithCode := p.getClaudeRequest(request)
	if errWithCode != nil {
		return nil, errWithCode
	}
	defer req.Body.Close()

	// 发送请求
	resp, errWithCode := p.Requester.SendRequestRaw(req)
	if errWithCode != nil {
		return nil, errWithCode
	}

	chatHandler := &claude.ClaudeRelayStreamHandler{
		Usage:     p.Usage,
		ModelName: request.Model,
		Prefix:    `data: {"type"`,
	}

	return requester.RequestNoTrimStream(p.Requester, resp, chatHandler.HandlerStream)
}

// getClaudeRequest 构建 Claude API 请求
func (p *ZhipuProvider) getClaudeRequest(request *claude.ClaudeRequest) (*http.Request, *types.OpenAIErrorWithStatusCode) {
	// 构建完整的 Claude API URL
	fullRequestURL := fmt.Sprintf("%s%s", ClaudeBaseURL, ClaudeMessages)

	// 如果渠道配置了自定义 base_url，使用自定义的
	if p.Channel.GetBaseURL() != "" {
		fullRequestURL = fmt.Sprintf("%s%s", p.Channel.GetBaseURL(), ClaudeMessages)
	}

	// 获取请求头
	headers := p.GetRequestHeaders()

	// 创建请求
	req, err := p.Requester.NewRequest(http.MethodPost, fullRequestURL, p.Requester.WithBody(request), p.Requester.WithHeader(headers))
	if err != nil {
		return nil, common.ErrorWrapper(err, "new_request_failed", http.StatusInternalServerError)
	}

	return req, nil
}
