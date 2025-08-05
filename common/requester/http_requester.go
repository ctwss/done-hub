package requester

import (
	"bufio"
	"bytes"
	"context"
	"done-hub/common"
	"done-hub/common/logger"
	"done-hub/common/utils"
	"done-hub/types"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type HttpErrorHandler func(*http.Response) *types.OpenAIError

type HTTPRequester struct {
	// requestBuilder    utils.RequestBuilder
	CreateFormBuilder func(io.Writer) FormBuilder
	ErrorHandler      HttpErrorHandler
	proxyAddr         string
	Context           context.Context
	IsOpenAI          bool
}

// NewHTTPRequester 创建一个新的 HTTPRequester 实例。
// proxyAddr: 是代理服务器的地址。
// errorHandler: 是一个错误处理函数，它接收一个 *http.Response 参数并返回一个 *types.OpenAIErrorResponse。
// 如果 errorHandler 为 nil，那么会使用一个默认的错误处理函数。
func NewHTTPRequester(proxyAddr string, errorHandler HttpErrorHandler) *HTTPRequester {
	return &HTTPRequester{
		CreateFormBuilder: func(body io.Writer) FormBuilder {
			return NewFormBuilder(body)
		},
		ErrorHandler: errorHandler,
		proxyAddr:    proxyAddr,
		Context:      context.Background(),
		IsOpenAI:     true,
	}
}

type requestOptions struct {
	body   any
	header http.Header
}

type requestOption func(*requestOptions)

func (r *HTTPRequester) setProxy() context.Context {
	return utils.SetProxy(r.proxyAddr, r.Context)
}

// 创建请求
func (r *HTTPRequester) NewRequest(method, url string, setters ...requestOption) (*http.Request, error) {
	args := &requestOptions{
		body:   nil,
		header: make(http.Header),
	}
	for _, setter := range setters {
		setter(args)
	}
	req, err := utils.RequestBuilder(r.setProxy(), method, url, args.body, args.header)
	if err != nil {
		return nil, err
	}

	return req, nil
}

// 发送请求
func (r *HTTPRequester) SendRequest(req *http.Request, response any, outputResp bool) (*http.Response, *types.OpenAIErrorWithStatusCode) {
	// 记录请求详情用于调试
	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err == nil {
			// 重新设置Body
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))

			bodyStr := string(bodyBytes)
			if len(bodyStr) > 1000 {
				logger.SysLog(fmt.Sprintf("[HTTPRequester] Request body (first 1000 chars): %s", bodyStr[:1000]))
			} else {
				logger.SysLog(fmt.Sprintf("[HTTPRequester] Request body: %s", bodyStr))
			}
		}
	}

	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, common.ErrorWrapper(err, "http_request_failed", http.StatusInternalServerError)
	}

	if !outputResp {
		defer resp.Body.Close()
	}

	// 处理响应
	if r.IsFailureStatusCode(resp) {
		return nil, HandleErrorResp(resp, r.ErrorHandler, r.IsOpenAI)
	}

	// 解析响应
	if response == nil {
		return resp, nil
	}

	if outputResp {
		var buf bytes.Buffer
		tee := io.TeeReader(resp.Body, &buf)
		err = DecodeResponse(tee, response)

		// 将响应体重新写入 resp.Body
		resp.Body = io.NopCloser(&buf)
	} else {
		// 读取响应内容用于日志记录
		bodyBytes, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			logger.SysError(fmt.Sprintf("[HTTPRequester] Failed to read response body: %v", readErr))
			return nil, common.ErrorWrapper(readErr, "response_read_failed", http.StatusInternalServerError)
		}

		// 记录响应内容的前500个字符用于调试
		bodyStr := string(bodyBytes)
		if len(bodyStr) > 500 {
			logger.SysLog(fmt.Sprintf("[HTTPRequester] Response body preview (first 500 chars): %s", bodyStr[:500]))
		} else {
			logger.SysLog(fmt.Sprintf("[HTTPRequester] Response body: %s", bodyStr))
		}

		// 检测是否意外收到了流式响应
		if strings.HasPrefix(bodyStr, "data: ") {
			logger.SysError("[HTTPRequester] Received unexpected streaming response for non-streaming request")
			// 尝试解析第一个有效的JSON chunk
			err = r.handleUnexpectedStreamResponse(bodyStr, response)
		} else {
			// 重新创建Reader用于JSON解码
			resp.Body = io.NopCloser(strings.NewReader(bodyStr))
			err = json.NewDecoder(resp.Body).Decode(response)
		}

		// 如果JSON解析失败，记录详细错误
		if err != nil {
			logger.SysError(fmt.Sprintf("[HTTPRequester] JSON decode failed: %v, Response body: %s", err, bodyStr))
		}
	}

	if err != nil {
		return nil, common.ErrorWrapper(err, "decode_response_failed", http.StatusInternalServerError)
	}

	return resp, nil
}

