@echo off
setlocal enabledelayedexpansion

REM ===========================================
REM ERP API Docker Build and Push Script
REM ===========================================

echo.
echo ========================================
echo ERP API Docker Build and Push Script
echo ========================================
echo.

REM Set variables
set IMAGE_NAME=done_hub
set REGISTRY_URL=registry.cn-beijing.aliyuncs.com/zksh/done_hub:latest
set BUILD_CONTEXT= .

echo [INFO] Image Name: %IMAGE_NAME%
echo [INFO] Registry URL: %REGISTRY_URL%
echo [INFO] Build Context: %BUILD_CONTEXT%
echo.

REM Check if Docker is running
echo [STEP 1/4] Checking Docker service status...
docker version >nul 2>&1
if %errorlevel% neq 0 (
    echo [ERROR] Docker service is not running or not installed. Please start Docker Desktop first.
    pause
    exit /b 1
)
echo [SUCCESS] Docker service is running
echo.

REM Build Docker image
echo [STEP 2/4] Building Docker image...
echo [EXEC] docker build -t %IMAGE_NAME% -f Dockerfile %BUILD_CONTEXT%
docker build -t %IMAGE_NAME% -f Dockerfile %BUILD_CONTEXT%
if %errorlevel% neq 0 (
    echo [ERROR] Docker image build failed
    pause
    exit /b 1
)
echo [SUCCESS] Docker image build completed
echo.

REM Tag the image
echo [STEP 3/4] Tagging the image...
echo [EXEC] docker tag %IMAGE_NAME% %REGISTRY_URL%
docker tag %IMAGE_NAME% %REGISTRY_URL%
if %errorlevel% neq 0 (
    echo [ERROR] Image tagging failed
    pause
    exit /b 1
)
echo [SUCCESS] Image tagging completed
echo.

REM Push image to registry
echo [STEP 4/4] Pushing image to Aliyun registry...
echo [TIP] If not logged in, please run: docker login registry.cn-beijing.aliyuncs.com
echo [EXEC] docker push %REGISTRY_URL%
docker push %REGISTRY_URL%
if %errorlevel% neq 0 (
    echo [ERROR] Image push failed. Please check network connection and login status.
    echo [TIP] Please login first: docker login registry.cn-beijing.aliyuncs.com
    pause
    exit /b 1
)
echo [SUCCESS] Image push completed
REM Display image information
echo ========================================
echo Build and Push Completed!
echo ========================================
echo Local Image: %IMAGE_NAME%
echo Remote Image: %REGISTRY_URL%
echo.
echo You can run the container with:
echo docker run -d -p 3000:3000 --name done_hub %REGISTRY_URL%
echo.

REM Ask if cleanup local images
set /p cleanup="Do you want to remove local built images? (y/N): "
if /i "%cleanup%"=="y" (
    echo [EXEC] Cleaning up local images...
    docker rmi %IMAGE_NAME% >nul 2>&1
    echo [DONE] Local images cleaned up
)

echo.
echo Script execution completed!
pause
