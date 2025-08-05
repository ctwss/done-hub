package common

import (
	"done-hub/common"
	"done-hub/common/requester"
	"done-hub/model"
	"done-hub/providers/base"
	"done-hub/providers/claude"
	"done-hub/types"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ClaudeAdapter 通用 Claude 适配器，为任何实现了 ChatInterface 的 provider 提供 Claude 支持
type ClaudeAdapter struct {
	ChatProvider base.ChatInterface
	converter    *ClaudeConverter
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

func (a *ClaudeAdapter) GetContext() *gin.Context {
	return a.ChatProvider.GetContext()
}

func (a *ClaudeAdapter) SetOriginalModel(modelName string) {
	a.ChatProvider.SetOriginalModel(modelName)
}

func (a *ClaudeAdapter) GetOriginalModel() string {
	return a.ChatProvider.GetOriginalModel()
}

func (a *ClaudeAdapter) GetResponseModelName(requestModel string) string {
	return a.ChatProvider.GetResponseModelName(requestModel)
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
		converter:    NewClaudeConverter(),
	}
}

// CreateClaudeChat 实现 Claude 聊天接口
func (a *ClaudeAdapter) CreateClaudeChat(request *claude.ClaudeRequest) (*claude.ClaudeResponse, *types.OpenAIErrorWithStatusCode) {
	// 检查是否是阿里云Provider且使用原生模式
	if aliProvider, ok := a.ChatProvider.(interface{ GetUseOpenaiAPI() bool }); ok {
		if !aliProvider.GetUseOpenaiAPI() {
			return nil, common.StringErrorWrapper("Ali provider in native mode does not support Claude interface through adapter. Please configure the channel to use OpenAI compatible mode.", "unsupported_operation", http.StatusNotImplemented)
		}
	}

	// 将 Claude 请求转换为 OpenAI 格式
	openaiRequest, errWithCode := a.converter.ConvertClaudeToOpenAI(request)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 使用底层 ChatProvider 处理请求
	openaiResponse, errWithCode := a.ChatProvider.CreateChatCompletion(openaiRequest)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 将 OpenAI 响应转换为 Claude 格式
	claudeResponse := a.converter.ConvertOpenAIToClaude(openaiResponse)

	// 设置用量信息
	usage := a.ChatProvider.GetUsage()
	if openaiResponse.Usage != nil {
		claude.ClaudeUsageToOpenaiUsage(&claudeResponse.Usage, usage)
	}

	return claudeResponse, nil
}

// CreateClaudeChatStream 实现 Claude 流式聊天接口
func (a *ClaudeAdapter) CreateClaudeChatStream(request *claude.ClaudeRequest) (requester.StreamReaderInterface[string], *types.OpenAIErrorWithStatusCode) {
	// 检查是否是阿里云Provider且使用原生模式
	if aliProvider, ok := a.ChatProvider.(interface{ GetUseOpenaiAPI() bool }); ok {
		if !aliProvider.GetUseOpenaiAPI() {
			return nil, common.StringErrorWrapper("Ali provider in native mode does not support Claude interface through adapter. Please configure the channel to use OpenAI compatible mode.", "unsupported_operation", http.StatusNotImplemented)
		}
	}

	// 将 Claude 请求转换为 OpenAI 格式
	openaiRequest, errWithCode := a.converter.ConvertClaudeToOpenAI(request)
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
		converter:      a.converter,
	}

	return claudeStreamWrapper, nil
}

// 旧的转换函数已移动到 claude_converter.go 中的统一实现


// 旧的转换函数已移动到 claude_converter.go 中的统一实现


// ClaudeStreamWrapper 包装原始流，将 OpenAI 流式响应转换为 Claude 格式
type ClaudeStreamWrapper struct {
	OriginalStream requester.StreamReaderInterface[string]
	Usage          *types.Usage
	ModelName      string
	converter      *ClaudeConverter
	dataChan       chan string
	errChan        chan error
}

// SetConverter 设置转换器
func (w *ClaudeStreamWrapper) SetConverter(converter *ClaudeConverter) {
	w.converter = converter
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
