# OpenAI Claude 原生路由测试

## 实现概述

为 ChannelTypeOpenAI (1) 添加了 Claude 原生路由支持，允许通过 `/claude/v1/messages` 端点访问所有 OpenAI 模型，包括 GPT-4o, GPT-4, GPT-3.5-turbo 等。

## 修改的文件

1. **relay/claude.go** - 在 AllowChannelType 中添加了 config.ChannelTypeOpenAI
2. **providers/openai/relay_claude.go** - 新建文件，实现了 Claude 接口方法

## 实现细节

### Claude 接口实现

OpenAIProvider 现在实现了 `claude.ClaudeChatInterface` 接口：

- `CreateClaudeChat(request *claude.ClaudeRequest) (*claude.ClaudeResponse, *types.OpenAIErrorWithStatusCode)`
- `CreateClaudeChatStream(request *claude.ClaudeRequest) (requester.StreamReaderInterface[string], *types.OpenAIErrorWithStatusCode)`

### 转换逻辑

#### 请求转换 (Claude → OpenAI)
1. **消息格式转换**: Claude 消息 → OpenAI ChatCompletionMessage
2. **系统消息处理**: 将 Claude 的 system 字段转换为 OpenAI 的系统消息
3. **多模态内容**: 处理文本、图像、工具结果等内容类型
4. **工具定义**: Claude tools → OpenAI tools/functions
5. **参数映射**: temperature, top_p, max_tokens, stop_sequences 等

#### 响应转换 (OpenAI → Claude)
1. **响应格式**: OpenAI ChatCompletionResponse → Claude ClaudeResponse
2. **内容处理**: 文本内容和工具调用转换
3. **停止原因映射**: OpenAI finish_reason → Claude stop_reason
4. **用量统计**: OpenAI usage → Claude usage

### 支持的功能

✅ **基本聊天**: 支持所有 OpenAI 模型的文本对话  
✅ **流式响应**: 完整的流式输出支持  
✅ **系统消息**: 支持系统提示词  
✅ **多模态**: 支持图像输入（GPT-4V 等）  
✅ **工具调用**: 支持函数调用和工具使用  
✅ **参数控制**: temperature, top_p, max_tokens 等  
✅ **停止序列**: 支持自定义停止序列  
✅ **用量统计**: 准确的 token 计费  

### 支持的模型

- **GPT-4 系列**: gpt-4, gpt-4-turbo, gpt-4o, gpt-4o-mini
- **GPT-3.5 系列**: gpt-3.5-turbo, gpt-3.5-turbo-16k
- **O1 系列**: o1-preview, o1-mini, o3-mini
- **多模态模型**: gpt-4-vision-preview, gpt-4o
- **其他**: text-davinci-003, code-davinci-002 等

## 测试用例

### 基本聊天请求

```bash
curl -X POST http://localhost:3000/claude/v1/messages \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "gpt-4o",
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
    "model": "gpt-4",
    "system": "You are a helpful assistant that responds in a professional manner.",
    "messages": [
      {
        "role": "user",
        "content": "Explain quantum computing"
      }
    ],
    "max_tokens": 500,
    "temperature": 0.7
  }'
```

### 流式请求

```bash
curl -X POST http://localhost:3000/claude/v1/messages \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [
      {
        "role": "user",
        "content": "Write a short story about AI"
      }
    ],
    "max_tokens": 1000,
    "stream": true
  }'
```

### 工具调用请求

```bash
curl -X POST http://localhost:3000/claude/v1/messages \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "gpt-4o",
    "messages": [
      {
        "role": "user",
        "content": "What is the weather like in New York?"
      }
    ],
    "tools": [
      {
        "name": "get_weather",
        "description": "Get current weather information",
        "input_schema": {
          "type": "object",
          "properties": {
            "location": {
              "type": "string",
              "description": "The city name"
            }
          },
          "required": ["location"]
        }
      }
    ],
    "max_tokens": 500
  }'
```

### 多模态请求 (图像)

```bash
curl -X POST http://localhost:3000/claude/v1/messages \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "gpt-4o",
    "messages": [
      {
        "role": "user",
        "content": [
          {
            "type": "text",
            "text": "What do you see in this image?"
          },
          {
            "type": "image",
            "source": {
              "type": "base64",
              "media_type": "image/jpeg",
              "data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="
            }
          }
        ]
      }
    ],
    "max_tokens": 300
  }'
```

### 预期响应格式

```json
{
  "id": "chatcmpl-123456789",
  "type": "message",
  "role": "assistant",
  "model": "gpt-4o",
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

### OpenAI 渠道配置

1. **渠道类型**: 选择 "OpenAI" (1)
2. **API Key**: 输入 OpenAI API Key
3. **Base URL**: 可选，默认使用 OpenAI 官方端点
4. **模型**: 选择支持的 OpenAI 模型

### Azure OpenAI 配置

对于 Azure OpenAI 服务：

1. **Base URL**: 设置为 Azure 端点
2. **API Key**: 使用 Azure API Key
3. **API Version**: 在 "其他参数" 中设置

## 优势特性

### 1. 完整的 OpenAI 生态支持
- 支持所有 OpenAI 模型和功能
- 保持与 OpenAI API 的完全兼容性
- 支持最新的模型和特性

### 2. 统一的 Claude 接口
- 提供一致的 Claude API 体验
- 简化多模型集成
- 标准化的请求和响应格式

### 3. 高级功能支持
- 完整的工具调用支持
- 多模态内容处理
- 流式响应优化
- 精确的用量统计

### 4. 灵活的配置
- 支持自定义 Base URL
- 兼容 Azure OpenAI
- 支持代理和自定义端点

## 验证步骤

1. 确保 OpenAI 渠道配置正确
2. 设置渠道类型为 1 (OpenAI)
3. 配置有效的 OpenAI API Key
4. 测试 Claude 格式的请求
5. 验证响应格式符合 Claude 标准
6. 测试流式和非流式请求
7. 验证工具调用功能
8. 检查用量统计准确性

## 注意事项

- 支持所有 OpenAI 模型，包括最新的 GPT-4o 和 O1 系列
- 自动处理请求和响应格式转换
- 保持与原生 OpenAI API 的完全兼容性
- 支持 Azure OpenAI 和自定义端点
- 正确处理多模态内容和工具调用
- 精确的 token 计费和用量统计
