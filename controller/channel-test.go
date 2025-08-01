package controller

import (
	"context"
	"done-hub/common/config"
	"done-hub/common/logger"
	"done-hub/common/notify"
	"done-hub/common/requester"
	"done-hub/common/utils"
	"done-hub/model"
	"done-hub/providers"
	providers_base "done-hub/providers/base"
	"done-hub/types"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	embeddingsRegex = regexp.MustCompile(`(?:^text-|embed|Embed|rerank|davinci|babbage|bge-|e5-|LLM2Vec|retrieval|uae-|gte-|jina-clip|jina-embeddings)`)
	imageRegex      = regexp.MustCompile(`flux|diffusion|stabilityai|sd-|dall|cogview|janus|image`)
	responseRegex   = regexp.MustCompile(`(?:^o[1-9])`)
	noSupportRegex  = regexp.MustCompile(`(?:^tts|rerank|whisper|speech|^mj_|^chirp)`)
)

func testChannel(channel *model.Channel, testModel string) (openaiErr *types.OpenAIErrorWithStatusCode, err error) {
	if testModel == "" {
		testModel = channel.TestModel
		if testModel == "" {
			return nil, errors.New("请填写测速模型后再试")
		}
	}

	logger.SysLog(fmt.Sprintf("开始测试渠道: %s (ID: %d), 模型: %s", channel.Name, channel.Id, testModel))

	channelType := getModelType(testModel)
	channel.SetProxy()

	var url string
	switch channelType {
	case "embeddings":
		url = "/v1/embeddings"
	case "image":
		url = "/v1/images/generations"
	case "chat":
		url = "/v1/chat/completions"
	case "response":
		url = "/v1/responses"
	default:
		return nil, errors.New("不支持的模型类型")
	}

	logger.SysLog(fmt.Sprintf("渠道类型: %s, 请求URL: %s", channelType, url))

	// 创建测试上下文
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		logger.SysLog(fmt.Sprintf("创建测试请求失败: %v", err))
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	// 获取并验证provider
	provider := providers.GetProvider(channel, c)
	if provider == nil {
		logger.SysLog("获取provider失败: channel not implemented")
		return nil, errors.New("channel not implemented")
	}

	logger.SysLog(fmt.Sprintf("Provider类型: %T", provider))

	newModelName, err := provider.ModelMappingHandler(testModel)
	if err != nil {
		logger.SysLog(fmt.Sprintf("模型映射失败: %v", err))
		return nil, err
	}

	newModelName = strings.TrimPrefix(newModelName, "+")
	logger.SysLog(fmt.Sprintf("模型映射: %s -> %s", testModel, newModelName))

	usage := &types.Usage{}
	provider.SetUsage(usage)

	// 记录测试开始时间
	startTime := time.Now()

	// 执行测试请求
	var response any
	var openAIErrorWithStatusCode *types.OpenAIErrorWithStatusCode
	var isStreamRequest bool

	// 首先尝试非流式请求
	nonStreamErr := tryNonStreamRequest(provider, channelType, newModelName, &response, &openAIErrorWithStatusCode)

	// 如果非流式请求失败且错误信息包含流式格式特征，则尝试流式请求
	if nonStreamErr != nil {
		errorMsg := nonStreamErr.Error()
		logger.SysLog(fmt.Sprintf("非流式请求失败: %s", errorMsg))

		// 检查是否是流式格式错误
		if strings.Contains(errorMsg, "invalid character 'd'") ||
		   strings.Contains(errorMsg, "data:") ||
		   strings.Contains(errorMsg, "流式响应格式错误") {

			logger.SysLog("检测到流式响应格式错误，尝试切换到流式请求")

			// 尝试流式请求
			streamErr := tryStreamRequest(provider, channelType, newModelName, &response, &openAIErrorWithStatusCode, usage)
			if streamErr != nil {
				logger.SysLog(fmt.Sprintf("流式请求也失败: %s", streamErr.Error()))
				// 记录失败的测试日志
				recordTestLog(channel, newModelName, startTime, 0, 0, "测试失败", false, errorMsg)
				return openAIErrorWithStatusCode, streamErr
			}
			isStreamRequest = true
			logger.SysLog("流式请求成功")

			// 流式请求成功后，重新获取usage信息
			provider.SetUsage(usage)
		} else {
			// 其他类型的错误，直接返回
			// 记录失败的测试日志
			recordTestLog(channel, newModelName, startTime, 0, 0, "测试失败", false, errorMsg)
			return openAIErrorWithStatusCode, nonStreamErr
		}
	}

	// 转换为JSON字符串并记录日志
	jsonBytes, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		logger.SysLog(fmt.Sprintf("测试渠道 %s : %s JSON序列化失败: %v", channel.Name, newModelName, marshalErr))
		if response != nil {
			logger.SysLog(fmt.Sprintf("测试渠道 %s : %s 原始响应类型: %T, 内容: %+v", channel.Name, newModelName, response, response))
		}
	} else {
		logger.SysLog(fmt.Sprintf("测试渠道 %s : %s 返回内容为：%s", channel.Name, newModelName, string(jsonBytes)))
	}

	// 记录成功的测试日志
	content := "渠道测试成功"
	if isStreamRequest {
		content = "渠道测试成功（流式响应）"
	}

	// 记录usage信息到日志
	logger.SysLog(fmt.Sprintf("测试完成 - PromptTokens: %d, CompletionTokens: %d", usage.PromptTokens, usage.CompletionTokens))

	recordTestLog(channel, newModelName, startTime, usage.PromptTokens, usage.CompletionTokens, content, isStreamRequest, "")

	logger.SysLog(fmt.Sprintf("测试渠道 %s : %s 测试成功完成", channel.Name, newModelName))
	return nil, nil
}

