# go-runlike 🚀

[English](#english)

---

<a name="english"></a>
## 🌍 English

**go-runlike** is a high-performance, single-binary tool written in Go. It reverse-engineers running Docker containers into their original `docker run` commands or modern `docker-compose.yml` files.

### ✨ Key Features
* **Zero Dependencies**: A static single binary. No Python or runtime libraries required.
* **Smart Parameter Merging**: Automatically combines flags into `-dit` for cleaner, more professional output.
* **Docker Compose Generation**: Use `-y` or `--yml` to instantly create production-ready Compose files.
* **Modern Networking**: Implements modern `networks` mapping with `external: true` declarations, replacing legacy `--link`.
* **Clean Output**: Intelligent diff-filtering excludes default image ENV variables and WorkDirs.
* **Comprehensive Support**: Handles Volumes, Devices, Logging, DNS, Sysctls, Capabilities, and more.

### 🚀 Quick Start
```bash
# Generate docker run command
./go-runlike my-container

# Generate pretty-printed command with backslashes
./go-runlike -p my-container

# Generate Docker Compose YAML
./go-runlike -y my-container
```