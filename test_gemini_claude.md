# Gemini Claude 原生路由测试

## 实现概述

为 ChannelTypeGemini (25) 添加了 Claude 原生路由支持，允许通过 `/claude/v1/messages` 端点访问 Google Gemini API，同时支持原生 Gemini API 和 OpenAI 兼容 API 两种模式。

## 修改的文件

1. **relay/claude.go** - 在 AllowChannelType 中添加了 config.ChannelTypeGemini
2. **providers/gemini/relay_claude.go** - 新建文件，实现了 Claude 接口方法
3. **web/src/views/Channel/type/Config.js** - 添加了更多 Gemini 模型
4. **model/price.go** - 为新模型添加了价格配置

## 实现细节

### Claude 接口实现

GeminiProvider 现在实现了 `claude.ClaudeChatInterface` 接口：

- `CreateClaudeChat(request *claude.ClaudeRequest) (*claude.ClaudeResponse, *types.OpenAIErrorWithStatusCode)`
- `CreateClaudeChatStream(request *claude.ClaudeRequest) (requester.StreamReaderInterface[string], *types.OpenAIErrorWithStatusCode)`

### 双模式支持

#### 模式 1: 原生 Gemini API
- 直接使用 Google Gemini API
- Claude 请求 → Gemini 格式 → Gemini API → Gemini 响应 → Claude 格式

#### 模式 2: OpenAI 兼容 API  
- 使用 Gemini 的 OpenAI 兼容端点
- Claude 请求 → OpenAI 格式 → OpenAI 兼容 API → OpenAI 响应 → Claude 格式

### 支持的模型

- `gemini-pro`
- `gemini-pro-vision`
- `gemini-1.0-pro`
- `gemini-1.5-pro`
- `gemini-1.5-flash`
- `gemini-2.0-flash-exp`

### 转换逻辑

#### 请求转换
1. Claude 消息格式 → OpenAI 消息格式
2. OpenAI 消息格式 → Gemini 消息格式
3. 处理系统消息、多模态内容、工具调用

#### 响应转换
1. Gemini 响应 → Claude 响应格式
2. 处理文本内容、工具调用、停止原因
3. 正确映射用量统计

## 测试用例

### 基本聊天请求

```bash
curl -X POST http://localhost:3000/claude/v1/messages \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "gemini-1.5-flash",
    "messages": [
      {
        "role": "user",
        "content": "Hello, how are you?"
      }
    ],
    "max_tokens": 1024
  }'
```

### 带系统消息的请求

```bash
curl -X POST http://localhost:3000/claude/v1/messages \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "gemini-1.5-pro",
    "system": "You are a helpful assistant that responds in a friendly manner.",
    "messages": [
      {
        "role": "user",
        "content": "What is the capital of France?"
      }
    ],
    "max_tokens": 500
  }'
```

### 流式请求

```bash
curl -X POST http://localhost:3000/claude/v1/messages \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "gemini-2.0-flash-exp",
    "messages": [
      {
        "role": "user",
        "content": "Tell me a short story"
      }
    ],
    "max_tokens": 1000,
    "stream": true
  }'
```

### 预期响应格式

```json
{
  "id": "chatcmpl-123456789",
  "type": "message",
  "role": "assistant",
  "model": "gemini-1.5-flash",
  "content": [
    {
      "type": "text",
      "text": "Hello! I'm doing well, thank you for asking. How can I assist you today?"
    }
  ],
  "stop_reason": "end_turn",
  "usage": {
    "input_tokens": 12,
    "output_tokens": 23
  }
}
```

## 配置说明

### Gemini 渠道配置

1. **渠道类型**: 选择 "Google Gemini" (25)
2. **API Key**: 输入 Google AI Studio 的 API Key
3. **Base URL**: 可选，默认使用 Google 官方端点
4. **版本号**: 在 "其他参数" 中设置，如 "v1beta"

### 插件配置

可以在渠道的插件配置中设置：

```json
{
  "use_openai_api": {
    "enable": true
  },
  "code_execution": {
    "enable": false
  }
}
```

- `use_openai_api`: 是否使用 OpenAI 兼容 API
- `code_execution`: 是否启用代码执行功能

## 验证步骤

1. 确保 Gemini 渠道配置正确
2. 设置渠道类型为 25 (Google Gemini)
3. 配置 Google AI Studio API Key
4. 测试 Claude 格式的请求
5. 验证响应格式符合 Claude 标准
6. 测试流式和非流式请求
7. 验证用量统计正确

## 注意事项

- 支持 Gemini 的原生 API 和 OpenAI 兼容 API 两种模式
- 自动处理消息格式转换和响应格式转换
- 支持多模态内容（文本、图像等）
- 兼容现有的 Gemini 渠道配置
- 正确处理用量统计和计费
- 支持工具调用和函数调用

## 错误处理

- 自动处理 API 限制和错误
- 正确映射 Gemini 错误到 Claude 错误格式
- 支持重试和故障转移机制
