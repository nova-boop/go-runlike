# go-runlike 🚀

[English](#english) | [中文](#中文)

---

<a name="english"></a>
## 🌍 English

**go-runlike** is a high-performance, single-binary tool written in Go. It reverse-engineers running Docker containers into their original `docker run` commands or modern `docker-compose.yml` files.

### ✨ Key Features
* **Zero Dependencies**: A static single binary. No Python or runtime libraries required.
* **Smart Parameter Merging**: Automatically combines flags into `-dit` for cleaner, more professional output.
* **Docker Compose Generation**: Use `-y` or `--yml` to instantly create production-ready Compose files.
* **Batch Export**: Use `-a` to export all containers at once — shell commands, YAML, or both.
* **Modern Networking**: Implements modern `networks` mapping with `external: true` declarations, replacing legacy `--link`.
* **Clean Output**: Intelligent diff-filtering excludes default image ENV variables and WorkDirs.
* **Comprehensive Support**: Handles Volumes, Devices, Logging, DNS, Sysctls, Capabilities, and more.

### 🚀 Quick Start

```bash
# Build
go build -ldflags="-s -w" -o runlike main.go

# Generate docker run command
./runlike my-container

# Generate pretty-printed command with backslashes
./runlike -p my-container

# Generate Docker Compose YAML
./runlike -y my-container
```

### 📦 Batch Export (-a)

Export all containers (running and stopped) to files:

```bash
# Export both shell commands and per-container .yml files to current directory
./runlike -a

# Export only docker_run_shell.txt (pretty-printed)
./runlike -a -p

# Export only per-container .yml files
./runlike -a -y

# Specify output directory (auto-created)
./runlike -a -o ./backup
```

Output structure:
```
./
├── docker_run_shell.txt      # All containers' docker run commands
├── nginx.yml                  # Per-container compose YAML
├── redis.yml
└── caddy_filebrowser.yml      # "/" in names replaced with "_"
```

### 📋 All Options

| Flag | Description |
|------|-------------|
| `-p` | Pretty-print with backslash line breaks |
| `-y`, `--yml` | Output as Docker Compose YAML |
| `-a` | Export all containers to files |
| `-o <dir>` | Output directory for `-a` mode (default: `./`, auto-created) |
| `-no-name` | Omit `--name` parameter |
| `-l` | Omit labels |

---

<a name="中文"></a>
## 🌍 中文

**go-runlike** 是一个高性能的 Go 单文件 CLI 工具。它通过 Docker Engine API 检查运行中的容器，反向生成等效的 `docker run` 命令或 `docker-compose.yml` 文件。

### ✨ 主要特性
* **零依赖**：静态编译的单文件二进制，无需 Python 或运行时库
* **智能参数合并**：自动将参数合并为 `-dit` 形式，输出更简洁专业
* **Docker Compose 生成**：使用 `-y` 或 `--yml` 即可生成生产级 Compose 文件
* **批量导出**：使用 `-a` 一次性导出所有容器 — shell 命令、YAML 或两者兼有
* **现代网络**：使用 `networks` 映射 + `external: true` 声明，取代旧的 `--link`
* **智能过滤**：自动对比镜像默认值，过滤掉冗余的 ENV、ExposedPorts、WorkDir
* **全面支持**：处理 Volumes、Devices、Logging、DNS、Sysctls、Capabilities 等

### 🚀 快速开始

```bash
# 编译
go build -ldflags="-s -w" -o runlike main.go

# 生成 docker run 命令
./runlike my-container

# 生成带换行的格式化命令
./runlike -p my-container

# 生成 Docker Compose YAML
./runlike -y my-container
```

### 📦 批量导出 (-a)

导出所有容器（含已停止的）到文件：

```bash
# 同时输出 docker_run_shell.txt 和每个容器的 .yml 文件到当前目录
./runlike -a

# 只输出 docker_run_shell.txt（带换行格式）
./runlike -a -p

# 只输出每个容器的独立 .yml 文件
./runlike -a -y

# 指定输出目录（目录不存在会自动创建）
./runlike -a -o ./backup
```

输出结构：
```
./
├── docker_run_shell.txt      # 所有容器的 docker run 命令汇总
├── nginx.yml                  # 每个容器的 compose YAML
├── redis.yml
└── caddy_filebrowser.yml      # 名称中的 "/" 替换为 "_"
```

### 📋 全部选项

| 参数 | 说明 |
|------|------|
| `-p` | 格式化输出，使用反斜杠换行 |
| `-y`, `--yml` | 输出 Docker Compose YAML 格式 |
| `-a` | 批量导出所有容器到文件 |
| `-o <目录>` | `-a` 模式的输出目录（默认 `./`，不存在自动创建） |
| `-no-name` | 不包含 `--name` 参数 |
| `-l` | 不包含 labels |
