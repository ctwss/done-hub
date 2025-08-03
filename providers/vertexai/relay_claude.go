package vertexai

import (
	"done-hub/common"
	"done-hub/common/logger"
	"done-hub/common/requester"
	"done-hub/providers/claude"
	"done-hub/providers/vertexai/category"
	"done-hub/types"
	"encoding/json"
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

	// 将 Claude 请求转换为 OpenAI 格式
	openaiRequest := &types.ChatCompletionRequest{
		Model:       request.Model,
		Messages:    p.convertClaudeMessagesToOpenAI(request.Messages),
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		TopP:        request.TopP,
		Stream:      request.Stream,
	}

	// 处理系统消息
	if request.System != nil {
		if systemStr, ok := request.System.(string); ok && systemStr != "" {
			systemMsg := types.ChatCompletionMessage{
				Role:    types.ChatMessageRoleSystem,
				Content: systemStr,
			}
			openaiRequest.Messages = append([]types.ChatCompletionMessage{systemMsg}, openaiRequest.Messages...)
		}
	}

	// 处理停止序列
	if request.StopSequences != nil && len(request.StopSequences) > 0 {
		openaiRequest.Stop = request.StopSequences
	}

	// 处理工具定义
	if len(request.Tools) > 0 {
		tools := make([]*types.ChatCompletionTool, 0)
		for _, tool := range request.Tools {
			openaiTool := &types.ChatCompletionTool{
				Type: "function",
				Function: types.ChatCompletionFunction{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.InputSchema,
				},
			}
			tools = append(tools, openaiTool)
		}
		openaiRequest.Tools = tools

		// 处理工具选择
		if request.ToolChoice != nil {
			switch request.ToolChoice.Type {
			case "auto":
				openaiRequest.ToolChoice = "auto"
			case "any":
				openaiRequest.ToolChoice = "required"
			case "tool":
				openaiRequest.ToolChoice = map[string]interface{}{
					"type": "function",
					"function": map[string]interface{}{
						"name": request.ToolChoice.Name,
					},
				}
			}
		}
	}

	// 使用 VertexAI 处理请求
	openaiResponse, errWithCode := p.CreateChatCompletion(openaiRequest)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 将 OpenAI 响应转换为 Claude 格式
	claudeResponse := p.convertOpenAIToClaude(openaiResponse, request)

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

	// 将 Claude 请求转换为 OpenAI 格式
	openaiRequest := &types.ChatCompletionRequest{
		Model:       request.Model,
		Messages:    p.convertClaudeMessagesToOpenAI(request.Messages),
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		TopP:        request.TopP,
		Stream:      true, // 强制设置为流式
	}

	// 处理系统消息
	if request.System != nil {
		if systemStr, ok := request.System.(string); ok && systemStr != "" {
			systemMsg := types.ChatCompletionMessage{
				Role:    types.ChatMessageRoleSystem,
				Content: systemStr,
			}
			openaiRequest.Messages = append([]types.ChatCompletionMessage{systemMsg}, openaiRequest.Messages...)
		}
	}

	// 处理停止序列
	if request.StopSequences != nil && len(request.StopSequences) > 0 {
		openaiRequest.Stop = request.StopSequences
	}

	// 处理工具定义
	if len(request.Tools) > 0 {
		tools := make([]*types.ChatCompletionTool, 0)
		for _, tool := range request.Tools {
			openaiTool := &types.ChatCompletionTool{
				Type: "function",
				Function: types.ChatCompletionFunction{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.InputSchema,
				},
			}
			tools = append(tools, openaiTool)
		}
		openaiRequest.Tools = tools
	}

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

// convertClaudeMessagesToOpenAI 将 Claude 消息转换为 OpenAI 格式
func (p *VertexAIProvider) convertClaudeMessagesToOpenAI(messages []claude.Message) []types.ChatCompletionMessage {
	openaiMessages := make([]types.ChatCompletionMessage, 0, len(messages))

	for _, msg := range messages {
		openaiMsg := types.ChatCompletionMessage{
			Role: msg.Role,
		}

		// 处理消息内容
		if msg.Content != nil {
			switch content := msg.Content.(type) {
			case string:
				openaiMsg.Content = content
			case []interface{}:
				// 处理多模态内容
				var textParts []string

				for _, part := range content {
					if partMap, ok := part.(map[string]interface{}); ok {
						partType, _ := partMap["type"].(string)

						switch partType {
						case "text":
							if text, exists := partMap["text"]; exists {
								if textStr, ok := text.(string); ok {
									textParts = append(textParts, textStr)
								}
							}
						case "tool_result":
							// 处理工具结果
							if toolCallId, exists := partMap["tool_use_id"]; exists {
								if content, exists := partMap["content"]; exists {
									toolResult := types.ChatCompletionMessage{
										Role:       types.ChatMessageRoleTool,
										Content:    content.(string),
										ToolCallID: toolCallId.(string),
									}
									openaiMessages = append(openaiMessages, toolResult)
								}
							}
						case "image":
							// 处理图像内容
							if source, exists := partMap["source"]; exists {
								if sourceMap, ok := source.(map[string]interface{}); ok {
									if _, exists := sourceMap["data"]; exists {
										if _, exists := sourceMap["media_type"]; exists {
											logger.SysLog("VertexAI Claude: Image content detected but not fully supported in conversion")
											textParts = append(textParts, "[Image content]")
										}
									}
								}
							}
						}
					}
				}

				if len(textParts) > 0 {
					openaiMsg.Content = strings.Join(textParts, "\n")
				}
			}
		}

		openaiMessages = append(openaiMessages, openaiMsg)
	}

	return openaiMessages
}

// convertOpenAIToClaude 将 OpenAI 响应转换为 Claude 格式
func (p *VertexAIProvider) convertOpenAIToClaude(response *types.ChatCompletionResponse, request *claude.ClaudeRequest) *claude.ClaudeResponse {
	content := make([]claude.ResContent, 0)

	if len(response.Choices) > 0 {
		choice := response.Choices[0]

		// 处理文本内容
		if choice.Message.Content != nil {
			if contentStr, ok := choice.Message.Content.(string); ok && contentStr != "" {
				content = append(content, claude.ResContent{
					Type: "text",
					Text: contentStr,
				})
			}
		}

		// 处理工具调用
		if len(choice.Message.ToolCalls) > 0 {
			for _, toolCall := range choice.Message.ToolCalls {
				var input interface{}
				if toolCall.Function.Arguments != "" {
					if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &input); err != nil {
						logger.SysError("Failed to unmarshal tool arguments: " + err.Error())
						input = map[string]interface{}{}
					}
				} else {
					input = map[string]interface{}{}
				}

				content = append(content, claude.ResContent{
					Type:  "tool_use",
					Id:    toolCall.Id,
					Name:  toolCall.Function.Name,
					Input: input,
				})
			}
		}
	}

	// 转换停止原因
	stopReason := "end_turn"
	if len(response.Choices) > 0 {
		switch response.Choices[0].FinishReason {
		case "stop":
			stopReason = "end_turn"
		case "length":
			stopReason = "max_tokens"
		case "tool_calls":
			stopReason = "tool_use"
		case "content_filter":
			stopReason = "stop_sequence"
		}
	}

	claudeResponse := &claude.ClaudeResponse{
		Id:         response.ID,
		Type:       "message",
		Role:       "assistant",
		Content:    content,
		Model:      response.Model,
		StopReason: stopReason,
	}

	// 设置用量信息
	if response.Usage != nil {
		claudeResponse.Usage = claude.Usage{
			InputTokens:  response.Usage.PromptTokens,
			OutputTokens: response.Usage.CompletionTokens,
		}
	}

	return claudeResponse
}