// handleUnexpectedStreamResponse 处理意外收到的流式响应
func (r *HTTPRequester) handleUnexpectedStreamResponse(bodyStr string, response interface{}) error {
	logger.SysLog("[HTTPRequester] Attempting to parse unexpected streaming response")

	// 分割SSE数据
	lines := strings.Split(bodyStr, "\n")
	var jsonChunks []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data != "[DONE]" && data != "" {
				jsonChunks = append(jsonChunks, data)
			}
		}
	}

	if len(jsonChunks) == 0 {
		return fmt.Errorf("no valid JSON chunks found in streaming response")
	}

	// 尝试解析第一个chunk
	firstChunk := jsonChunks[0]
	logger.SysLog(fmt.Sprintf("[HTTPRequester] Parsing first chunk: %s", firstChunk))

	// 解析为临时结构体
	var streamChunk map[string]interface{}
	if err := json.Unmarshal([]byte(firstChunk), &streamChunk); err != nil {
		return fmt.Errorf("failed to parse streaming chunk: %v", err)
	}

	// 转换流式chunk为非流式响应
	return r.convertStreamChunkToResponse(streamChunk, jsonChunks, response)
}

// convertStreamChunkToResponse 将流式chunks转换为非流式响应
func (r *HTTPRequester) convertStreamChunkToResponse(firstChunk map[string]interface{}, allChunks []string, response interface{}) error {
	// 构建非流式响应结构
	nonStreamResponse := map[string]interface{}{
		"id":      firstChunk["id"],
		"object":  "chat.completion", // 改为非流式对象类型
		"created": firstChunk["created"],
		"model":   firstChunk["model"],
		"choices": []map[string]interface{}{},
		"usage":   map[string]interface{}{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		},
	}

	// 合并所有chunks的内容
	var fullContent strings.Builder
	var finishReason string = "stop"

	for _, chunkStr := range allChunks {
		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(chunkStr), &chunk); err != nil {
			continue
		}

		if choices, ok := chunk["choices"].([]interface{}); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]interface{}); ok {
				if delta, ok := choice["delta"].(map[string]interface{}); ok {
					if content, ok := delta["content"].(string); ok {
						fullContent.WriteString(content)
					}
				}
				if reason, ok := choice["finish_reason"].(string); ok && reason != "" {
					finishReason = reason
				}
			}
		}
	}

	// 构建choice
	choice := map[string]interface{}{
		"index": 0,
		"message": map[string]interface{}{
			"role":    "assistant",
			"content": fullContent.String(),
		},
		"finish_reason": finishReason,
	}

	nonStreamResponse["choices"] = []map[string]interface{}{choice}

	// 转换为目标结构体
	jsonBytes, err := json.Marshal(nonStreamResponse)
	if err != nil {
		return fmt.Errorf("failed to marshal converted response: %v", err)
	}

	logger.SysLog(fmt.Sprintf("[HTTPRequester] Converted response: %s", string(jsonBytes)))

	return json.Unmarshal(jsonBytes, response)
}

// 发送请求 RAW
func (r *HTTPRequester) SendRequestRaw(req *http.Request) (*http.Response, *types.OpenAIErrorWithStatusCode) {
	// 发送请求
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, common.ErrorWrapper(err, "http_request_failed", http.StatusInternalServerError)
	}

	// 处理响应
	if r.IsFailureStatusCode(resp) {
		return nil, HandleErrorResp(resp, r.ErrorHandler, r.IsOpenAI)
	}

	return resp, nil
}

// 获取流式响应
func RequestStream[T streamable](requester *HTTPRequester, resp *http.Response, handlerPrefix HandlerPrefix[T]) (*streamReader[T], *types.OpenAIErrorWithStatusCode) {
	// 如果返回的头是json格式 说明有错误
	// if strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
	// 	return nil, HandleErrorResp(resp, requester.ErrorHandler, requester.IsOpenAI)
	// }

	stream := &streamReader[T]{
		reader:        bufio.NewReader(resp.Body),
		response:      resp,
		handlerPrefix: handlerPrefix,
		NoTrim:        false,

		DataChan: make(chan T),
		ErrChan:  make(chan error),
	}

	return stream, nil
}

