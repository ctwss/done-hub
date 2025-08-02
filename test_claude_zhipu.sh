#!/bin/bash

# 智谱 Claude 原生路由测试脚本

# 配置
BASE_URL="http://localhost:3000"
API_KEY="YOUR_API_KEY"  # 请替换为实际的 API Key

echo "=== 智谱 Claude 原生路由测试 ==="
echo "Base URL: $BASE_URL"
echo "API Key: $API_KEY"
echo ""

# 测试 1: 基本聊天请求
echo "测试 1: 基本聊天请求"
echo "---"
curl -X POST "$BASE_URL/claude/v1/messages" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "glm-4.5-air",
    "messages": [
      {
        "role": "user",
        "content": "Hello, please respond with a simple greeting."
      }
    ],
    "max_tokens": 100
  }' | jq .

echo ""
echo ""

# 测试 2: 流式请求
echo "测试 2: 流式请求"
echo "---"
curl -X POST "$BASE_URL/claude/v1/messages" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "glm-4.5-air",
    "messages": [
      {
        "role": "user",
        "content": "Count from 1 to 5"
      }
    ],
    "max_tokens": 50,
    "stream": true
  }'

echo ""
echo ""

# 测试 3: 带系统消息的请求
echo "测试 3: 带系统消息的请求"
echo "---"
curl -X POST "$BASE_URL/claude/v1/messages" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "glm-4.5-air",
    "system": "You are a helpful assistant that responds in Chinese.",
    "messages": [
      {
        "role": "user",
        "content": "Hello"
      }
    ],
    "max_tokens": 100
  }' | jq .

echo ""
echo ""

# 测试 4: 模型列表
echo "测试 4: 获取模型列表"
echo "---"
curl -X GET "$BASE_URL/claude/v1/models" \
  -H "Authorization: Bearer $API_KEY" | jq .

echo ""
echo "=== 测试完成 ==="
