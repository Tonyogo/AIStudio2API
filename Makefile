# ==============================================================================
# AIStudio2API Makefile
# ==============================================================================
# Linux 无头环境 Camoufox / Xvfb 图形库依赖安装指南:
#
# 1. Ubuntu 24.04 LTS+ / Debian 13 (如 Google Cloud Shell, 采用 64 位 t64 包名):
#    sudo apt update && sudo apt install -y xvfb libgtk-3-0t64 libasound2t64 libnss3 \
#        libdbus-glib-1-2 libxtst6 libxrandr2 libgbm1 libxkbcommon0 libpango-1.0-0 \
#        libcairo2 libxcomposite1 libxdamage1 libxfixes3
#
# 2. Ubuntu 22.04 LTS / Debian 11/12 (传统包名):
#    sudo apt update && sudo apt install -y xvfb libgtk-3-0 libasound2 libnss3 \
#        libdbus-glib-1-2 libxtst6 libxrandr2 libgbm1 libxkbcommon0 libpango-1.0-0 \
#        libcairo2 libxcomposite1 libxdamage1 libxfixes3
#
# 提示: 在 Linux 终端也可直接执行 `make deps` 自动识别系统版本并安装上述依赖。
# ==============================================================================

APP = aistudio2api

.PHONY: all start build build-web build-go import deps help

# 默认目标
all: help

# 启动服务：依赖 build，全量构建完成后在当前终端前台运行 (按 Ctrl+C 退出)
start: build
	@echo "==> 启动 AIStudio2API 服务..."
	./$(APP)

# 全量构建：依次执行前端构建与 Go 编译
build: build-web build-go
	@echo "==> 构建完成: ./$(APP)"

# 构建前端产物 (vue-tsc + vite build -> internal/webui/dist/)
build-web:
	@echo "==> 构建前端产物 (web)..."
	cd web && npm ci && npm run build

# 编译 Go 主程序
build-go:
	@echo "==> 编译 Go 二进制..."
	go build -o $(APP) ./cmd/aistudio2api

# 一键导入账号认证凭据
import:
	@echo "==> 执行账号凭据导入..."
	python3 import_auth.py

# 安装 Linux (Debian/Ubuntu) 虚拟显示与 Camoufox 运行库依赖
deps:
	@echo "==> 检查并安装 Linux 图形与 Xvfb 依赖..."
	@if command -v apt-get >/dev/null 2>&1; then \
		if apt-cache show libasound2t64 >/dev/null 2>&1; then \
			echo "检测到 Ubuntu 24.04+ / t64 体系，正在安装对应依赖包..."; \
			sudo apt-get update && sudo apt-get install -y xvfb libgtk-3-0t64 libasound2t64 libnss3 libdbus-glib-1-2 libxtst6 libxrandr2 libgbm1 libxkbcommon0 libpango-1.0-0 libcairo2 libxcomposite1 libxdamage1 libxfixes3; \
		else \
			echo "检测到传统 Debian/Ubuntu 体系，正在安装对应依赖包..."; \
			sudo apt-get update && sudo apt-get install -y xvfb libgtk-3-0 libasound2 libnss3 libdbus-glib-1-2 libxtst6 libxrandr2 libgbm1 libxkbcommon0 libpango-1.0-0 libcairo2 libxcomposite1 libxdamage1 libxfixes3; \
		fi \
	else \
		echo "错误: 当前系统未检测到 apt-get 包管理器，请手动安装对应的 Xvfb 和 GTK3/ALSA 库"; \
		exit 1; \
	fi

# 显示帮助信息
help:
	@echo "AIStudio2API 常用命令:"
	@echo "  make start   - 全量构建前端与 Go 程序，并在前台启动服务 (Ctrl+C 退出)"
	@echo "  make build   - 全量构建前端与 Go 二进制文件"
	@echo "  make import  - 执行浏览器账号凭据一键导入 (扫描 auth/ 目录)"
	@echo "  make deps    - 自动安装 Linux (Ubuntu/Debian) 下 Xvfb 与 Camoufox 依赖库"
	@echo "  make help    - 显示本帮助信息"