func RequestNoTrimStream[T streamable](requester *HTTPRequester, resp *http.Response, handlerPrefix HandlerPrefix[T]) (*streamReader[T], *types.OpenAIErrorWithStatusCode) {
	stream, err := RequestStream(requester, resp, handlerPrefix)
	if err != nil {
		return nil, err
	}

	stream.NoTrim = true

	return stream, nil
}

// 设置请求体
func (r *HTTPRequester) WithBody(body any) requestOption {
	return func(args *requestOptions) {
		args.body = body
	}
}

// 设置请求头
func (r *HTTPRequester) WithHeader(header map[string]string) requestOption {
	return func(args *requestOptions) {
		for k, v := range header {
			args.header.Set(k, v)
		}
	}
}

// 设置Content-Type
func (r *HTTPRequester) WithContentType(contentType string) requestOption {
	return func(args *requestOptions) {
		args.header.Set("Content-Type", contentType)
	}
}

// 判断是否为失败状态码
func (r *HTTPRequester) IsFailureStatusCode(resp *http.Response) bool {
	return resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusBadRequest
}

// 处理错误响应
func HandleErrorResp(resp *http.Response, toOpenAIError HttpErrorHandler, isPrefix bool) *types.OpenAIErrorWithStatusCode {

	openAIErrorWithStatusCode := &types.OpenAIErrorWithStatusCode{
		StatusCode: resp.StatusCode,
		OpenAIError: types.OpenAIError{
			Message: "",
			Type:    "upstream_error",
			Code:    "bad_response_status_code",
			Param:   strconv.Itoa(resp.StatusCode),
		},
	}

	defer resp.Body.Close()

	// 记录错误响应的详细信息
	logger.SysError(fmt.Sprintf("[HandleErrorResp] HTTP %d error from upstream API", resp.StatusCode))

	if toOpenAIError != nil {
		bodyBytes, err := io.ReadAll(resp.Body)
		if err == nil {
			// 记录原始错误响应体
			bodyStr := string(bodyBytes)
			if len(bodyStr) > 1000 {
				logger.SysError(fmt.Sprintf("[HandleErrorResp] Error response body (first 1000 chars): %s", bodyStr[:1000]))
			} else {
				logger.SysError(fmt.Sprintf("[HandleErrorResp] Error response body: %s", bodyStr))
			}

			resp.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
			errorResponse := toOpenAIError(resp)

			if errorResponse != nil && errorResponse.Message != "" {
				logger.SysError(fmt.Sprintf("[HandleErrorResp] Parsed error: Code=%s, Type=%s, Message=%s",
					errorResponse.Code, errorResponse.Type, errorResponse.Message))

				if strings.HasPrefix(errorResponse.Message, "当前分组") {
					openAIErrorWithStatusCode.StatusCode = http.StatusTooManyRequests
				}

				openAIErrorWithStatusCode.OpenAIError = *errorResponse
				if isPrefix {
					openAIErrorWithStatusCode.OpenAIError.Message = fmt.Sprintf("Provider API error: %s", openAIErrorWithStatusCode.OpenAIError.Message)
				}
			}

			// 如果 errorResponse 为 nil，并且响应体为JSON，则将响应体转换为字符串
			if errorResponse == nil && strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
				openAIErrorWithStatusCode.OpenAIError.Message = string(bodyBytes)
				logger.SysError("[HandleErrorResp] No structured error parsed, using raw JSON as message")
			}
		} else {
			logger.SysError(fmt.Sprintf("[HandleErrorResp] Failed to read error response body: %v", err))
		}
	}

	if openAIErrorWithStatusCode.OpenAIError.Message == "" {
		if isPrefix {
			openAIErrorWithStatusCode.OpenAIError.Message = fmt.Sprintf("Provider API error: bad response status code %d", resp.StatusCode)
		} else {
			openAIErrorWithStatusCode.OpenAIError.Message = fmt.Sprintf("bad response status code %d", resp.StatusCode)
		}
	}

	return openAIErrorWithStatusCode
}

func SetEventStreamHeaders(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
}

func GetJsonHeaders() map[string]string {
	return map[string]string{
		"Content-type": "application/json",
	}
}

type Stringer interface {
	GetString() *string
}

func DecodeResponse(body io.Reader, v any) error {
	if v == nil {
		return nil
	}

	if result, ok := v.(*string); ok {
		return DecodeString(body, result)
	}

	if stringer, ok := v.(Stringer); ok {
		return DecodeString(body, stringer.GetString())
	}

	return json.NewDecoder(body).Decode(v)
}

func DecodeString(body io.Reader, output *string) error {
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	*output = string(b)
	return nil
}
