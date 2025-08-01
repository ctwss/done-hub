package ali

import (
	"done-hub/providers/openai"
	"errors"
	"fmt"
	"net/http"
)

const (
	// VisionModelKeywords 定义视觉模型的关键词
	VisionModelKeywords = "-vl,qvq,vision"
	// WebSearchSupportedModels 定义支持web_search插件的模型关键词
	WebSearchSupportedModels = "qwen-plus,qwen-turbo,qwen-max,qwq-plus"
)

func (p *AliProvider) GetModelList() ([]string, error) {
	fullRequestURL := fmt.Sprintf("%s%s", OpenaiBaseURL, p.Config.ModelList)
	headers := p.GetRequestHeaders()

	req, err := p.Requester.NewRequest(http.MethodGet, fullRequestURL, p.Requester.WithHeader(headers))
	if err != nil {
		return nil, errors.New("new_request_failed")
	}

	response := &openai.ModelListResponse{}
	_, errWithCode := p.Requester.SendRequest(req, response, false)
	if errWithCode != nil {
		return nil, errors.New(errWithCode.Message)
	}

	var modelList []string
	for _, model := range response.Data {
		modelList = append(modelList, model.Id)
	}

	return modelList, nil
}
