package common

import (
	"done-hub/common"
	"done-hub/common/logger"
	"done-hub/providers/claude"
	"done-hub/types"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ClaudeConverter 提供Claude和OpenAI格式之间的转换功能
type ClaudeConverter struct{}

// NewClaudeConverter 创建新的转换器实例
func NewClaudeConverter() *ClaudeConverter {
	return &ClaudeConverter{}
}

// ConvertClaudeToOpenAI 将Claude请求转换为OpenAI格式
func (c *ClaudeConverter) ConvertClaudeToOpenAI(claudeRequest *claude.ClaudeRequest) (*types.ChatCompletionRequest, *types.OpenAIErrorWithStatusCode) {
	// 输入验证
	if claudeRequest == nil {
		return nil, common.StringErrorWrapper("request cannot be nil", "invalid_request", http.StatusBadRequest)
	}

	if len(claudeRequest.Messages) == 0 {
		return nil, common.StringErrorWrapper("messages cannot be empty", "invalid_request", http.StatusBadRequest)
	}

	if claudeRequest.MaxTokens <= 0 {
		return nil, common.StringErrorWrapper("max_tokens must be positive", "invalid_request", http.StatusBadRequest)
	}

	openaiRequest := &types.ChatCompletionRequest{
		Model:       claudeRequest.Model,
		Messages:    make([]types.ChatCompletionMessage, 0),
		MaxTokens:   claudeRequest.MaxTokens,
		Temperature: claudeRequest.Temperature,
		TopP:        claudeRequest.TopP,
		Stream:      claudeRequest.Stream,
	}

	// 处理 Stop 参数，过滤掉 null 值
	if claudeRequest.StopSequences != nil {
		openaiRequest.Stop = claudeRequest.StopSequences
	}

	// 处理系统消息
	if claudeRequest.System != nil {
		switch sys := claudeRequest.System.(type) {
		case string:
			openaiRequest.Messages = append(openaiRequest.Messages, types.ChatCompletionMessage{
				Role:    types.ChatMessageRoleSystem,
				Content: sys,
			})
		case []interface{}:
			// 处理数组形式的系统消息 - 每个文本部分创建单独的系统消息
			for _, item := range sys {
				if itemMap, ok := item.(map[string]interface{}); ok {
					if itemType, exists := itemMap["type"]; exists && itemType == "text" {
						if text, textExists := itemMap["text"]; textExists {
							if textStr, ok := text.(string); ok && textStr != "" {
								openaiRequest.Messages = append(openaiRequest.Messages, types.ChatCompletionMessage{
									Role:    types.ChatMessageRoleSystem,
									Content: textStr,
								})
							}
						}
					}
				}
			}
		}
	}

	// 转换消息
	for _, msg := range claudeRequest.Messages {
		openaiMsg := types.ChatCompletionMessage{
			Role: msg.Role,
		}

		// 处理消息内容
		switch content := msg.Content.(type) {
		case string:
			openaiMsg.Content = content
			openaiRequest.Messages = append(openaiRequest.Messages, openaiMsg)
		case []interface{}:
			// 处理复杂内容
			if msg.Role == "user" {
				// 用户消息：先处理 tool_result，再处理 text
				toolParts := make([]map[string]interface{}, 0)
				textParts := make([]map[string]interface{}, 0)

				for _, part := range content {
					if partMap, ok := part.(map[string]interface{}); ok {
						partType, _ := partMap["type"].(string)

						switch partType {
						case "tool_result":
							if _, exists := partMap["tool_use_id"].(string); exists {
								toolParts = append(toolParts, partMap)
							}
						case "text":
							if _, exists := partMap["text"].(string); exists {
								textParts = append(textParts, partMap)
							}
						}
					}
				}

				// 处理 tool_result 部分
				for _, tool := range toolParts {
					toolContent := ""
					if resultContent, exists := tool["content"]; exists {
						if contentStr, ok := resultContent.(string); ok {
							toolContent = contentStr
						} else {
							contentBytes, _ := json.Marshal(resultContent)
							toolContent = string(contentBytes)
						}
					}

					toolCallID := ""
					if id, ok := tool["tool_use_id"].(string); ok {
						toolCallID = id
					}

					toolResultMsg := types.ChatCompletionMessage{
						Role:       types.ChatMessageRoleTool,
						Content:    toolContent,
						ToolCallID: toolCallID,
					}
					openaiRequest.Messages = append(openaiRequest.Messages, toolResultMsg)
				}

				// 处理 text 部分 - 用户消息的 textParts 直接作为 content
				if len(textParts) > 0 {
					contentParts := make([]types.ChatMessagePart, 0)
					for _, textPart := range textParts {
						contentParts = append(contentParts, types.ChatMessagePart{
							Type: "text",
							Text: textPart["text"].(string),
						})
					}

					userMsg := types.ChatCompletionMessage{
						Role:    types.ChatMessageRoleUser,
						Content: contentParts,
					}
					openaiRequest.Messages = append(openaiRequest.Messages, userMsg)
				}

			} else if msg.Role == "assistant" {
				// 助手消息：分别处理 text 和 tool_use
				textParts := make([]map[string]interface{}, 0)
				toolCallParts := make([]map[string]interface{}, 0)

				for _, part := range content {
					if partMap, ok := part.(map[string]interface{}); ok {
						partType, _ := partMap["type"].(string)

						switch partType {
						case "text":
							if _, exists := partMap["text"].(string); exists {
								textParts = append(textParts, partMap)
							}
						case "tool_use":
							if _, exists := partMap["id"].(string); exists {
								toolCallParts = append(toolCallParts, partMap)
							}
						}
					}
				}

				// 处理 text 部分 - 每个文本部分创建单独的助手消息
				for _, textPart := range textParts {
					assistantMsg := types.ChatCompletionMessage{
						Role:    types.ChatMessageRoleAssistant,
						Content: textPart["text"].(string),
					}
					openaiRequest.Messages = append(openaiRequest.Messages, assistantMsg)
				}

				// 处理 tool_use 部分 - 创建单独的助手消息，content 为 null
				if len(toolCallParts) > 0 {
					toolCalls := make([]*types.ChatCompletionToolCalls, 0)
					for _, toolPart := range toolCallParts {
						// 安全地获取工具调用信息
						var toolId, toolName string
						var input interface{}

						if id, exists := toolPart["id"]; exists && id != nil {
							if idStr, ok := id.(string); ok && idStr != "" {
								toolId = idStr
							}
						}
						if toolId == "" {
							toolId = fmt.Sprintf("call_%d", time.Now().UnixNano())
						}

						if name, exists := toolPart["name"]; exists && name != nil {
							if nameStr, ok := name.(string); ok && nameStr != "" {
								toolName = nameStr
							}
						}
						if toolName == "" {
							logger.SysLog(fmt.Sprintf("[Claude Convert] 跳过工具调用，name 为空: %+v", toolPart))
							continue // 跳过没有名称的工具调用
						}

						if inputData, exists := toolPart["input"]; exists {
							input = inputData
						} else {
							input = map[string]interface{}{}
						}

						inputBytes, _ := json.Marshal(input)

						toolCall := &types.ChatCompletionToolCalls{
							Id:   toolId,
							Type: types.ChatMessageRoleFunction,
							Function: &types.ChatCompletionToolCallsFunction{
								Name:      toolName,
								Arguments: string(inputBytes),
							},
						}
						toolCalls = append(toolCalls, toolCall)
					}

					assistantMsg := types.ChatCompletionMessage{
						Role:      types.ChatMessageRoleAssistant,
						Content:   nil,
						ToolCalls: toolCalls,
					}
					openaiRequest.Messages = append(openaiRequest.Messages, assistantMsg)
				}
			}
			continue // 跳过默认的 append
		default:
			openaiRequest.Messages = append(openaiRequest.Messages, openaiMsg)
		}
	}

	// 处理工具定义
	if len(claudeRequest.Tools) > 0 {
		tools := make([]*types.ChatCompletionTool, 0)
		// 转换为 OpenAI 格式
		for _, tool := range claudeRequest.Tools {
			// input_schema → parameters
			openaiTool := &types.ChatCompletionTool{
				Type: "function",
				Function: types.ChatCompletionFunction{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.InputSchema, // Claude的input_schema → OpenAI的parameters
				},
			}
			tools = append(tools, openaiTool)
		}
		openaiRequest.Tools = tools

		// 处理工具选择
		if claudeRequest.ToolChoice != nil {
			openaiRequest.ToolChoice = claudeRequest.ToolChoice
		}
	}

	return openaiRequest, nil
}

// ConvertOpenAIToClaude 将OpenAI响应转换为Claude格式
func (c *ClaudeConverter) ConvertOpenAIToClaude(openaiResponse *types.ChatCompletionResponse) *claude.ClaudeResponse {
	if openaiResponse == nil || len(openaiResponse.Choices) == 0 {
		return &claude.ClaudeResponse{
			Id:      "msg_" + openaiResponse.ID,
			Type:    "message",
			Role:    "assistant",
			Content: []claude.ResContent{},
			Model:   openaiResponse.Model,
		}
	}

	choice := openaiResponse.Choices[0]
	content := make([]claude.ResContent, 0)

	// 处理文本内容
	// 检查是否达到 max_tokens 限制
	if choice.FinishReason == "length" && (choice.Message.Content == nil || choice.Message.Content == "") {
		// 当达到 max_tokens 限制且内容为空时，添加一个默认消息
		content = append(content, claude.ResContent{
			Type: "text",
			Text: "[Response truncated due to token limit]",
		})
	} else {
		// 正常处理内容
		switch contentValue := choice.Message.Content.(type) {
		case string:
			if contentValue != "" {
				content = append(content, claude.ResContent{
					Type: "text",
					Text: contentValue,
				})
			}
		case []interface{}:
			// 处理复杂内容格式
			for _, part := range contentValue {
				if partMap, ok := part.(map[string]interface{}); ok {
					if partType, exists := partMap["type"].(string); exists && partType == "text" {
						if text, textExists := partMap["text"].(string); textExists && text != "" {
							content = append(content, claude.ResContent{
								Type: "text",
								Text: text,
							})
						}
					}
				}
			}
		case nil:
			// 内容为空，不添加任何内容
		default:
			// 尝试转换为字符串
			if str := fmt.Sprintf("%v", contentValue); str != "" && str != "<nil>" {
				content = append(content, claude.ResContent{
					Type: "text",
					Text: str,
				})
			}
		}
	}

	// 处理工具调用
	var toolCallTokens int
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

			// 计算工具调用的 tokens
			toolCallText := fmt.Sprintf("tool_use:%s:%s", toolCall.Function.Name, toolCall.Function.Arguments)
			toolCallTokens += common.CountTokenText(toolCallText, openaiResponse.Model)
		}
	}

	// 转换停止原因
	stopReason := ""
	switch choice.FinishReason {
	case "stop":
		stopReason = "end_turn"
	case "length":
		stopReason = "max_tokens"
	case "tool_calls":
		stopReason = "tool_use"
	case "content_filter":
		stopReason = "stop_sequence"
	default:
		stopReason = "end_turn"
	}

	claudeResponse := &claude.ClaudeResponse{
		Id:           "msg_" + openaiResponse.ID,
		Type:         "message",
		Role:         "assistant",
		Content:      content,
		Model:        openaiResponse.Model,
		StopReason:   stopReason,
		StopSequence: "", // 添加缺失的字段
	}

	// 处理使用量信息
	if openaiResponse.Usage != nil {
		// 计算最终的输出 tokens
		finalOutputTokens := openaiResponse.Usage.CompletionTokens

		if finalOutputTokens == 0 {
			// 如果 OpenAI 返回的 completion_tokens 为 0，计算工具调用和文本内容的 tokens
			finalOutputTokens = toolCallTokens

			// 累加文本内容的 tokens
			if len(content) > 0 {
				var textContent strings.Builder
				for _, c := range content {
					if c.Type == "text" && c.Text != "" {
						textContent.WriteString(c.Text)
					}
				}
				if textContent.Len() > 0 {
					textTokens := common.CountTokenText(textContent.String(), openaiResponse.Model)
					finalOutputTokens += textTokens
				}
			}
		}

		claudeResponse.Usage = claude.Usage{
			InputTokens:  openaiResponse.Usage.PromptTokens,
			OutputTokens: finalOutputTokens,
		}
	}

	return claudeResponse
}

// ConvertOpenAIStreamToClaude 将OpenAI流式响应转换为Claude流式格式
func (c *ClaudeConverter) ConvertOpenAIStreamToClaude(openaiStreamResponse *types.ChatCompletionStreamResponse) interface{} {
	if len(openaiStreamResponse.Choices) == 0 {
		return nil
	}

	choice := openaiStreamResponse.Choices[0]

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
			if toolCall.Function != nil && toolCall.Function.Name != "" {
				// 工具调用开始
				return map[string]interface{}{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]interface{}{
						"type":  "tool_use",
						"id":    toolCall.Id,
						"name":  toolCall.Function.Name,
						"input": map[string]interface{}{},
					},
				}
			}
			if toolCall.Function != nil && toolCall.Function.Arguments != "" {
				// 工具调用参数增量
				return map[string]interface{}{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]interface{}{
						"type":        "input_json_delta",
						"partial_json": toolCall.Function.Arguments,
					},
				}
			}
		}
	}

	// 处理结束标记
	if choice.FinishReason != "" {
		stopReason := "end_turn"
		switch choice.FinishReason {
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
		}
	}

	return nil
}
