package ali

import (
	"done-hub/common/logger"
	"done-hub/common/requester"
	"done-hub/providers/claude"
	commonadapter "done-hub/providers/common"
	"done-hub/types"
	"fmt"
)

// shouldUseOpenAIMode 检查是否应该使用OpenAI兼容模式
func (p *AliProvider) shouldUseOpenAIMode(hasTools bool) bool {
	if p.UseOpenaiAPI || hasTools {
		if hasTools && !p.UseOpenaiAPI {
			logger.SysLog("[Ali Claude] Tool calls detected, switching to OpenAI compatible mode for this request")
		}
		return true
	}
	return false
}

// CreateClaudeChat 实现 Claude 聊天接口
func (p *AliProvider) CreateClaudeChat(request *claude.ClaudeRequest) (*claude.ClaudeResponse, *types.OpenAIErrorWithStatusCode) {
	hasTools := len(request.Tools) > 0

	if p.shouldUseOpenAIMode(hasTools) {
		// OpenAI兼容模式：使用嵌入的OpenAIProvider的Claude接口实现
		return p.OpenAIProvider.CreateClaudeChat(request)
	}

	// 原生模式：使用阿里云原生格式（仅支持普通对话）
	converter := commonadapter.NewClaudeConverter()
	openaiRequest, errWithCode := converter.ConvertClaudeToOpenAI(request)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 确保非流式请求
	openaiRequest.Stream = false

	// 使用阿里云原生的CreateChatCompletion方法
	openaiResponse, errWithCode := p.CreateChatCompletion(openaiRequest)
	if errWithCode != nil {
		logger.SysError(fmt.Sprintf("[Ali Claude] CreateChatCompletion failed: %v", errWithCode))
		return nil, errWithCode
	}

	// 将OpenAI响应转换为Claude格式
	claudeResponse := converter.ConvertOpenAIToClaude(openaiResponse)

	// 设置用量信息
	usage := p.GetUsage()
	if openaiResponse.Usage != nil {
		claude.ClaudeUsageToOpenaiUsage(&claudeResponse.Usage, usage)
	}

	return claudeResponse, nil
}

// CreateClaudeChatStream 实现 Claude 流式聊天接口
func (p *AliProvider) CreateClaudeChatStream(request *claude.ClaudeRequest) (requester.StreamReaderInterface[string], *types.OpenAIErrorWithStatusCode) {
	hasTools := len(request.Tools) > 0

	if p.shouldUseOpenAIMode(hasTools) {
		// OpenAI兼容模式：使用嵌入的OpenAIProvider的Claude接口实现
		return p.OpenAIProvider.CreateClaudeChatStream(request)
	}

	// 原生模式：使用阿里云原生格式（仅支持普通对话）
	converter := commonadapter.NewClaudeConverter()
	openaiRequest, errWithCode := converter.ConvertClaudeToOpenAI(request)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 设置流式请求
	openaiRequest.Stream = true

	// 使用阿里云原生的CreateChatCompletionStream方法
	streamReader, errWithCode := p.CreateChatCompletionStream(openaiRequest)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 创建Claude格式的流处理器包装器
	claudeStreamWrapper := &commonadapter.ClaudeStreamWrapper{
		OriginalStream: streamReader,
		Usage:          p.GetUsage(),
		ModelName:      request.Model,
	}

	// 设置转换器
	claudeStreamWrapper.SetConverter(converter)

	return claudeStreamWrapper, nil
}
