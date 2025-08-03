package common

import (
	"done-hub/common"
	"done-hub/common/logger"
	"done-hub/common/requester"
	"done-hub/model"
	"done-hub/providers/base"
	"done-hub/providers/claude"
	"done-hub/types"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ClaudeAdapter 通用 Claude 适配器，为任何实现了 ChatInterface 的 provider 提供 Claude 支持
type ClaudeAdapter struct {
	ChatProvider base.ChatInterface
}

// 实现 base.ProviderInterface 的方法，直接委托给底层 ChatProvider

func (a *ClaudeAdapter) GetRequestHeaders() map[string]string {
	return a.ChatProvider.GetRequestHeaders()
}

func (a *ClaudeAdapter) GetUsage() *types.Usage {
	return a.ChatProvider.GetUsage()
}

func (a *ClaudeAdapter) SetUsage(usage *types.Usage) {
	a.ChatProvider.SetUsage(usage)
}

func (a *ClaudeAdapter) SetContext(c *gin.Context) {
	a.ChatProvider.SetContext(c)
}

func (a *ClaudeAdapter) SetOriginalModel(modelName string) {
	a.ChatProvider.SetOriginalModel(modelName)
}

func (a *ClaudeAdapter) GetOriginalModel() string {
	return a.ChatProvider.GetOriginalModel()
}

func (a *ClaudeAdapter) GetChannel() *model.Channel {
	return a.ChatProvider.GetChannel()
}

func (a *ClaudeAdapter) ModelMappingHandler(modelName string) (string, error) {
	return a.ChatProvider.ModelMappingHandler(modelName)
}

func (a *ClaudeAdapter) GetRequester() *requester.HTTPRequester {
	return a.ChatProvider.GetRequester()
}

func (a *ClaudeAdapter) SetOtherArg(otherArg string) {
	a.ChatProvider.SetOtherArg(otherArg)
}

func (a *ClaudeAdapter) GetOtherArg() string {
	return a.ChatProvider.GetOtherArg()
}

func (a *ClaudeAdapter) CustomParameterHandler() (map[string]interface{}, error) {
	return a.ChatProvider.CustomParameterHandler()
}

func (a *ClaudeAdapter) GetSupportedResponse() bool {
	return a.ChatProvider.GetSupportedResponse()
}

// NewClaudeAdapter 创建新的 Claude 适配器
func NewClaudeAdapter(chatProvider base.ChatInterface) *ClaudeAdapter {
	return &ClaudeAdapter{
		ChatProvider: chatProvider,
	}
}

// CreateClaudeChat 实现 Claude 聊天接口
func (a *ClaudeAdapter) CreateClaudeChat(request *claude.ClaudeRequest) (*claude.ClaudeResponse, *types.OpenAIErrorWithStatusCode) {
	// 将 Claude 请求转换为 OpenAI 格式
	openaiRequest, errWithCode := a.convertClaudeToOpenAI(request)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 使用底层 ChatProvider 处理请求
	openaiResponse, errWithCode := a.ChatProvider.CreateChatCompletion(openaiRequest)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 将 OpenAI 响应转换为 Claude 格式
	claudeResponse := a.convertOpenAIToClaude(openaiResponse, request)

	// 设置用量信息
	usage := a.ChatProvider.GetUsage()
	if openaiResponse.Usage != nil {
		claude.ClaudeUsageToOpenaiUsage(&claudeResponse.Usage, usage)
	}

	return claudeResponse, nil
}

// CreateClaudeChatStream 实现 Claude 流式聊天接口
func (a *ClaudeAdapter) CreateClaudeChatStream(request *claude.ClaudeRequest) (requester.StreamReaderInterface[string], *types.OpenAIErrorWithStatusCode) {
	// 将 Claude 请求转换为 OpenAI 格式
	openaiRequest, errWithCode := a.convertClaudeToOpenAI(request)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 设置流式请求
	openaiRequest.Stream = true

	// 使用底层 ChatProvider 处理流式请求
	streamReader, errWithCode := a.ChatProvider.CreateChatCompletionStream(openaiRequest)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 创建 Claude 格式的流处理器包装器
	claudeStreamWrapper := &ClaudeStreamWrapper{
		OriginalStream: streamReader,
		Usage:          a.ChatProvider.GetUsage(),
		ModelName:      request.Model,
	}

	return claudeStreamWrapper, nil
}

// convertClaudeToOpenAI 将 Claude 请求转换为 OpenAI 格式
func (a *ClaudeAdapter) convertClaudeToOpenAI(request *claude.ClaudeRequest) (*types.ChatCompletionRequest, *types.OpenAIErrorWithStatusCode) {
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
func (a *ClaudeAdapter) convertOpenAIToClaude(response *types.ChatCompletionResponse, request *claude.ClaudeRequest) *claude.ClaudeResponse {
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
						logger.SysError(fmt.Sprintf("Failed to unmarshal tool arguments: %v", err))
						input = map[string]interface{}{} // 使用空对象作为默认值
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

// ClaudeStreamWrapper 包装原始流，将 OpenAI 流式响应转换为 Claude 格式
type ClaudeStreamWrapper struct {
	OriginalStream requester.StreamReaderInterface[string]
	Usage          *types.Usage
	ModelName      string
	dataChan       chan string
	errChan        chan error
}

// Recv 接收并转换流式数据
func (w *ClaudeStreamWrapper) Recv() (<-chan string, <-chan error) {
	if w.dataChan == nil {
		w.dataChan = make(chan string)
		w.errChan = make(chan error)

		// 启动转换协程
		go w.processStream()
	}

	return w.dataChan, w.errChan
}

// Close 关闭流
func (w *ClaudeStreamWrapper) Close() {
	w.OriginalStream.Close()
}

// processStream 处理流式数据转换
func (w *ClaudeStreamWrapper) processStream() {
	defer close(w.dataChan)
	defer close(w.errChan)

	// 从原始流接收数据
	dataChan, errChan := w.OriginalStream.Recv()

	for {
		select {
		case data, ok := <-dataChan:
			if !ok {
				return // 数据通道已关闭
			}
			// 转换 OpenAI 流式响应为 Claude 格式
			claudeData := w.convertOpenAIStreamToClaude(data)
			if claudeData != "" {
				w.dataChan <- claudeData
			}
		case err, ok := <-errChan:
			if !ok {
				return // 错误通道已关闭
			}
			w.errChan <- err
			return
		}
	}
}

// convertOpenAIStreamToClaude 将 OpenAI 流式响应转换为 Claude 流式格式
func (w *ClaudeStreamWrapper) convertOpenAIStreamToClaude(data string) string {
	// 如果不是 data: 开头，直接返回
	if !strings.HasPrefix(data, "data: ") {
		return data
	}

	// 提取 JSON 部分
	jsonData := strings.TrimPrefix(data, "data: ")
	jsonData = strings.TrimSpace(jsonData)

	// 处理 [DONE] 标记
	if jsonData == "[DONE]" {
		return data // 保持原样
	}

	// 解析 OpenAI 流式响应
	var openaiStreamResponse types.ChatCompletionStreamResponse
	if err := json.Unmarshal([]byte(jsonData), &openaiStreamResponse); err != nil {
		return data // 解析失败，返回原数据
	}

	// 转换为 Claude 流式格式
	claudeStreamResponse := w.convertStreamResponse(&openaiStreamResponse)
	if claudeStreamResponse == nil {
		return "" // 跳过这个数据块
	}

	// 序列化为 JSON
	claudeData, err := json.Marshal(claudeStreamResponse)
	if err != nil {
		return data // 序列化失败，返回原数据
	}

	return "data: " + string(claudeData)
}

// convertStreamResponse 转换单个流式响应
func (w *ClaudeStreamWrapper) convertStreamResponse(response *types.ChatCompletionStreamResponse) interface{} {
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
					"input_tokens":  0, // 流式响应通常不包含准确的用量信息
					"output_tokens": 0,
				},
			}
		}
	}

	return nil
}
