# lms-mcp

[![CI](https://github.com/sbot5/lms-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/sbot5/lms-mcp/actions/workflows/ci.yml)

把 Ed 和 Moodle 的课程内容**只读**镜像到本地，建立一个带全文搜索的 SQLite 索引，并作为 stdio MCP 服务器提供给 Claude Code 和 Codex 查询。可以问它「这周有哪些新公告、新成绩、临近的截止」，或基于课件做总结和复习。

- **严格只读**：绝不发帖、提交、答题或改设置。只读限制在 HTTP 层强制执行。
- **本地优先**：定时把内容同步到本地，工具默认查本地，需要时再实时刷新。
- 面向个人、Windows 使用；开发中，接口可能变化。

> 这是一个独立项目，与 Ed、Moodle 均无官方关系。

## 构建

需要 **Go 1.25 及以上**。GitHub Release 的 Windows 预编译包会在后续版本提供。

```sh
go build -trimpath -o bin/lms-mcp ./cmd/lms-mcp
# Windows: go build -trimpath -o bin/lms-mcp.exe ./cmd/lms-mcp
```

下面的例子假设 `lms-mcp` 在 PATH 上；源码构建则用 `./bin/lms-mcp`。

## 首次配置

```sh
lms-mcp setup                 # 交互式写出 config.json（选数据目录、Ed 区域、Moodle 网址）
lms-mcp auth ed               # 粘贴 Ed API token，存入 Windows 凭据管理器
lms-mcp auth moodle           # 存入 Moodle 会话（见下）
lms-mcp doctor                # 检查连通性、身份和课程发现
lms-mcp sync                  # 第一次同步
```

- **Ed token**：在 Ed 账号设置里创建 API token。
- **Moodle**：学校用 SSO + MFA 登录、且关闭了手机服务时，lms-mcp 用浏览器会话访问。目前在云端开发用环境变量注入会话；Windows 上用专用浏览器配置登录的 `auth moodle` 流程将在后续版本提供。细节见 [plan.md](docs/plan.md) 第 4 节。
- 凭证存在 Windows 凭据管理器，不落明文文件。云端开发时改读环境变量（见下）。

配置文件 `config.json`（相对路径都相对它所在目录解析）：

```json
{
  "data_dir": "data",
  "ed": { "enabled": true, "region": "au" },
  "moodle": { "enabled": true, "base_url": "https://moodle.example.edu" },
  "exclude": ["ABC9999"],
  "max_file_mb": 200
}
```

| 字段 | 说明 |
| --- | --- |
| `data_dir` | 索引和下载内容的存放目录，相对 config.json 解析。 |
| `ed.region` | `au`（默认）、`us`、`eu`，决定 Ed 区域主机。 |
| `moodle.base_url` | Moodle 站点根地址；也可用环境变量 `MOODLE_BASE_URL` 覆盖。 |
| `exclude` | 要跳过的课程代码（不区分大小写）。 |
| `max_file_mb` | 单文件下载上限，默认 200MB；视频只保留链接。 |

## MCP 接入

**Claude Code**：

```sh
claude mcp add -s user lms -- /绝对路径/lms-mcp mcp -config /绝对路径/config.json
```

**Codex**（`~/.codex/config.toml`）：

```toml
[mcp_servers.lms]
command = "/绝对路径/lms-mcp"
args = ["mcp", "-config", "/绝对路径/config.json"]
tool_timeout_sec = 120
```

MCP 走 stdio，stdout 只承载协议，日志走 stderr。工具（均为只读）：

| 工具 | 用途 |
| --- | --- |
| `list_courses` | 列出课程及 Ed/Moodle 配对。 |
| `get_status` | 凭证与同步状态（不显示凭证值）。 |
| `sync_start` / `sync_status` | 异步同步并查询进度。 |

更多查询工具（帖子、课件、成绩、截止、全文搜索）在后续里程碑加入，见 [plan.md](docs/plan.md)。

## 云端开发

在云环境里用环境变量提供凭证，不进入仓库：

- `MOODLE_BASE_URL`：Moodle 站点根地址。
- `ED_API_TOKEN`：Ed token；或设 `ED_AUTH=proxy`，由环境的 API-credential 代理注入 Authorization 头。
- `MOODLE_COOKIE`：Moodle 会话 cookie；或设 `MOODLE_AUTH=proxy` 用代理注入 Cookie 头。
- `MOODLE_ICAL_URL`：可选的日历导出链接。

## 命令

`setup`、`auth ed|moodle|status`、`doctor`、`sync`、`status`、`list`、`search <query>`、`mcp`、`version`。全局参数 `-config <文件>`（默认是系统配置目录下的 `lms-mcp/config.json`）。

## 开发

```sh
gofmt -l cmd internal    # 应无输出
go vet ./...
go test ./...
GOOS=windows GOARCH=amd64 go build ./cmd/lms-mcp   # Windows 交叉编译检查
```

测试全部离线，用脱敏样本和回环 `httptest` 服务器，不需要真实凭证。只用纯 Go 依赖（含 `modernc.org/sqlite`），所以能从 Linux 交叉编译到 Windows。

见 [架构](docs/architecture.md)、[开发计划](docs/plan.md) 和 [贡献说明](CONTRIBUTING.md)。调研笔记在 [docs/research/](docs/research/)。

MIT 许可证。