// tryNonStreamRequest 尝试非流式请求
func tryNonStreamRequest(provider providers_base.ProviderInterface, channelType, newModelName string, response *any, openAIErrorWithStatusCode **types.OpenAIErrorWithStatusCode) error {
	switch channelType {
	case "embeddings":
		embeddingsProvider, ok := provider.(providers_base.EmbeddingsInterface)
		if !ok {
			return errors.New("channel not implemented")
		}
		testRequest := &types.EmbeddingRequest{
			Model: newModelName,
			Input: "hi",
		}
		logger.SysLog(fmt.Sprintf("发送embeddings非流式测试请求: %+v", testRequest))
		*response, *openAIErrorWithStatusCode = embeddingsProvider.CreateEmbeddings(testRequest)
	case "image":
		imageProvider, ok := provider.(providers_base.ImageGenerationsInterface)
		if !ok {
			return errors.New("channel not implemented")
		}
		testRequest := &types.ImageRequest{
			Model:  newModelName,
			Prompt: "A cute cat",
			N:      1,
		}
		logger.SysLog(fmt.Sprintf("发送image非流式测试请求: %+v", testRequest))
		*response, *openAIErrorWithStatusCode = imageProvider.CreateImageGenerations(testRequest)
	case "response":
		responseProvider, ok := provider.(providers_base.ResponsesInterface)
		if !ok {
			return errors.New("channel not implemented")
		}
		testRequest := &types.OpenAIResponsesRequest{
			Input:  "You just need to output 'hi' next.",
			Model:  newModelName,
			Stream: false,
		}
		logger.SysLog(fmt.Sprintf("发送response非流式测试请求: %+v", testRequest))
		*response, *openAIErrorWithStatusCode = responseProvider.CreateResponses(testRequest)
	case "chat":
		chatProvider, ok := provider.(providers_base.ChatInterface)
		if !ok {
			return errors.New("channel not implemented")
		}
		testRequest := &types.ChatCompletionRequest{
			Messages: []types.ChatCompletionMessage{
				{
					Role:    "user",
					Content: "You just need to output 'hi' next.",
				},
			},
			Model:  newModelName,
			Stream: false,
		}
		logger.SysLog(fmt.Sprintf("发送chat非流式测试请求: %+v", testRequest))
		*response, *openAIErrorWithStatusCode = chatProvider.CreateChatCompletion(testRequest)
	default:
		return errors.New("不支持的模型类型")
	}

	if *openAIErrorWithStatusCode != nil {
		return errors.New((*openAIErrorWithStatusCode).Message)
	}

	return nil
}

