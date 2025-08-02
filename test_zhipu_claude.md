# 智谱 Claude 原生路由测试

## 实现概述

为 ChannelTypeZhipu (16) 添加了 Claude 原生路由支持，允许通过 `/claude/v1/messages` 端点访问智谱的 Claude 兼容 API。

## 修改的文件

1. **relay/claude.go** - 在 AllowChannelType 中添加了 config.ChannelTypeZhipu
2. **providers/zhipu/relay_claude.go** - 新建文件，实现了 Claude 接口方法
3. **web/src/views/Channel/type/Config.js** - 添加了 glm-4.5-air 模型
4. **model/price.go** - 为 glm-4.5-air 添加了价格配置

## 实现细节

### Claude 接口实现

ZhipuProvider 现在实现了 `claude.ClaudeChatInterface` 接口：

- `CreateClaudeChat(request *claude.ClaudeRequest) (*claude.ClaudeResponse, *types.OpenAIErrorWithStatusCode)`
- `CreateClaudeChatStream(request *claude.ClaudeRequest) (requester.StreamReaderInterface[string], *types.OpenAIErrorWithStatusCode)`

### API 端点配置

- 智谱 Claude API 端点: `https://open.bigmodel.cn/api/anthropic/v1/messages`
- 使用智谱现有的 JWT token 认证机制
- 支持自定义 base_url 配置

### 支持的模型

- 添加了 `glm-4.5-air` 模型支持
- 价格配置: ￥0.005 / 1k tokens

## 测试用例

### 基本聊天请求

```bash
curl -X POST http://localhost:3000/claude/v1/messages \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "glm-4.5-air",
    "messages": [
      {
        "role": "user",
        "content": "Hello"
      }
    ],
    "max_tokens": 1024
  }'
```

### 预期响应格式

```json
{
  "id": "202508021632015267aa4366ea47a0",
  "type": "message",
  "role": "assistant",
  "model": "glm-4.5-air",
  "content": [
    {
      "type": "text",
      "text": "Hello! 👋 How can I assist you today? Feel free to ask any questions or let me know what you'd like help with."
    }
  ],
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {
    "input_tokens": 6,
    "output_tokens": 172
  }
}
```

### 流式请求

```bash
curl -X POST http://localhost:3000/claude/v1/messages \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "glm-4.5-air",
    "messages": [
      {
        "role": "user",
        "content": "Hello"
      }
    ],
    "max_tokens": 1024,
    "stream": true
  }'
```

## 验证步骤

1. 确保智谱渠道配置正确
2. 设置渠道类型为 16 (Zhipu)
3. 配置智谱 API Key
4. 测试 Claude 格式的请求
5. 验证响应格式符合 Claude 标准

## 注意事项

- 智谱的 Claude API 使用与原生 API 相同的认证机制 (JWT token)
- 支持流式和非流式请求
- 自动处理用量统计和计费
- 兼容现有的智谱渠道配置
