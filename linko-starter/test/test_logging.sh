#!/bin/bash

LOG_FILE="linko.access.log"
SERVER_PORT=8899
SERVER_URL="http://localhost:$SERVER_PORT"

# 1. 环境清理
rm -f $LOG_FILE
echo "--- Starting Log Integration Test ---"

# 2. 启动服务器 (后台运行)
LINKO_LOG_FILE=$LOG_FILE go run . &
SERVER_PID=$!

# 确保脚本退出时关闭服务器
trap "kill $SERVER_PID" EXIT

# 等待服务器就绪
echo "Waiting for server to spin up..."
sleep 2

# 3. 触发错误逻辑：发送错误的登录信息
echo "Triggering invalid login..."
curl -s -X POST "$SERVER_URL/api/login" \
     -H "Content-Type: application/json" \
     -d '{"username":"saruman", "password":"wrong_password"}' > /dev/null

# 给异步写入日志一点时间
sleep 1

# 4. 断言检查
echo "--- Verification ---"

# 统计 "error validating password" 出现的次数
ERR_COUNT=$(grep -c "error validating password" $LOG_FILE)

if [ "$ERR_COUNT" -eq 1 ]; then
    echo "✅ PASS: Found exactly 1 authentication error log."
elif [ "$ERR_COUNT" -gt 1 ]; then
    echo "❌ FAIL: Found $ERR_COUNT logs. Redundant logs still exist!"
    exit 1
else
    echo "❌ FAIL: No error log found. Middleware might not be logging correctly."
    exit 1
fi

# 检查日志文件是否捕获了其他字段 (验证结构化)
if grep -q "method=POST" $LOG_FILE && grep -q "path=/api/login" $LOG_FILE; then
    echo "✅ PASS: Log contains correct structured metadata (method/path)."
else
    echo "❌ FAIL: Log structure is missing context."
fi

echo "--- Test Completed Successfully ---"
