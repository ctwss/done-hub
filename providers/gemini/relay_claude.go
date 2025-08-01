package gemini

import (
	"done-hub/common"
	"done-hub/common/requester"
	"done-hub/providers/claude"
	"done-hub/types"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// CreateClaudeChat 实现 Claude 聊天接口
func (p *GeminiProvider) CreateClaudeChat(request *claude.ClaudeRequest) (*claude.ClaudeResponse, *types.OpenAIErrorWithStatusCode) {
	// 如果使用 OpenAI API，直接转换为 OpenAI 格式处理
	if p.UseOpenaiAPI {
		return p.createClaudeChatWithOpenAI(request)
	}

	// 将 Claude 请求转换为 Gemini 格式
	geminiRequest, errWithCode := p.convertClaudeToGemini(request)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 发送 Gemini 请求
	req, errWithCode := p.getChatRequest(geminiRequest, false)
	if errWithCode != nil {
		return nil, errWithCode
	}
	defer req.Body.Close()

	geminiResponse := &GeminiChatResponse{}
	_, errWithCode = p.Requester.SendRequest(req, geminiResponse, false)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 将 Gemini 响应转换为 Claude 格式
	claudeResponse, errWithCode := p.convertGeminiToClaude(geminiResponse, request)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 设置用量信息
	usage := p.GetUsage()
	*usage = ConvertOpenAIUsage(geminiResponse.UsageMetadata)
	claude.ClaudeUsageToOpenaiUsage(&claudeResponse.Usage, usage)

	return claudeResponse, nil
}

// CreateClaudeChatStream 实现 Claude 流式聊天接口
func (p *GeminiProvider) CreateClaudeChatStream(request *claude.ClaudeRequest) (requester.StreamReaderInterface[string], *types.OpenAIErrorWithStatusCode) {
	// 如果使用 OpenAI API，直接转换为 OpenAI 格式处理
	if p.UseOpenaiAPI {
		return p.createClaudeChatStreamWithOpenAI(request)
	}

	// 将 Claude 请求转换为 Gemini 格式
	geminiRequest, errWithCode := p.convertClaudeToGemini(request)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 设置流式请求
	geminiRequest.Stream = true

	// 发送 Gemini 流式请求
	req, errWithCode := p.getChatRequest(geminiRequest, false)
	if errWithCode != nil {
		return nil, errWithCode
	}
	defer req.Body.Close()

	resp, errWithCode := p.Requester.SendRequestRaw(req)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 创建 Claude 格式的流处理器
	chatHandler := &GeminiClaudeStreamHandler{
		Usage:     p.Usage,
		ModelName: request.Model,
		Prefix:    `data: {"type"`,
	}

	return requester.RequestStream[string](p.Requester, resp, chatHandler.handlerStream)
}

// createClaudeChatWithOpenAI 使用 OpenAI API 处理 Claude 请求
func (p *GeminiProvider) createClaudeChatWithOpenAI(request *claude.ClaudeRequest) (*claude.ClaudeResponse, *types.OpenAIErrorWithStatusCode) {
	// 将 Claude 请求转换为 OpenAI 格式（这里不需要使用 claude.ConvertFromChatOpenai）

	// 使用 OpenAI provider 处理
	openaiResponse, errWithCode := p.OpenAIProvider.CreateChatCompletion(&types.ChatCompletionRequest{
		Model:       request.Model,
		Messages:    convertClaudeMessagesToOpenAI(request.Messages),
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		TopP:        request.TopP,
		Stream:      false,
	})
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 将 OpenAI 响应转换为 Claude 格式
	return convertOpenAIToClaude(openaiResponse, request), nil
}

// createClaudeChatStreamWithOpenAI 使用 OpenAI API 处理 Claude 流式请求
func (p *GeminiProvider) createClaudeChatStreamWithOpenAI(request *claude.ClaudeRequest) (requester.StreamReaderInterface[string], *types.OpenAIErrorWithStatusCode) {
	// 使用 OpenAI provider 处理流式请求
	return p.OpenAIProvider.CreateChatCompletionStream(&types.ChatCompletionRequest{
		Model:       request.Model,
		Messages:    convertClaudeMessagesToOpenAI(request.Messages),
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		TopP:        request.TopP,
		Stream:      true,
	})
}

// convertClaudeToGemini 将 Claude 请求转换为 Gemini 格式
func (p *GeminiProvider) convertClaudeToGemini(request *claude.ClaudeRequest) (*GeminiChatRequest, *types.OpenAIErrorWithStatusCode) {
	// 先转换为 OpenAI 格式，再转换为 Gemini 格式
	openaiRequest := &types.ChatCompletionRequest{
		Model:       request.Model,
		Messages:    convertClaudeMessagesToOpenAI(request.Messages),
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

	// 使用现有的转换函数
	return ConvertFromChatOpenai(openaiRequest)
}

// convertGeminiToClaude 将 Gemini 响应转换为 Claude 格式
func (p *GeminiProvider) convertGeminiToClaude(response *GeminiChatResponse, request *claude.ClaudeRequest) (*claude.ClaudeResponse, *types.OpenAIErrorWithStatusCode) {
	if len(response.Candidates) == 0 {
		return nil, common.StringErrorWrapper("no candidates", "no_candidates", http.StatusInternalServerError)
	}

	candidate := response.Candidates[0]
	content := make([]claude.ResContent, 0)

	// 处理文本内容
	for _, part := range candidate.Content.Parts {
		if part.Text != "" {
			content = append(content, claude.ResContent{
				Type: "text",
				Text: part.Text,
			})
		}
		// 处理函数调用
		if part.FunctionCall != nil {
			content = append(content, claude.ResContent{
				Type:  "tool_use",
				Id:    fmt.Sprintf("call_%s", part.FunctionCall.Name),
				Name:  part.FunctionCall.Name,
				Input: part.FunctionCall.Args,
			})
		}
	}

	// 转换停止原因
	stopReason := "end_turn"
	if candidate.FinishReason != nil {
		switch *candidate.FinishReason {
		case "STOP":
			stopReason = "end_turn"
		case "MAX_TOKENS":
			stopReason = "max_tokens"
		case "SAFETY":
			stopReason = "stop_sequence"
		}
	}

	claudeResponse := &claude.ClaudeResponse{
		Id:         response.ResponseId,
		Type:       "message",
		Role:       "assistant",
		Content:    content,
		Model:      request.Model,
		StopReason: stopReason,
		Usage: claude.Usage{
			InputTokens:  response.UsageMetadata.PromptTokenCount,
			OutputTokens: response.UsageMetadata.CandidatesTokenCount,
		},
	}

	return claudeResponse, nil
}

// convertClaudeMessagesToOpenAI 将 Claude 消息转换为 OpenAI 格式
func convertClaudeMessagesToOpenAI(messages []claude.Message) []types.ChatCompletionMessage {
	openaiMessages := make([]types.ChatCompletionMessage, 0, len(messages))

	for _, msg := range messages {
		openaiMsg := types.ChatCompletionMessage{
			Role: msg.Role,
		}

		// 处理内容
		if msg.Content != nil {
			switch content := msg.Content.(type) {
			case string:
				openaiMsg.Content = content
			case []interface{}:
				// 处理多模态内容
				var textParts []string
				for _, part := range content {
					if partMap, ok := part.(map[string]interface{}); ok {
						if partType, exists := partMap["type"]; exists && partType == "text" {
							if text, exists := partMap["text"]; exists {
								if textStr, ok := text.(string); ok {
									textParts = append(textParts, textStr)
								}
							}
						}
					}
				}
				openaiMsg.Content = strings.Join(textParts, "\n")
			default:
				// 尝试转换为字符串
				if contentBytes, err := json.Marshal(content); err == nil {
					openaiMsg.Content = string(contentBytes)
				}
			}
		}

		openaiMessages = append(openaiMessages, openaiMsg)
	}

	return openaiMessages
}

// convertOpenAIToClaude 将 OpenAI 响应转换为 Claude 格式
func convertOpenAIToClaude(response *types.ChatCompletionResponse, request *claude.ClaudeRequest) *claude.ClaudeResponse {
	content := make([]claude.ResContent, 0)

	if len(response.Choices) > 0 {
		choice := response.Choices[0]
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

	if response.Usage != nil {
		claudeResponse.Usage = claude.Usage{
			InputTokens:  response.Usage.PromptTokens,
			OutputTokens: response.Usage.CompletionTokens,
		}
	}

	return claudeResponse
}

// GeminiClaudeStreamHandler 处理 Gemini 到 Claude 的流式响应转换
type GeminiClaudeStreamHandler struct {
	Usage     *types.Usage
	ModelName string
	Prefix    string
}

// handlerStream 处理流式响应转换
func (h *GeminiClaudeStreamHandler) handlerStream(rawLine *[]byte, dataChan chan string, errChan chan error) {
	// 处理 Gemini 流式响应并转换为 Claude 格式
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

	// 解析 Gemini 流式响应
	var geminiStreamResponse GeminiChatResponse
	if err := json.Unmarshal(*rawLine, &geminiStreamResponse); err != nil {
		errChan <- common.ErrorToOpenAIError(err)
		return
	}

	// 转换为 Claude 流式格式
	claudeStreamResponse := h.convertGeminiStreamToClaude(&geminiStreamResponse)
	if claudeStreamResponse != nil {
		responseBody, _ := json.Marshal(claudeStreamResponse)
		dataChan <- string(responseBody)
	}
}

// convertGeminiStreamToClaude 将 Gemini 流式响应转换为 Claude 流式格式
func (h *GeminiClaudeStreamHandler) convertGeminiStreamToClaude(response *GeminiChatResponse) interface{} {
	if len(response.Candidates) == 0 {
		return nil
	}

	candidate := response.Candidates[0]

	// 处理文本内容
	for _, part := range candidate.Content.Parts {
		if part.Text != "" {
			return map[string]interface{}{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]interface{}{
					"type": "text_delta",
					"text": part.Text,
				},
			}
		}
	}

	// 处理结束事件
	if candidate.FinishReason != nil {
		stopReason := "end_turn"
		switch *candidate.FinishReason {
		case "STOP":
			stopReason = "end_turn"
		case "MAX_TOKENS":
			stopReason = "max_tokens"
		case "SAFETY":
			stopReason = "stop_sequence"
		}

		return map[string]interface{}{
			"type": "message_delta",
			"delta": map[string]interface{}{
				"stop_reason": stopReason,
			},
			"usage": map[string]interface{}{
				"input_tokens":  response.UsageMetadata.PromptTokenCount,
				"output_tokens": response.UsageMetadata.CandidatesTokenCount,
			},
		}
	}

	return nil
}
