package relay

import (
	"done-hub/common"
	"done-hub/common/config"
	"done-hub/common/logger"
	"done-hub/common/requester"
	"done-hub/common/utils"
	"done-hub/providers/base"
	"done-hub/providers/claude"
	commonadapter "done-hub/providers/common"
	"done-hub/safty"
	"done-hub/types"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// AllowChannelType nil表示支持所有渠道类型
// 通过接口检测来确定是否支持 Claude 协议
var AllowChannelType []int = nil

type relayClaudeOnly struct {
	relayBase
	claudeRequest *claude.ClaudeRequest
}

func NewRelayClaudeOnly(c *gin.Context) *relayClaudeOnly {
	// 只有当AllowChannelType不为nil时才设置渠道类型过滤
	if AllowChannelType != nil {
		c.Set("allow_channel_type", AllowChannelType)
	}
	relay := &relayClaudeOnly{
		relayBase: relayBase{
			allowHeartbeat: true,
			c:              c,
		},
	}

	return relay
}

func (r *relayClaudeOnly) setRequest() error {
	r.claudeRequest = &claude.ClaudeRequest{}
	if err := common.UnmarshalBodyReusable(r.c, r.claudeRequest); err != nil {
		return err
	}
	r.setOriginalModel(r.claudeRequest.Model)
	// 设置原始模型到 Context，用于统一请求响应模型功能
	r.c.Set("original_model", r.claudeRequest.Model)

	// 检测背景任务（参考demo逻辑）
	if r.isBackgroundTask() {

		return r.handleBackgroundTaskInSetRequest()
	}

	// 保持原始的流式/非流式状态

	return nil
}

func (r *relayClaudeOnly) getRequest() interface{} {
	return r.claudeRequest
}

func (r *relayClaudeOnly) IsStream() bool {
	return r.claudeRequest.Stream
}

func (r *relayClaudeOnly) getPromptTokens() (int, error) {
	channel := r.provider.GetChannel()
	return CountTokenMessages(r.claudeRequest, channel.PreCost)
}

func (r *relayClaudeOnly) send() (err *types.OpenAIErrorWithStatusCode, done bool) {
	// 首先检查是否直接实现了 Claude 接口
	chatProvider, ok := r.provider.(claude.ClaudeChatInterface)
	if ok {
		// 直接使用Claude接口，适用于所有实现了Claude接口的Provider（包括Gemini、VertexAI、自定义渠道等）
		logger.SysLog(fmt.Sprintf("[Claude Relay] 使用原生 Claude 接口，Provider 类型: %T", r.provider))
		return r.sendWithClaudeInterface(chatProvider)
	}

	// 如果没有直接实现 Claude 接口，检查是否实现了 ChatInterface，使用通用适配器
	if baseChatProvider, ok := r.provider.(base.ChatInterface); ok {
		// 使用通用适配器，适用于所有实现了ChatInterface但未实现Claude接口的Provider
		adapter := commonadapter.NewClaudeAdapter(baseChatProvider)
		chatProvider = adapter
		logger.SysLog(fmt.Sprintf("[Claude Relay] 使用通用适配器为 Provider 类型 %T 提供 Claude 支持", r.provider))
		return r.sendWithClaudeInterface(chatProvider)
	}

	// 如果既没有实现Claude接口也没有实现ChatInterface，则报错
	logger.SysError(fmt.Sprintf("[Claude Relay] Provider 不支持 Claude 接口或 Chat 接口，Provider 类型: %T", r.provider))
	err = common.StringErrorWrapperLocal("channel not implemented", "channel_error", http.StatusServiceUnavailable)
	done = true
	return
}

func (r *relayClaudeOnly) GetError(err *types.OpenAIErrorWithStatusCode) (int, any) {
	newErr := FilterOpenAIErr(r.c, err)

	claudeErr := claude.OpenaiErrToClaudeErr(&newErr)

	return newErr.StatusCode, claudeErr.ClaudeError
}

func (r *relayClaudeOnly) HandleJsonError(err *types.OpenAIErrorWithStatusCode) {
	statusCode, response := r.GetError(err)
	r.c.JSON(statusCode, response)
}

func (r *relayClaudeOnly) HandleStreamError(err *types.OpenAIErrorWithStatusCode) {
	_, response := r.GetError(err)

	str, jsonErr := json.Marshal(response)
	if jsonErr != nil {
		logger.SysError("Failed to marshal error response: " + jsonErr.Error())
		return
	}
	_, writeErr := r.c.Writer.Write([]byte("event: error\ndata: " + string(str) + "\n\n"))
	if writeErr != nil {
		logger.SysError("Failed to write error response: " + writeErr.Error())
	}
	r.c.Writer.Flush()
}

func CountTokenMessages(request *claude.ClaudeRequest, preCostType int) (int, error) {
	if preCostType == config.PreContNotAll {
		return 0, nil
	}

	tokenEncoder := common.GetTokenEncoder(request.Model)

	tokenNum := 0

	tokensPerMessage := 4
	var textMsg strings.Builder

	for _, message := range request.Messages {
		tokenNum += tokensPerMessage
		switch v := message.Content.(type) {
		case string:
			textMsg.WriteString(v)
		case []any:
			for _, m := range v {
				content := m.(map[string]any)
				switch content["type"] {
				case "text":
					textMsg.WriteString(content["text"].(string))
				default:
					// 不算了  就只算他50吧
					tokenNum += 50
				}
			}
		}
	}

	if textMsg.Len() > 0 {
		tokenNum += common.GetTokenNum(tokenEncoder, textMsg.String())
	}

	return tokenNum, nil
}



// convertClaudeToOpenAI 将Claude请求转换为OpenAI格式
func (r *relayClaudeOnly) convertClaudeToOpenAI() (*types.ChatCompletionRequest, *types.OpenAIErrorWithStatusCode) {
	openaiRequest := &types.ChatCompletionRequest{
		Model:       r.claudeRequest.Model,
		Messages:    make([]types.ChatCompletionMessage, 0),
		MaxTokens:   r.claudeRequest.MaxTokens,
		Temperature: r.claudeRequest.Temperature,
		TopP:        r.claudeRequest.TopP,
		Stream:      r.claudeRequest.Stream,
	}

	// 处理 Stop 参数，过滤掉 null 值
	if r.claudeRequest.StopSequences != nil {
		openaiRequest.Stop = r.claudeRequest.StopSequences
	}

	// 处理系统消息
	if r.claudeRequest.System != nil {

		switch sys := r.claudeRequest.System.(type) {
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

	for _, msg := range r.claudeRequest.Messages {

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
	if len(r.claudeRequest.Tools) > 0 {
		tools := make([]*types.ChatCompletionTool, 0)
		// 转换为 OpenAI 格式

		for _, tool := range r.claudeRequest.Tools {
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
		if r.claudeRequest.ToolChoice != nil {
			openaiRequest.ToolChoice = r.claudeRequest.ToolChoice
		}
	}

	// 打印转换后的 OpenAI 请求内容

	return openaiRequest, nil
}

// convertOpenAIResponseToClaude 将OpenAI响应转换为Claude格式
func (r *relayClaudeOnly) convertOpenAIResponseToClaude(openaiResponse *types.ChatCompletionResponse) *claude.ClaudeResponse {
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

// isBackgroundTask 检测是否为背景任务（如话题分析）
// convertOpenAIStreamToClaude 将OpenAI流式响应转换为Claude格式
func (r *relayClaudeOnly) convertOpenAIStreamToClaude(stream requester.StreamReaderInterface[string]) int64 {

	r.c.Header("Content-Type", "text/event-stream")
	r.c.Header("Cache-Control", "no-cache")
	r.c.Header("Connection", "keep-alive")

	flusher, ok := r.c.Writer.(http.Flusher)
	if !ok {
		logger.SysError("Streaming unsupported")
		return 0
	}

	messageId := fmt.Sprintf("msg_%d", utils.GetTimestamp())
	model := r.modelName
	hasStarted := false
	hasTextContentStarted := false
	hasFinished := false
	contentChunks := 0
	toolCallChunks := 0
	isClosed := false
	isThinkingStarted := false
	contentIndex := 0
	processedInThisChunk := make(map[int]bool)

	// 保存最后的 usage 信息，用于 EOF 时补发
	var lastUsage map[string]interface{}

	// 累积工具调用的 token 数（用于当上游不提供 usage 时的计算）
	toolCallStatesForTokens := make(map[int]map[string]string) // 用于记录工具调用状态以便最后计算 tokens

	// 安全关闭函数，确保流正确结束
	safeClose := func() {
		if !isClosed {
			isClosed = true
			// 清理工具调用状态
			toolCallStates = make(map[int]map[string]interface{})
			toolCallToContentIndex = make(map[int]int)
		}
	}

	// 确保在函数结束时关闭流
	defer safeClose()

	var firstResponseTime int64
	isFirst := true

	dataChan, errChan := stream.Recv()

streamLoop:
	for {
		select {
		case rawLine := <-dataChan:
			if isClosed {
				break streamLoop
			}

			if isFirst {
				firstResponseTime = utils.GetTimestamp()
				isFirst = false
			}

			if !hasStarted && !isClosed && !hasFinished {
				hasStarted = true
				// 发送message_start事件（格式与demo完全一致）
				// 直接构造JSON字符串以确保字段顺序正确
				messageStartJSON := fmt.Sprintf(`{"type":"message_start","message":{"id":"%s","type":"message","role":"assistant","content":[],"model":"%s","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`, messageId, model)
				r.writeSSEEventRaw("message_start", messageStartJSON, &isClosed)
			}

			// 处理不同格式的流式数据
			var data string
			if strings.HasPrefix(rawLine, "data: ") {
				// SSE 格式: data: {...}
				data = strings.TrimPrefix(rawLine, "data: ")
				if data == "[DONE]" {
					break streamLoop
				}
			} else if strings.TrimSpace(rawLine) != "" && (strings.HasPrefix(rawLine, "{") || strings.HasPrefix(rawLine, ": OPENROUTER PROCESSING")) {
				// 直接 JSON 格式或处理标记
				if strings.HasPrefix(rawLine, ": OPENROUTER PROCESSING") {
					continue
				}
				data = rawLine
			} else {
				continue
			}

			var openaiChunk map[string]interface{}
			if err := json.Unmarshal([]byte(data), &openaiChunk); err != nil {
				continue
			}

			// 重置每个chunk的处理状态
			processedInThisChunk = make(map[int]bool)

			// 保存 usage 信息
			if usage, usageExists := openaiChunk["usage"].(map[string]interface{}); usageExists {
				lastUsage = usage
			}

			// 处理choices
			if choices, exists := openaiChunk["choices"].([]interface{}); exists && len(choices) > 0 {
				choice := choices[0].(map[string]interface{})

				// 处理delta内容
				if delta, exists := choice["delta"].(map[string]interface{}); exists {

					// 处理thinking内容
					if thinking, thinkingExists := delta["thinking"]; thinkingExists && !isClosed && !hasFinished {
						if thinkingMap, ok := thinking.(map[string]interface{}); ok {
							if !isThinkingStarted {
								contentBlockStart := map[string]interface{}{
									"type":  "content_block_start",
									"index": contentIndex,
									"content_block": map[string]interface{}{
										"type":     "thinking",
										"thinking": "",
									},
								}
								r.writeSSEEvent("content_block_start", contentBlockStart, &isClosed)
								flusher.Flush()
								isThinkingStarted = true
							}

							if signature, sigExists := thinkingMap["signature"]; sigExists {
								thinkingSignature := map[string]interface{}{
									"type":  "content_block_delta",
									"index": contentIndex,
									"delta": map[string]interface{}{
										"type":      "signature_delta",
										"signature": signature,
									},
								}
								r.writeSSEEvent("content_block_delta", thinkingSignature, &isClosed)
								flusher.Flush()

								contentBlockStop := map[string]interface{}{
									"type":  "content_block_stop",
									"index": contentIndex,
								}
								r.writeSSEEvent("content_block_stop", contentBlockStop, &isClosed)
								flusher.Flush()
								contentIndex++
							} else if content, contentExists := thinkingMap["content"]; contentExists {
								thinkingChunk := map[string]interface{}{
									"type":  "content_block_delta",
									"index": contentIndex,
									"delta": map[string]interface{}{
										"type":     "thinking_delta",
										"thinking": content,
									},
								}
								r.writeSSEEvent("content_block_delta", thinkingChunk, &isClosed)
								flusher.Flush()
							}
						}
					}
					// 处理文本内容
					if contentValue, contentExists := delta["content"]; contentExists && contentValue != nil && !isClosed && !hasFinished {
						if content, ok := contentValue.(string); ok {

							// 只有当内容不为空时才处理
							if content != "" {
								contentChunks++

								// 累积文本内容到 TextBuilder 用于 token 计算
								r.provider.GetUsage().TextBuilder.WriteString(content)

								if !hasTextContentStarted && !hasFinished {
									// 发送content_block_start事件（格式与demo一致）
									contentBlockStartJSON := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, contentIndex)
									r.writeSSEEventRaw("content_block_start", contentBlockStartJSON, &isClosed)
									hasTextContentStarted = true
								}

								// 发送content_block_delta事件（格式与demo一致）
								contentBytes, _ := json.Marshal(content)
								contentBlockDeltaJSON := fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%s}}`, contentIndex, string(contentBytes))
								r.writeSSEEventRaw("content_block_delta", contentBlockDeltaJSON, &isClosed)
							}
						}
					}

					// 处理工具调用
					if toolCalls, toolExists := delta["tool_calls"].([]interface{}); toolExists && !isClosed && !hasFinished {
						toolCallChunks++
						for _, toolCall := range toolCalls {
							if toolCallMap, ok := toolCall.(map[string]interface{}); ok {
								r.processToolCallDelta(toolCallMap, &contentIndex, flusher, processedInThisChunk, hasTextContentStarted, &isClosed, &hasFinished)

								// 累积工具调用信息（在流结束时统一计算 tokens）
								if function, funcExists := toolCallMap["function"].(map[string]interface{}); funcExists {
									toolCallIndex := 0 // 需要从 toolCallMap 中获取 index
									if idx, idxExists := toolCallMap["index"]; idxExists {
										if idxFloat, ok := idx.(float64); ok {
											toolCallIndex = int(idxFloat)
										} else if idxInt, ok := idx.(int); ok {
											toolCallIndex = idxInt
										}
									}

									// 确保索引不为负数
									if toolCallIndex < 0 {
										toolCallIndex = 0
									}

									if toolCallStatesForTokens[toolCallIndex] == nil {
										toolCallStatesForTokens[toolCallIndex] = map[string]string{
											"name":      "",
											"arguments": "",
										}
									}

									if name, nameExists := function["name"].(string); nameExists {
										toolCallStatesForTokens[toolCallIndex]["name"] = name
									}
									if args, argsExists := function["arguments"].(string); argsExists {
										toolCallStatesForTokens[toolCallIndex]["arguments"] += args
									}
								}
							}
						}
					}
				}

				// 处理finish_reason
				if finishReason, exists := choice["finish_reason"].(string); exists && finishReason != "" && !isClosed && !hasFinished {

					hasFinished = true

					// 检查是否有内容（用于调试，但不记录日志）
					if contentChunks == 0 && toolCallChunks == 0 {
						// 无内容的流响应，但这可能是正常情况（如背景任务）
					}

					// 发送content_block_stop事件 - 复刻JavaScript逻辑（格式与demo一致）
					if (hasTextContentStarted || toolCallChunks > 0) && !isClosed {
						contentBlockStopJSON := fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, contentIndex)
						r.writeSSEEventRaw("content_block_stop", contentBlockStopJSON, &isClosed)
					}

					// 转换停止原因
					claudeStopReason := "end_turn"
					switch finishReason {
					case "stop":
						claudeStopReason = "end_turn"
					case "length":
						claudeStopReason = "max_tokens"
					case "tool_calls":
						claudeStopReason = "tool_use"
					case "content_filter":
						claudeStopReason = "stop_sequence"
					}

					// 发送message_delta事件（格式与demo一致，必须包含usage字段）
					var messageDeltaJSON string
					if usage, usageExists := openaiChunk["usage"].(map[string]interface{}); usageExists {
						// 安全地获取token数量，防止类型断言失败
						inputTokens := 0
						outputTokens := 0
						if promptTokens, ok := usage["prompt_tokens"]; ok {
							if tokens, ok := promptTokens.(float64); ok {
								inputTokens = int(tokens)
							}
						}
						if completionTokens, ok := usage["completion_tokens"]; ok {
							if tokens, ok := completionTokens.(float64); ok {
								outputTokens = int(tokens)
							}
						}
						messageDeltaJSON = fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"%s","stop_sequence":null},"usage":{"input_tokens":%d,"output_tokens":%d}}`, claudeStopReason, inputTokens, outputTokens)
					} else {
						// 如果没有usage信息，计算工具调用和文本内容的 tokens
						currentUsage := r.provider.GetUsage()

						// 计算工具调用 tokens（在流结束时统一计算）
						estimatedOutputTokens := 0
						for _, toolCallState := range toolCallStatesForTokens {
							if name, nameExists := toolCallState["name"]; nameExists {
								args := toolCallState["arguments"]
								if name != "" {
									toolCallText := fmt.Sprintf("tool_use:%s:%s", name, args)
									tokens := common.CountTokenText(toolCallText, r.modelName)
									estimatedOutputTokens += tokens
								}
							}
						}

						// 累加文本内容的 tokens
						if currentUsage.TextBuilder.Len() > 0 {
							textTokens := common.CountTokenText(currentUsage.TextBuilder.String(), r.modelName)
							estimatedOutputTokens += textTokens
						}

						// 更新 Provider 的 Usage
						currentUsage.CompletionTokens = estimatedOutputTokens
						currentUsage.TotalTokens = currentUsage.PromptTokens + estimatedOutputTokens

						messageDeltaJSON = fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"%s","stop_sequence":null},"usage":{"input_tokens":%d,"output_tokens":%d}}`, claudeStopReason, currentUsage.PromptTokens, estimatedOutputTokens)
					}

					if !isClosed {
						r.writeSSEEventRaw("message_delta", messageDeltaJSON, &isClosed)
					}

					// 发送message_stop事件（格式与demo一致）
					if !isClosed {
						messageStopJSON := `{"type":"message_stop"}`
						r.writeSSEEventRaw("message_stop", messageStopJSON, &isClosed)
					}

					// 确保流正确结束
					safeClose()
					break streamLoop
				}
			}
		case err := <-errChan:
			if err != nil {
				if err.Error() == "EOF" {
					// 正常结束 - 确保发送完整的结束序列
					if !hasFinished && !isClosed {
						// 如果还没有发送结束事件，补发
						if hasTextContentStarted || toolCallChunks > 0 {
							contentBlockStopJSON := fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, contentIndex)
							r.writeSSEEventRaw("content_block_stop", contentBlockStopJSON, &isClosed)
						}

						// 使用保存的 usage 信息，如果没有则使用默认值
						var messageDeltaJSON string
						if lastUsage != nil {
							inputTokens := 0
							outputTokens := 0
							if promptTokens, ok := lastUsage["prompt_tokens"]; ok {
								if tokens, ok := promptTokens.(float64); ok {
									inputTokens = int(tokens)
								}
							}
							if completionTokens, ok := lastUsage["completion_tokens"]; ok {
								if tokens, ok := completionTokens.(float64); ok {
									outputTokens = int(tokens)
								}
							}
							messageDeltaJSON = fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":%d,"output_tokens":%d}}`, inputTokens, outputTokens)
						} else {
							currentUsage := r.provider.GetUsage()

							// 计算工具调用 tokens（在流结束时统一计算）
							estimatedOutputTokens := 0
							for _, toolCallState := range toolCallStatesForTokens {
								if name, nameExists := toolCallState["name"]; nameExists {
									args := toolCallState["arguments"]
									if name != "" {
										toolCallText := fmt.Sprintf("tool_use:%s:%s", name, args)
										estimatedOutputTokens += common.CountTokenText(toolCallText, r.modelName)
									}
								}
							}

							// 累加文本内容的 tokens
							if currentUsage.TextBuilder.Len() > 0 {
								textTokens := common.CountTokenText(currentUsage.TextBuilder.String(), r.modelName)
								estimatedOutputTokens += textTokens
							}

							// 更新 Provider 的 Usage
							currentUsage.CompletionTokens = estimatedOutputTokens
							currentUsage.TotalTokens = currentUsage.PromptTokens + estimatedOutputTokens

							messageDeltaJSON = fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":%d,"output_tokens":%d}}`, currentUsage.PromptTokens, estimatedOutputTokens)
						}
						r.writeSSEEventRaw("message_delta", messageDeltaJSON, &isClosed)

						messageStopJSON := `{"type":"message_stop"}`
						r.writeSSEEventRaw("message_stop", messageStopJSON, &isClosed)
					}

					safeClose()
					break streamLoop
				}
				logger.SysError("Stream read error: " + err.Error())
				safeClose()
			}
			break streamLoop
		}
	}

	return firstResponseTime
}

