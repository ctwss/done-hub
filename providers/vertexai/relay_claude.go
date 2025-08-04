package vertexai

import (
	"done-hub/common"
	"done-hub/common/requester"
	"done-hub/providers/claude"
	commonadapter "done-hub/providers/common"
	"done-hub/providers/vertexai/category"
	"done-hub/types"
	"net/http"
	"strings"
)

// CreateClaudeChat 实现 Claude 聊天接口
func (p *VertexAIProvider) CreateClaudeChat(request *claude.ClaudeRequest) (*claude.ClaudeResponse, *types.OpenAIErrorWithStatusCode) {
	// 输入验证
	if request == nil {
		return nil, common.StringErrorWrapper("request cannot be nil", "invalid_request", http.StatusBadRequest)
	}

	if len(request.Messages) == 0 {
		return nil, common.StringErrorWrapper("messages cannot be empty", "invalid_request", http.StatusBadRequest)
	}

	if request.MaxTokens <= 0 {
		return nil, common.StringErrorWrapper("max_tokens must be positive", "invalid_request", http.StatusBadRequest)
	}

	// 检查是否是 Claude 模型
	if !p.isClaudeModel(request.Model) {
		return nil, common.StringErrorWrapper("model not supported for Claude API", "invalid_model", http.StatusBadRequest)
	}

	// 设置 Claude 分类
	p.Category = category.CategoryMap["claude"]
	if p.Category == nil {
		return nil, common.StringErrorWrapper("Claude category not found", "internal_error", http.StatusInternalServerError)
	}

	// 使用统一的转换器将 Claude 请求转换为 OpenAI 格式
	converter := commonadapter.NewClaudeConverter()
	openaiRequest, errWithCode := converter.ConvertClaudeToOpenAI(request)
	if errWithCode != nil {
		return nil, errWithCode
	}
	// 使用 VertexAI 处理请求
	openaiResponse, errWithCode := p.CreateChatCompletion(openaiRequest)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 将 OpenAI 响应转换为 Claude 格式
	claudeResponse := converter.ConvertOpenAIToClaude(openaiResponse)

	// 设置用量信息
	usage := p.GetUsage()
	if openaiResponse.Usage != nil {
		claude.ClaudeUsageToOpenaiUsage(&claudeResponse.Usage, usage)
	}

	return claudeResponse, nil
}

// CreateClaudeChatStream 实现 Claude 流式聊天接口
func (p *VertexAIProvider) CreateClaudeChatStream(request *claude.ClaudeRequest) (requester.StreamReaderInterface[string], *types.OpenAIErrorWithStatusCode) {
	// 输入验证
	if request == nil {
		return nil, common.StringErrorWrapper("request cannot be nil", "invalid_request", http.StatusBadRequest)
	}

	if len(request.Messages) == 0 {
		return nil, common.StringErrorWrapper("messages cannot be empty", "invalid_request", http.StatusBadRequest)
	}

	if request.MaxTokens <= 0 {
		return nil, common.StringErrorWrapper("max_tokens must be positive", "invalid_request", http.StatusBadRequest)
	}

	// 检查是否是 Claude 模型
	if !p.isClaudeModel(request.Model) {
		return nil, common.StringErrorWrapper("model not supported for Claude API", "invalid_model", http.StatusBadRequest)
	}

	// 设置 Claude 分类
	p.Category = category.CategoryMap["claude"]
	if p.Category == nil {
		return nil, common.StringErrorWrapper("Claude category not found", "internal_error", http.StatusInternalServerError)
	}

	// 使用统一的转换器将 Claude 请求转换为 OpenAI 格式
	converter := commonadapter.NewClaudeConverter()
	openaiRequest, errWithCode := converter.ConvertClaudeToOpenAI(request)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 强制设置为流式
	openaiRequest.Stream = true

	// 使用 VertexAI 处理流式请求
	return p.CreateChatCompletionStream(openaiRequest)
}

// isClaudeModel 检查是否是 Claude 模型
func (p *VertexAIProvider) isClaudeModel(model string) bool {
	claudeModels := []string{
		"claude-3-5-sonnet-20240620",
		"claude-3-5-sonnet-20241022",
		"claude-3-opus-20240229",
		"claude-3-sonnet-20240229",
		"claude-3-haiku-20240307",
		"claude-3-5-haiku-20241022",
		"claude-3-7-sonnet-20250219",
		"claude-sonnet-4-20250514",
		"claude-opus-4-20250514",
	}

	for _, claudeModel := range claudeModels {
		if strings.Contains(model, claudeModel) || strings.Contains(claudeModel, model) {
			return true
		}
	}

	return false
}




