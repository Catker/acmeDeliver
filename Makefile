.PHONY: all clean build server client test

# 默认目标
all: build

# 构建所有程序
build: server client

# 构建服务端
server:
	@echo "构建服务端..."
	@go build -o acmedeliver-server ./cmd/server

# 构建客户端
client:
	@echo "构建客户端..."
	@go build -o acmedeliver-client ./cmd/client

# 下载依赖
deps:
	@echo "下载依赖..."
	@go mod download
	@go mod tidy

# 清理构建文件
clean:
	@echo "清理构建文件..."
	@rm -f acmedeliver-server acmedeliver-client

# 运行测试
test:
	@echo "运行测试..."
	@go test ./...

# 生成示例配置
gen-config:
	@./acmedeliver-server --gen-config > config.yaml
	@echo "示例配置已生成: config.yaml"

# 帮助信息
help:
	@echo "可用目标:"
	@echo "  make build      - 构建服务端和客户端"
	@echo "  make server     - 构建服务端"
	@echo "  make client     - 构建客户端"
	@echo "  make deps       - 下载依赖"
	@echo "  make clean      - 清理构建文件"
	@echo "  make test       - 运行测试"
	@echo "  make gen-config - 生成示例配置文件"