// 工具调用状态管理
var (
	toolCallStates         = make(map[int]map[string]interface{}) // toolCallIndex -> toolCallInfo
	toolCallToContentIndex = make(map[int]int)                    // toolCallIndex -> contentBlockIndex
)

// processToolCallDelta 处理工具调用的增量数据
func (r *relayClaudeOnly) processToolCallDelta(toolCall map[string]interface{}, contentIndex *int, flusher http.Flusher, processedInThisChunk map[int]bool, hasTextContentStarted bool, isClosed *bool, hasFinished *bool) {
	// 获取工具调用索引
	toolCallIndex := 0
	if index, exists := toolCall["index"].(float64); exists {
		toolCallIndex = int(index)
	}

	// 防止重复处理
	if processedInThisChunk[toolCallIndex] {
		return
	}
	processedInThisChunk[toolCallIndex] = true

	if function, exists := toolCall["function"].(map[string]interface{}); exists {
		// 检查是否是未知索引（新的工具调用）
		isUnknownIndex := false
		if _, exists := toolCallToContentIndex[toolCallIndex]; !exists {
			isUnknownIndex = true
		}

		if isUnknownIndex {
			// 计算新的内容块索引
			newContentBlockIndex := len(toolCallToContentIndex)
			if hasTextContentStarted {
				newContentBlockIndex = len(toolCallToContentIndex) + 1
			}

			// 如果不是第一个内容块，先发送前一个的 stop 事件
			if newContentBlockIndex != 0 {
				contentBlockStop := map[string]interface{}{
					"type":  "content_block_stop",
					"index": *contentIndex,
				}
				r.writeSSEEvent("content_block_stop", contentBlockStop, isClosed)
				flusher.Flush()
				*contentIndex++
			}

			// 设置索引映射
			toolCallToContentIndex[toolCallIndex] = newContentBlockIndex

			// 生成工具调用ID和名称 - 支持临时ID
			toolCallId := ""
			toolCallName := ""

			if id, idExists := toolCall["id"].(string); idExists && id != "" {
				toolCallId = id
			} else {
				toolCallId = fmt.Sprintf("call_%d_%d", utils.GetTimestamp(), toolCallIndex)
			}

			if name, nameExists := function["name"].(string); nameExists && name != "" {
				toolCallName = name
			} else {
				toolCallName = fmt.Sprintf("tool_%d", toolCallIndex)
			}

			// 发送 content_block_start 事件
			contentBlockStart := map[string]interface{}{
				"type":  "content_block_start",
				"index": *contentIndex,
				"content_block": map[string]interface{}{
					"type":  "tool_use",
					"id":    toolCallId,
					"name":  toolCallName,
					"input": map[string]interface{}{},
				},
			}
			r.writeSSEEvent("content_block_start", contentBlockStart, isClosed)
			flusher.Flush()

			// 保存工具调用状态
			toolCallStates[toolCallIndex] = map[string]interface{}{
				"id":                toolCallId,
				"name":              toolCallName,
				"arguments":         "",
				"contentBlockIndex": newContentBlockIndex,
			}
		} else if toolCall["id"] != nil && function["name"] != nil {
			// 处理ID更新
			if existingToolCall, exists := toolCallStates[toolCallIndex]; exists {
				existingId := existingToolCall["id"].(string)
				existingName := existingToolCall["name"].(string)

				// 检查是否是临时ID
				wasTemporary := strings.HasPrefix(existingId, "call_") && strings.HasPrefix(existingName, "tool_")

				if wasTemporary {
					if newId, ok := toolCall["id"].(string); ok && newId != "" {
						existingToolCall["id"] = newId
					}
					if newName, ok := function["name"].(string); ok && newName != "" {
						existingToolCall["name"] = newName
					}
				}
			}
		}

		// 处理参数增量
		if arguments, argsExists := function["arguments"].(string); argsExists && arguments != "" && !*isClosed && !*hasFinished {
			_, exists := toolCallToContentIndex[toolCallIndex]
			if !exists {
				return
			}

			// 更新累积的参数
			if currentToolCall, exists := toolCallStates[toolCallIndex]; exists {
				currentArgs := currentToolCall["arguments"].(string)
				currentToolCall["arguments"] = currentArgs + arguments

				// JSON 验证
				trimmedArgs := strings.TrimSpace(currentToolCall["arguments"].(string))
				if strings.HasPrefix(trimmedArgs, "{") && strings.HasSuffix(trimmedArgs, "}") {
					var parsedParams interface{}
					if err := json.Unmarshal([]byte(trimmedArgs), &parsedParams); err != nil {
						logger.SysError("Failed to validate tool arguments JSON: " + err.Error())
					}
				}
			}

			// 发送 input_json_delta 事件
			contentBlockDelta := map[string]interface{}{
				"type":  "content_block_delta",
				"index": *contentIndex,
				"delta": map[string]interface{}{
					"type":         "input_json_delta",
					"partial_json": arguments,
				},
			}
			r.writeSSEEvent("content_block_delta", contentBlockDelta, isClosed)
			flusher.Flush()
		}
	}
}

