package vertexai

import (
	"done-hub/common"
	"done-hub/common/requester"
	"done-hub/providers/gemini"
	"done-hub/providers/vertexai/category"
	"done-hub/types"
	"net/http"
)

func (p *VertexAIProvider) CreateGeminiChat(request *gemini.GeminiChatRequest) (*gemini.GeminiChatResponse, *types.OpenAIErrorWithStatusCode) {
	req, errWithCode := p.getGeminiRequest(request)
	if errWithCode != nil {
		return nil, errWithCode
	}
	defer req.Body.Close()

	geminiResponse := &gemini.GeminiChatResponse{}
	// 发送请求
	_, openaiErr := p.Requester.SendRequest(req, geminiResponse, false)
	if openaiErr != nil {
		return nil, openaiErr
	}

	if len(geminiResponse.Candidates) == 0 {
		return nil, common.StringErrorWrapper("no candidates", "no_candidates", http.StatusInternalServerError)
	}

	usage := p.GetUsage()
	*usage = gemini.ConvertOpenAIUsage(geminiResponse.UsageMetadata)

	return geminiResponse, nil
}

func (p *VertexAIProvider) CreateGeminiChatStream(request *gemini.GeminiChatRequest) (requester.StreamReaderInterface[string], *types.OpenAIErrorWithStatusCode) {
	req, errWithCode := p.getGeminiRequest(request)
	if errWithCode != nil {
		return nil, errWithCode
	}
	defer req.Body.Close()

	channel := p.GetChannel()

	chatHandler := &gemini.GeminiRelayStreamHandler{
		Usage:     p.Usage,
		ModelName: request.Model,
		Prefix:    `data: `,

		Key: channel.Key,
	}

	// 发送请求
	resp, openaiErr := p.Requester.SendRequestRaw(req)
	if openaiErr != nil {
		return nil, openaiErr
	}

	stream, openaiErr := requester.RequestNoTrimStream(p.Requester, resp, chatHandler.HandlerStream)
	if openaiErr != nil {
		return nil, openaiErr
	}

	return stream, nil
}

func (p *VertexAIProvider) getGeminiRequest(request *gemini.GeminiChatRequest) (*http.Request, *types.OpenAIErrorWithStatusCode) {
	var err error
	p.Category, err = category.GetCategory(request.Model)
	if err != nil || p.Category.ChatComplete == nil || p.Category.ResponseChatComplete == nil {
		return nil, common.StringErrorWrapperLocal("vertexAI gemini provider not found", "vertexAI_err", http.StatusInternalServerError)
	}

	otherUrl := p.Category.GetOtherUrl(request.Stream)
	modelName := p.Category.GetModelName(request.Model)

	// 获取请求地址
	fullRequestURL := p.GetFullRequestURL(modelName, otherUrl)
	if fullRequestURL == "" {
		return nil, common.StringErrorWrapperLocal("vertexAI config error", "invalid_vertexai_config", http.StatusInternalServerError)
	}

	headers := p.GetRequestHeaders()

	if headers == nil {
		return nil, common.StringErrorWrapperLocal("vertexAI config error", "invalid_vertexai_config", http.StatusInternalServerError)
	}

	if request.Stream {
		headers["Accept"] = "text/event-stream"
	}

	rawData, exists := p.GetRawBody()
	if !exists {
		return nil, common.StringErrorWrapperLocal("request body not found", "request_body_not_found", http.StatusInternalServerError)
	}

	// 错误处理
	p.Requester.ErrorHandler = RequestErrorHandle(p.Category.ErrorHandler)

	// 清理原始 JSON 数据中不兼容的字段
	cleanedData, err := gemini.CleanGeminiRequestData(rawData, true)
	if err != nil {
		return nil, common.ErrorWrapper(err, "clean_vertexai_gemini_data_failed", http.StatusInternalServerError)
	}

	// 使用BaseProvider的统一方法创建请求，支持额外参数处理
	req, errWithCode := p.NewRequestWithCustomParams(http.MethodPost, fullRequestURL, cleanedData, headers, request.Model)
	if errWithCode != nil {
		return nil, errWithCode
	}
	return req, nil
}

func convertOpenAIUsage(geminiUsage *gemini.GeminiUsageMetadata) types.Usage {
	if geminiUsage == nil {
		return types.Usage{}
	}
	return types.Usage{
		PromptTokens:     geminiUsage.PromptTokenCount,
		CompletionTokens: geminiUsage.CandidatesTokenCount + geminiUsage.ThoughtsTokenCount,
		TotalTokens:      geminiUsage.TotalTokenCount,

		CompletionTokensDetails: types.CompletionTokensDetails{
			ReasoningTokens: geminiUsage.ThoughtsTokenCount,
		},
	}
}
