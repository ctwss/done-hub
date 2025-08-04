package openai

import (
	"done-hub/common"
	"done-hub/common/config"
	"done-hub/common/requester"
	"done-hub/providers/claude"
	commonadapter "done-hub/providers/common"
	"done-hub/types"
	"encoding/json"
	"io"
	"strings"
)

// CreateClaudeChat 实现 Claude 聊天接口
func (p *OpenAIProvider) CreateClaudeChat(request *claude.ClaudeRequest) (*claude.ClaudeResponse, *types.OpenAIErrorWithStatusCode) {
	// 使用统一的转换器
	converter := commonadapter.NewClaudeConverter()

	// 将 Claude 请求转换为 OpenAI 格式
	openaiRequest, errWithCode := converter.ConvertClaudeToOpenAI(request)
	if errWithCode != nil {
		return nil, errWithCode
	}

	// 使用 OpenAI provider 处理请求
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
func (p *OpenAIProvider) CreateClaudeChatStream(request *claude.ClaudeRequest) (requester.StreamReaderInterface[string], *types.OpenAIErrorWithStatusCode) {
	// 使用统一的转换器
	converter := commonadapter.NewClaudeConverter()

	// 将 Claude 请求转换为 OpenAI 格式
	openaiRequest, errWithCode := converter.ConvertClaudeToOpenAI(request)
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
		converter: converter,
	}

	return requester.RequestStream[string](p.Requester, resp, chatHandler.handlerStream)
}





// OpenAIClaudeStreamHandler 处理 OpenAI 到 Claude 的流式响应转换
type OpenAIClaudeStreamHandler struct {
	Usage     *types.Usage
	ModelName string
	Prefix    string
	converter *commonadapter.ClaudeConverter
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
	claudeStreamResponse := h.converter.ConvertOpenAIStreamToClaude(&openaiStreamResponse)
	if claudeStreamResponse != nil {
		responseBody, _ := json.Marshal(claudeStreamResponse)
		dataChan <- string(responseBody)
	}
}