// writeSSEEvent 写入SSE事件 - 添加安全错误处理和连接状态检测（仅用于自定义渠道）
func (r *relayClaudeOnly) writeSSEEvent(eventType string, data interface{}, isClosed *bool) {
	if *isClosed {
		return
	}

	defer func() {
		if r := recover(); r != nil {
			*isClosed = true
		}
	}()

	// 检查客户端连接状态
	select {
	case <-r.c.Request.Context().Done():
		// 客户端已断开连接
		*isClosed = true
		return
	default:
		// 连接正常，继续处理
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		*isClosed = true
		return
	}

	_, err = fmt.Fprintf(r.c.Writer, "event: %s\ndata: %s\n\n", eventType, string(jsonData))
	if err != nil {
		// 检测常见的连接关闭错误
		if strings.Contains(err.Error(), "broken pipe") ||
			strings.Contains(err.Error(), "connection reset") ||
			strings.Contains(err.Error(), "write: connection reset by peer") ||
			strings.Contains(err.Error(), "client disconnected") {
			*isClosed = true
		}
		return
	}

	// 立即flush数据，确保客户端能及时收到
	if flusher, ok := r.c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

// writeSSEEventRaw 直接发送原始JSON字符串，确保字段顺序正确（仅用于自定义渠道）
func (r *relayClaudeOnly) writeSSEEventRaw(eventType, jsonData string, isClosed *bool) {
	if *isClosed {
		return
	}

	defer func() {
		if r := recover(); r != nil {
			*isClosed = true
		}
	}()

	// 检查客户端连接状态
	select {
	case <-r.c.Request.Context().Done():
		// 客户端已断开连接
		*isClosed = true
		return
	default:
		// 连接正常，继续处理
	}

	_, err := fmt.Fprintf(r.c.Writer, "event: %s\ndata: %s\n\n", eventType, jsonData)
	if err != nil {
		// 检测常见的连接关闭错误
		if strings.Contains(err.Error(), "broken pipe") ||
			strings.Contains(err.Error(), "connection reset") ||
			strings.Contains(err.Error(), "write: connection reset by peer") ||
			strings.Contains(err.Error(), "client disconnected") {
			*isClosed = true
		}
		return
	}

	// 立即flush数据，确保客户端能及时收到
	if flusher, ok := r.c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

// handleBackgroundTaskInSetRequest 在setRequest阶段处理背景任务

// isBackgroundTask 检测是否为背景任务（如话题分析）
func (r *relayClaudeOnly) isBackgroundTask() bool {
	if r.claudeRequest.System == nil {
		return false
	}

	var systemTexts []string

	switch sys := r.claudeRequest.System.(type) {
	case string:
		systemTexts = append(systemTexts, sys)
	case []interface{}:
		for _, item := range sys {
			if itemMap, ok := item.(map[string]interface{}); ok {
				if itemType, exists := itemMap["type"]; exists && itemType == "text" {
					if text, textExists := itemMap["text"]; textExists {
						if textStr, ok := text.(string); ok {
							systemTexts = append(systemTexts, textStr)
						}
					}
				}
			}
		}
	}

	// 检查系统消息是否包含背景任务标识
	for _, text := range systemTexts {
		if strings.Contains(text, "Summarize this coding conversation") ||
			strings.Contains(text, "write a 5-10 word title") ||
			strings.Contains(text, "Analyze if this message indicates a new conversation topic") {
			return true
		}
	}

	return false
}

// handleBackgroundTaskInSetRequest 在setRequest阶段处理背景任务
func (r *relayClaudeOnly) handleBackgroundTaskInSetRequest() error {

	if r.claudeRequest.Stream {
		// 流式响应：立即结束连接
		r.c.Header("Content-Type", "text/event-stream")
		r.c.Header("Cache-Control", "no-cache")
		r.c.Header("Connection", "keep-alive")

		// 发送最简单的完成事件并立即结束
		messageId := fmt.Sprintf("msg_bg_%d", utils.GetTimestamp())
		if _, err := r.c.Writer.Write([]byte(`data: {"type":"message_start","message":{"id":"` + messageId + `","type":"message","role":"assistant","content":[],"model":"` + r.modelName + `","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}` + "\n\n")); err != nil {
			logger.SysError("Failed to write message_start: " + err.Error())
		}
		if _, err := r.c.Writer.Write([]byte(`data: {"type":"message_stop"}` + "\n\n")); err != nil {
			logger.SysError("Failed to write message_stop: " + err.Error())
		}

		if flusher, ok := r.c.Writer.(http.Flusher); ok {
			flusher.Flush()
		}
	} else {
		// 非流式响应：立即返回空的Claude响应
		r.c.Header("Content-Type", "application/json")
		emptyResponse := &claude.ClaudeResponse{
			Id:         fmt.Sprintf("msg_bg_%d", utils.GetTimestamp()),
			Type:       "message",
			Role:       "assistant",
			Content:    []claude.ResContent{},
			Model:      r.modelName,
			StopReason: "end_turn",
			Usage: claude.Usage{
				InputTokens:  0,
				OutputTokens: 0,
			},
		}

		r.c.JSON(http.StatusOK, emptyResponse)
	}

	// 返回一个特殊错误，表示这是背景任务，已经处理完成
	return errors.New("background_task_handled")
}

// writeStreamResponse 直接写入流式响应
func (r *relayClaudeOnly) writeStreamResponse(response *http.Response) {
	// 设置响应头
	for k, v := range response.Header {
		for _, val := range v {
			r.c.Header(k, val)
		}
	}

	// 直接复制响应体
	defer response.Body.Close()
	if _, err := io.Copy(r.c.Writer, response.Body); err != nil {
		logger.SysError("Failed to copy response body: " + err.Error())
	}
}


// sendWithClaudeInterface 使用Claude接口处理请求
// 适用于所有实现了claude.ClaudeChatInterface的Provider（如Gemini、VertexAI等）
func (r *relayClaudeOnly) sendWithClaudeInterface(chatProvider claude.ClaudeChatInterface) (err *types.OpenAIErrorWithStatusCode, done bool) {
	r.claudeRequest.Model = r.modelName

	// 内容审查
	if config.EnableSafe {
		for _, message := range r.claudeRequest.Messages {
			if message.Content != nil {
				CheckResult, _ := safty.CheckContent(message.Content)
				if !CheckResult.IsSafe {
					err = common.StringErrorWrapperLocal(CheckResult.Reason, CheckResult.Code, http.StatusBadRequest)
					done = true
					return
				}
			}
		}
	}

	if r.claudeRequest.Stream {
		var response requester.StreamReaderInterface[string]
		response, err = chatProvider.CreateClaudeChatStream(r.claudeRequest)
		if err != nil {
			return
		}

		if r.heartbeat != nil {
			r.heartbeat.Stop()
		}

		doneStr := func() string {
			return ""
		}
		firstResponseTime := responseGeneralStreamClient(r.c, response, doneStr)
		r.SetFirstResponseTime(firstResponseTime)
	} else {
		var response *claude.ClaudeResponse
		response, err = chatProvider.CreateClaudeChat(r.claudeRequest)
		if err != nil {
			return
		}

		if r.heartbeat != nil {
			r.heartbeat.Stop()
		}

		openErr := responseJsonClient(r.c, response)

		if openErr != nil {
			err = openErr
		}
	}

	if err != nil {
		done = true
	}
	return
}
