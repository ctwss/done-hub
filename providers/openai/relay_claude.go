package openai

import (
	"done-hub/common"
	"done-hub/common/config"
	"done-hub/common/requester"
	"done-hub/providers/claude"
	"done-hub/types"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// CreateClaudeChat 实现 Claude 聊天接口
func (p *OpenAIProvider) CreateClaudeChat(request *claude.ClaudeRequest) (*claude.ClaudeResponse, *types.OpenAIErrorWithStatusCode) {
	// 将 Claude 请求转换为 OpenAI 格式
	openaiRequest, errWithCode := p.convertClaudeToOpenAI(request)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 使用 OpenAI provider 处理请求
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
func (p *OpenAIProvider) CreateClaudeChatStream(request *claude.ClaudeRequest) (requester.StreamReaderInterface[string], *types.OpenAIErrorWithStatusCode) {
	// 将 Claude 请求转换为 OpenAI 格式
	openaiRequest, errWithCode := p.convertClaudeToOpenAI(request)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 设置流式请求
	openaiRequest.Stream = true

	// 使用 OpenAI provider 处理流式请求
	req, errWithCode := p.GetRequestTextBody(config.RelayModeChatCompletions, openaiRequest.Model, openaiRequest)
	if errWithCode != nil {
		return nil, errWithCode
	}
	defer req.Body.Close()

	resp, errWithCode := p.Requester.SendRequestRaw(req)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 创建 Claude 格式的流处理器
	chatHandler := &OpenAIClaudeStreamHandler{
		Usage:     p.Usage,
		ModelName: request.Model,
		Prefix:    `data: {"type"`,
	}

	return requester.RequestStream[string](p.Requester, resp, chatHandler.handlerStream)
}

// convertClaudeToOpenAI 将 Claude 请求转换为 OpenAI 格式
func (p *OpenAIProvider) convertClaudeToOpenAI(request *claude.ClaudeRequest) (*types.ChatCompletionRequest, *types.OpenAIErrorWithStatusCode) {
	openaiRequest := &types.ChatCompletionRequest{
		Model:       request.Model,
		Messages:    make([]types.ChatCompletionMessage, 0),
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		TopP:        request.TopP,
		Stream:      request.Stream,
	}

	// 处理停止序列
	if request.StopSequences != nil && len(request.StopSequences) > 0 {
		openaiRequest.Stop = request.StopSequences
	}

	// 处理系统消息
	if request.System != nil {
		if systemStr, ok := request.System.(string); ok && systemStr != "" {
			systemMsg := types.ChatCompletionMessage{
				Role:    types.ChatMessageRoleSystem,
				Content: systemStr,
			}
			openaiRequest.Messages = append(openaiRequest.Messages, systemMsg)
		}
	}

	// 转换消息
	for _, msg := range request.Messages {
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
										Content:    fmt.Sprintf("%v", content),
										ToolCallID: fmt.Sprintf("%v", toolCallId),
									}
									openaiRequest.Messages = append(openaiRequest.Messages, toolResult)
								}
							}
						case "image":
							// 处理图像内容 - 转换为 OpenAI 格式
							if source, exists := partMap["source"]; exists {
								if sourceMap, ok := source.(map[string]interface{}); ok {
									if data, exists := sourceMap["data"]; exists {
										if mediaType, exists := sourceMap["media_type"]; exists {
											imageContent := map[string]interface{}{
												"type": "image_url",
												"image_url": map[string]interface{}{
													"url": fmt.Sprintf("data:%s;base64,%s", mediaType, data),
												},
											}
											textParts = append(textParts, fmt.Sprintf("[Image: %v]", imageContent))
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
				// toolResults 已经直接添加到 openaiRequest.Messages 中了
			default:
				// 尝试转换为字符串
				if contentBytes, err := json.Marshal(content); err == nil {
					openaiMsg.Content = string(contentBytes)
				}
			}
		}

		openaiRequest.Messages = append(openaiRequest.Messages, openaiMsg)
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

	return openaiRequest, nil
}

// convertOpenAIToClaude 将 OpenAI 响应转换为 Claude 格式
func (p *OpenAIProvider) convertOpenAIToClaude(response *types.ChatCompletionResponse, request *claude.ClaudeRequest) *claude.ClaudeResponse {
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
					json.Unmarshal([]byte(toolCall.Function.Arguments), &input)
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

// OpenAIClaudeStreamHandler 处理 OpenAI 到 Claude 的流式响应转换
type OpenAIClaudeStreamHandler struct {
	Usage     *types.Usage
	ModelName string
	Prefix    string
}

// handlerStream 处理流式响应转换
func (h *OpenAIClaudeStreamHandler) handlerStream(rawLine *[]byte, dataChan chan string, errChan chan error) {
	// 处理 OpenAI 流式响应并转换为 Claude 格式
	if !strings.HasPrefix(string(*rawLine), "data: ") {
		*rawLine = nil
		return
	}

	*rawLine = (*rawLine)[6:]

	if strings.HasPrefix(string(*rawLine), "[DONE]") {
		errChan <- io.EOF
		*rawLine = nil
		return
	}

	// 解析 OpenAI 流式响应
	var openaiStreamResponse types.ChatCompletionStreamResponse
	if err := json.Unmarshal(*rawLine, &openaiStreamResponse); err != nil {
		errChan <- common.ErrorToOpenAIError(err)
		return
	}

	// 转换为 Claude 流式格式
	claudeStreamResponse := h.convertOpenAIStreamToClaude(&openaiStreamResponse)
	if claudeStreamResponse != nil {
		responseBody, _ := json.Marshal(claudeStreamResponse)
		dataChan <- string(responseBody)
	}
}

// convertOpenAIStreamToClaude 将 OpenAI 流式响应转换为 Claude 流式格式
func (h *OpenAIClaudeStreamHandler) convertOpenAIStreamToClaude(response *types.ChatCompletionStreamResponse) interface{} {
	if len(response.Choices) == 0 {
		return nil
	}

	choice := response.Choices[0]

	// 处理文本内容增量
	if choice.Delta.Content != "" {
		return map[string]interface{}{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]interface{}{
				"type": "text_delta",
				"text": choice.Delta.Content,
			},
		}
	}

	// 处理工具调用增量
	if len(choice.Delta.ToolCalls) > 0 {
		for _, toolCall := range choice.Delta.ToolCalls {
			if toolCall.Function.Name != "" {
				return map[string]interface{}{
					"type":  "content_block_start",
					"index": toolCall.Index,
					"content_block": map[string]interface{}{
						"type": "tool_use",
						"id":   toolCall.Id,
						"name": toolCall.Function.Name,
					},
				}
			}
			if toolCall.Function.Arguments != "" {
				return map[string]interface{}{
					"type":  "content_block_delta",
					"index": toolCall.Index,
					"delta": map[string]interface{}{
						"type":         "input_json_delta",
						"partial_json": toolCall.Function.Arguments,
					},
				}
			}
		}
	}

	// 处理结束事件
	if choice.FinishReason != nil {
		if finishReasonStr, ok := choice.FinishReason.(string); ok && finishReasonStr != "" {
			stopReason := "end_turn"
			switch finishReasonStr {
			case "stop":
				stopReason = "end_turn"
			case "length":
				stopReason = "max_tokens"
			case "tool_calls":
				stopReason = "tool_use"
			case "content_filter":
				stopReason = "stop_sequence"
			}

			return map[string]interface{}{
				"type": "message_delta",
				"delta": map[string]interface{}{
					"stop_reason": stopReason,
				},
				"usage": map[string]interface{}{
					"input_tokens":  0, // OpenAI 流式响应通常不包含用量信息
					"output_tokens": 0,
				},
			}
		}
	}

	return nil
}