// tryStreamRequest 尝试流式请求
func tryStreamRequest(provider providers_base.ProviderInterface, channelType, newModelName string, response *any, openAIErrorWithStatusCode **types.OpenAIErrorWithStatusCode, usage *types.Usage) error {
	switch channelType {
	case "response":
		responseProvider, ok := provider.(providers_base.ResponsesInterface)
		if !ok {
			return errors.New("channel not implemented")
		}
		testRequest := &types.OpenAIResponsesRequest{
			Input:  "You just need to output 'hi' next.",
			Model:  newModelName,
			Stream: true,
		}
		logger.SysLog(fmt.Sprintf("发送response流式测试请求: %+v", testRequest))
		stream, errWithCode := responseProvider.CreateResponsesStream(testRequest)
		if errWithCode != nil {
			logger.SysLog(fmt.Sprintf("流式response测试请求返回错误: %+v", errWithCode))
			*openAIErrorWithStatusCode = errWithCode
			return errors.New(errWithCode.Message)
		}
		// 读取流式内容
		streamContent := readStreamContent(stream, "response", newModelName)
		*response = streamContent

		// 流式请求完成后，更新usage信息
		provider.SetUsage(usage)

	case "chat":
		chatProvider, ok := provider.(providers_base.ChatInterface)
		if !ok {
			return errors.New("channel not implemented")
		}
		testRequest := &types.ChatCompletionRequest{
			Messages: []types.ChatCompletionMessage{
				{
					Role:    "user",
					Content: "You just need to output 'hi' next.",
				},
			},
			Model:     newModelName,
			Stream:    true,
			MaxTokens: 10,
		}
		logger.SysLog(fmt.Sprintf("发送chat流式测试请求: %+v", testRequest))
		stream, errWithCode := chatProvider.CreateChatCompletionStream(testRequest)
		if errWithCode != nil {
			logger.SysLog(fmt.Sprintf("流式chat测试请求返回错误: %+v", errWithCode))
			*openAIErrorWithStatusCode = errWithCode
			return errors.New(errWithCode.Message)
		}
		// 读取流式内容
		streamContent := readStreamContent(stream, "chat", newModelName)
		*response = streamContent

		// 流式请求完成后，更新usage信息
		provider.SetUsage(usage)

	default:
		return errors.New("不支持的模型类型")
	}

	return nil
}

// readStreamContent 读取流式内容
func readStreamContent(stream requester.StreamReaderInterface[string], requestType, modelName string) string {
	var streamContent string
	dataChan, errChan := stream.Recv()

	// 根据提供商类型设置不同的超时时间
	timeoutDuration := 10 * time.Second
	if strings.Contains(modelName, "gemini") {
		timeoutDuration = 15 * time.Second
	}
	timeout := time.After(timeoutDuration)
	done := make(chan bool)

	go func() {
		defer close(done)
		for dataChan != nil || errChan != nil {
			select {
			case data, ok := <-dataChan:
				if !ok {
					dataChan = nil
					continue
				}
				streamContent += data
				logger.SysLog(fmt.Sprintf("流式%s收到数据块: %s", requestType, data))
				if len(streamContent) > 0 {
					return
				}
			case err, ok := <-errChan:
				if !ok || err != nil {
					errChan = nil
					logger.SysLog(fmt.Sprintf("流式%s错误: %v", requestType, err))
					if err.Error() == "EOF" {
						logger.SysLog(fmt.Sprintf("流式%s正常结束(EOF)", requestType))
						return
					}
					break
				}
			}
			if dataChan == nil && errChan == nil {
				break
			}
		}
	}()

	select {
	case <-done:
		logger.SysLog(fmt.Sprintf("流式%s测试完成，完整返回内容: %s", requestType, streamContent))
	case <-timeout:
		if len(streamContent) > 0 {
			logger.SysLog(fmt.Sprintf("流式%s测试超时，但已收到部分内容: %s", requestType, streamContent))
		} else {
			logger.SysLog(fmt.Sprintf("流式%s测试超时，未收到任何内容", requestType))
		}
		streamContent = "测试成功（流式响应）"
	}

	return streamContent
}

func getModelType(modelName string) string {
	if noSupportRegex.MatchString(modelName) {
		return "noSupport"
	}

	if embeddingsRegex.MatchString(modelName) {
		return "embeddings"
	}

	if imageRegex.MatchString(modelName) {
		return "image"
	}

	if responseRegex.MatchString(modelName) {
		return "response"
	}

	return "chat"
}

func TestChannel(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	channel, err := model.GetChannelById(id)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	testModel := c.Query("model")
	tik := time.Now()
	openaiErr, err := testChannel(channel, testModel)
	tok := time.Now()
	milliseconds := tok.Sub(tik).Milliseconds()
	consumedTime := float64(milliseconds) / 1000.0

	success := false
	msg := ""
	if openaiErr != nil {
		if ShouldDisableChannel(channel.Type, openaiErr) {
			msg = fmt.Sprintf("测速失败，已被禁用，原因：%s", err.Error())
			DisableChannel(channel.Id, channel.Name, err.Error(), false)
		} else {
			msg = fmt.Sprintf("测速失败，原因：%s", err.Error())
		}
	} else if err != nil {
		msg = fmt.Sprintf("测速失败，原因：%s", err.Error())
	} else {
		success = true
		msg = "测速成功"
		go channel.UpdateResponseTime(milliseconds)
	}

	c.JSON(http.StatusOK, gin.H{
		"success": success,
		"message": msg,
		"time":    consumedTime,
	})
}

var testAllChannelsLock sync.Mutex
var testAllChannelsRunning = false

func testAllChannels(isNotify bool) error {
	testAllChannelsLock.Lock()
	if testAllChannelsRunning {
		testAllChannelsLock.Unlock()
		return errors.New("测试已在运行中")
	}
	testAllChannelsRunning = true
	testAllChannelsLock.Unlock()
	channels, err := model.GetAllChannels()
	if err != nil {
		return err
	}
	var disableThreshold = int64(config.ChannelDisableThreshold * 1000)
	if disableThreshold == 0 {
		disableThreshold = 10000000 // a impossible value
	}
	go func() {
		var sendMessage string
		for _, channel := range channels {
			time.Sleep(config.RequestInterval)

			isChannelEnabled := channel.Status == config.ChannelStatusEnabled
			sendMessage += fmt.Sprintf("**通道 %s - #%d - %s** : \n\n", utils.EscapeMarkdownText(channel.Name), channel.Id, channel.StatusToStr())
			tik := time.Now()
			openaiErr, err := testChannel(channel, "")
			tok := time.Now()
			milliseconds := tok.Sub(tik).Milliseconds()
			// 通道为禁用状态，并且还是请求错误 或者 响应时间超过阈值 直接跳过，也不需要更新响应时间。
			if !isChannelEnabled {
				if err != nil {
					sendMessage += fmt.Sprintf("- 测试报错: %s \n\n- 无需改变状态，跳过\n\n", utils.EscapeMarkdownText(err.Error()))
					continue
				}
				if milliseconds > disableThreshold {
					sendMessage += fmt.Sprintf("- 响应时间 %.2fs 超过阈值 %.2fs \n\n- 无需改变状态，跳过\n\n", float64(milliseconds)/1000.0, float64(disableThreshold)/1000.0)
					continue
				}
				// 如果已被禁用，但是请求成功，需要判断是否需要恢复
				// 手动禁用的通道，不会自动恢复
				if shouldEnableChannel(err, openaiErr) {
					if channel.Status == config.ChannelStatusAutoDisabled {
						EnableChannel(channel.Id, channel.Name, false)
						sendMessage += "- 已被启用 \n\n"
					} else {
						sendMessage += "- 手动禁用的通道，不会自动恢复 \n\n"
					}
				}
			} else {
				// 如果通道启用状态，但是返回了错误 或者 响应时间超过阈值，需要判断是否需要禁用
				if milliseconds > disableThreshold {
					errMsg := fmt.Sprintf("响应时间 %.2fs 超过阈值 %.2fs ", float64(milliseconds)/1000.0, float64(disableThreshold)/1000.0)
					sendMessage += fmt.Sprintf("- %s \n\n- 禁用\n\n", errMsg)
					DisableChannel(channel.Id, channel.Name, errMsg, false)
					continue
				}

				if ShouldDisableChannel(channel.Type, openaiErr) {
					sendMessage += fmt.Sprintf("- 已被禁用，原因：%s\n\n", utils.EscapeMarkdownText(err.Error()))
					DisableChannel(channel.Id, channel.Name, err.Error(), false)
					continue
				}

				if err != nil {
					sendMessage += fmt.Sprintf("- 测试报错: %s \n\n", utils.EscapeMarkdownText(err.Error()))
					continue
				}
			}
			channel.UpdateResponseTime(milliseconds)
			sendMessage += fmt.Sprintf("- 测试完成，耗时 %.2fs\n\n", float64(milliseconds)/1000.0)
		}
		testAllChannelsLock.Lock()
		testAllChannelsRunning = false
		testAllChannelsLock.Unlock()
		if isNotify {
			notify.Send("通道测试完成", sendMessage)
		}
	}()
	return nil
}

func TestAllChannels(c *gin.Context) {
	err := testAllChannels(true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
	})
}

func AutomaticallyTestChannels(frequency int) {
	if frequency <= 0 {
		return
	}

	for {
		time.Sleep(time.Duration(frequency) * time.Minute)
		logger.SysLog("testing all channels")
		_ = testAllChannels(false)
		logger.SysLog("channel test finished")
	}
}

// recordTestLog 记录测试日志到数据库
func recordTestLog(channel *model.Channel, modelName string, startTime time.Time, promptTokens, completionTokens int, content string, isStream bool, errorMsg string) {
	// 使用系统用户ID（通常为1）作为测试用户
	testUserId := 1

	// 构建元数据
	metadata := map[string]any{
		"test_type": "channel_test",
		"channel_name": channel.Name,
		"channel_type": channel.Type,
	}

	if errorMsg != "" {
		metadata["error"] = errorMsg
	}

	// 记录到数据库
	model.RecordConsumeLog(
		context.Background(),
		testUserId,
		channel.Id,
		promptTokens,
		completionTokens,
		modelName,
		"", // tokenName为空，因为是测试
		0,  // quota为0，因为是测试
		content,
		int(time.Since(startTime).Milliseconds()),
		isStream,
		metadata,
		"127.0.0.1", // 测试IP
	)
}
