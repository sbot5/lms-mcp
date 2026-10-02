# lms-mcp v1 开发计划

> 这是开发的唯一事实来源：决策、架构、里程碑、验收标准和进度都在这里。新会话从 `AGENTS.md` 开始读，然后读本文件的「进度」表和当前里程碑那一节，不需要聊天记录。
> 调研笔记：[Ed API](research/ed-api.md) · [Moodle 会话访问](research/moodle-session.md) · [Moodle Web Services（学校已关闭，仅作参考）](research/moodle-ws.md) · [MCP 客户端限制](research/mcp-clients.md)

## 进度

| 里程碑 | 内容 | 状态 | PR |
|---|---|---|---|
| M0 | 计划、协作文件、调研笔记 | 用户已审阅通过（2026-10-02），待合并 | [#2](https://github.com/sbot5/lms-mcp/pull/2) |
| M1 | 新骨架：配置、凭证、HTTP 守卫、SQLite、认证、doctor/capture、MCP 框架 | 未开始 | |
| M2 | Ed 全量：Lessons、Resources、讨论、作答与截止 | 未开始 | |
| M3 | Moodle 课件与资源 | 未开始 | |
| M4 | 文档解析与全文搜索 | 未开始 | |
| M5 | Moodle 作业、测验、成绩、论坛、日历、站内通知 | 未开始 | |
| M6 | 每小时后台同步、Windows 通知、安装与发布 | 未开始 | |

每个里程碑一个 PR（D15）。合并后可以打 `v1.0.0-mN` 预发布标签，方便提前在 Windows 上试用。

## 1. 决策记录

「来源」列中，「用户」指 2026-10-02 访谈里用户的回答，「默认」指我提出、用户可以推翻的默认值。

| # | 主题 | 决定 | 来源 |
|---|---|---|---|
| D1 | 使用者 | 只给自己用：一名学生，只用 Windows。可以针对该校写死流程，但代码保持 provider 分层 | 用户 |
| D2 | 客户端 | Claude Code 为主，Codex 为次。不做 ChatGPT 或远程 HTTP 服务器 | 用户 |
| D3 | 站点 | Moodle：学校的 Moodle 站点（SSO + MFA）。站点地址只放在本地配置或环境变量 `MOODLE_BASE_URL` 里，不进仓库（D26）。Ed：`https://edstem.org/api`（AU；v0.3.0 已用真实 token 跑通） | 用户 |
| D4 | 课件位置 | 课件主要在 Ed Lessons。Moodle 第一优先是课件与资源下载 | 用户 |
| D5 | 数据模式 | 本地镜像加按需刷新：定时把全部内容（含成绩）同步到本地文件夹和 SQLite 索引。工具默认读本地，也可以实时刷新单项 | 用户 |
| D6 | 典型用法 | 动态汇总、截止与待办、基于课件学习、精确查找，四类都要 | 用户 |
| D7 | 读写 | 严格只读：不发帖、不提交、不答题、不标星、不主动标记已读。读取 Ed 帖子详情、读取 Moodle 论坛回帖时，平台可能隐式记为已读，用户已接受；「新内容」以 lms-mcp 自己的检测为准 | 用户 |
| D8 | 文档解析 | 把 PDF/PPTX/DOCX/ipynb 按页提取成文本，并建全文搜索。暂不做幻灯片截图 | 用户 |
| D9 | 后台与通知 | 增量同步加 Windows 通知。通知四类事件：老师帖与公告、新成绩与评语、临近截止（未提交，提前 48h 和 24h）、我的帖子有新回复 | 用户 |
| D10 | 课程范围 | 自动发现本学期全部课程，按课程代码（如 ABC1234）配对 Ed 与 Moodle，允许排除。历史学期保留，但不再刷新 | 用户 |
| D11 | 凭证 | 存在 Windows 凭据管理器。云端开发时读环境变量（`ED_API_TOKEN`、`MOODLE_COOKIE`、`MOODLE_ICAL_URL`），或由环境的 API credentials 在代理层注入（D30） | 用户 |
| D12 | 存储 | 普通本地文件夹，不放 OneDrive。视频不下载，只保留链接。单文件上限默认 200 MB，可配置 | 用户 |
| D13 | 兼容性 | 不兼容 v0.3.0，重新设计存储。升级后全量同步一次 | 用户 |
| D14 | 验收 | 在云端用开发专用 token 做真机验收，结束后吊销。接口返回脱敏成离线样本后入库 | 用户 |
| D15 | 交付 | 每个里程碑一个 PR，用户审核合并后再开下一个 | 用户 |
| D16 | 协作 | 加 AGENTS.md、CLAUDE.md 和 `.claude/agents`（Opus，high effort） | 用户 |
| D17 | 其他平台 | 暂不接入（课程录播、Gradescope、学校的其他系统） | 用户 |
| D18 | 安装 | GitHub Release 加一条命令的安装/升级脚本，自动写好 Claude Code 和 Codex 的 MCP 配置 | 用户 |
| D19 | 同步频率 | 每小时一次增量同步；每天一次全量对账 | 用户 |
| D20 | 姓名 | 老师和助教显示姓名，同学统一匿名为 `Student` | 用户 |
| D21 | 作答 | 同步自己的 quiz 作答、代码提交和已公布的答案，只读不提交 | 用户 |
| D22 | 技术栈 | 继续用 Go，单文件可执行程序，只用纯 Go 依赖（不用 CGO）。SQLite 用 `modernc.org/sqlite`（含 FTS5） | 默认 |
| D23 | 语言 | 面向用户的文档用中文。代码、注释、工具描述和 AGENTS.md 用英文（对模型更友好） | 默认 |
| D24 | Ed 私密帖 | 默认包含（只会是你自己的或分享给你的私密帖），这样「我的帖子新回复」提醒才完整 | 默认 |
| D25 | Moodle 认证 | ~~App 同款 token~~ 不可行：2026-10-02 探测显示学校关闭了移动服务（`enablemobilewebservice=0`，`typeoflogin=1`，SAML2 登录，Moodle 4.5–5.1），学生也拿不到其他 Web Service token。改为浏览器会话方案（D27） | 探测 |
| D26 | 公开仓库 | 仓库保持公开，但提交的任何内容（文档、样本、测试、提交信息）都不出现学校名、Moodle 域名或其他能识别用户的信息 | 用户 |
| D27 | Moodle 会话 | 主方案：只在登录时用一个独立的 Edge 配置文件（CDP 驱动），读出会话 cookie 存进凭据管理器。平时同步只用 cookie 加 sesskey 发普通 HTTP 请求，不开浏览器。见 §4 | 用户 |
| D28 | 访问记录 | 接受 Moodle 记录「已查看」，并可能自动勾选「查看即完成」，但要尽量少触发：课件只在第一次下载时打开一次页面，之后直接用文件链接；成绩页只在收到评分通知时或每天读一次；作业和测验页只在新出现或你问到时读 | 用户 |
| D29 | 重新登录 | 你几乎每天都要重新输 学校统一登录的密码和 MFA。会话过期时，先用那个 Edge 配置文件在后台静默续期；续不上再弹通知，你点一下完成登录。登录过期期间 Ed 照常同步，截止提醒靠 D31 | 用户 |
| D30 | 云端 Moodle 会话 | 云端开发用环境的 API credentials 注入 `Cookie` 头，cookie 不进入云端机器。环境变量设 `MOODLE_AUTH=proxy`。验收结束后在浏览器里点 Log out，cookie 立即失效 | 用户 |
| D31 | 日历保底 | 用 Moodle 日历导出链接（`MOODLE_ICAL_URL`，长期有效、只能读日历）给截止日期做保底，不依赖登录状态 | 用户 |

## 2. 目标架构

### 2.1 包结构

```text
cmd/lms-mcp          CLI 入口：auth、doctor、capture、sync、status、search、mcp、schedule、setup、version
internal/config      配置（%APPDATA%\lms-mcp\config.json）
internal/secrets     Windows 凭据管理器；非 Windows 或云端回退到环境变量
internal/httpx       共享 HTTP：User-Agent、每个主机限速、重试（429/5xx/Retry-After）、超时、错误脱敏、只读守卫
internal/ed          Ed API 客户端（只发 GET）
internal/moodle      Moodle 会话客户端：AJAX（函数白名单）、页面与文件、iCal、公开配置探测；Windows 上用 CDP 驱动 Edge 登录
internal/render      Ed XML、Moodle HTML 转 Markdown（沿用 v0.3.0 的 render 包）
internal/store       SQLite（WAL）：schema、迁移、FTS5、事件、同步租约
internal/files       文件镜像：流式下载、临时文件加原子改名、大小上限、视频只留链接
internal/extract     PDF/PPTX/DOCX/ipynb/HTML 按页转文本，按内容哈希缓存
internal/sync        编排：课程发现与配对、各 provider 增量同步、变更检测生成事件
internal/notify      通知规则、聚合去重、Windows toast
internal/mcpserver   MCP 工具、资源、提示，以及分页和输出预算
internal/anonymize   capture 用的结构保留型脱敏
```

依赖方向：`cmd` → `sync` / `mcpserver` / `notify` → `store` / `files` / `extract` / `render` → `ed` / `moodle` → `httpx`。provider 包只管协议，不碰数据库和文件系统。

### 2.2 本地数据布局

```text
<数据根目录，例如 D:\Study\lms>\
  lms.db (+ -wal, -shm)        元数据、正文、提取文本、FTS、事件、同步记录
  logs\                        同步日志（不含凭证，也不含带 token 的 URL）
  ABC1234-2026S2\              课程代码加学期
    ed\lessons\<模块>\<序号 课时名>\lesson.md 及其 PDF、附件
    ed\resources\<分类>\<文件>
    moodle\<节>\<活动>\<文件>
    moodle\<节>\<页面或 Book>.md
```

- 讨论帖、公告、成绩、截止日期只存在 SQLite 里，由工具渲染成 Markdown。课件和附件落盘，Claude Code 可以直接打开原文件。
- 文件不会因为远端删除而被删掉：远端删除只在索引里标记 `removed`。

### 2.3 SQLite 模型（草图，M1 定稿）

- `courses`：id, code, term, title, ed_course_id, moodle_course_id, active, excluded
- `items`：id（如 `ed:thread:123`、`ed:slide:456`、`moodle:cm:789`、`moodle:post:12`）, course_id, provider, kind, parent_id, title, url, author_role, author_display, created_at, updated_at, body_md, content_hash, meta_json, removed_at
- `files`：id, item_id, name, mime, size, source_url（已去掉 token）, local_path, sha256, etag, last_modified, status（`ok` / `video_link` / `too_large` / `failed`）, error
- `texts`：source_id, page, text；外加 FTS5 表 `fts`(title, body)
- `grades`：course_id, item_key, name, grade, grade_max, percentage, feedback_md, graded_at, hash
- `deadlines`：course_id, item_id, kind, opens_at, due_at, cutoff_at, submission_status, completed
- `events`：id, course_id, item_id, kind, detected_at, title, summary, baseline, notified_at
- `sync_runs`：id, scope, trigger, started_at, finished_at, status, counts_json, error
- `leases`：name, holder, expires_at
- `meta`：schema 版本等

### 2.4 同步策略

- **课程发现**：
  - Ed 从 `/user` 读课程，按 year/session 判断本学期。
  - Moodle 用 `core_course_get_enrolled_courses_by_timeline_classification`（`inprogress`）。
  - 两边用课程代码正则（默认 `[A-Z]{3,4}\d{4}`，可配置）提取代码来配对，配置里可以手动覆盖或排除。
- **Ed**（端点见 [ed-api.md](research/ed-api.md)）：
  - Lessons：每次都列 `/courses/{c}/lessons`。有变化的课时再拉 `/lessons/{l}`，**绝不带 `?view=1`**。
    - 幻灯片转成 Markdown，下载 PDF 幻灯片和附件。
    - quiz 同步题目、自己的作答和已公布的答案。
    - challenge 只取描述，丢弃 JWT/ticket 字段。
    - 自己的提交和成绩：学生能读到就同步。
  - Resources：课程的 `features.resources` 打开时，列表并下载。
  - 讨论：按 `sort=new` 翻页到水位线，对新帖和有变化的帖子拉详情；每天一次全量对账，检测删除和编辑。
- **Moodle**：走浏览器会话，按访问记录从少到多排序取数据。路径、白名单和会话管理见 §4。
  - 截止日期优先从日历导出链接取（D31）。
- **变更检测**：对每个条目的规范化内容算哈希。第一次导入是 baseline，不提醒。
- **事件类型**：`staff_post`、`announcement`、`reply_to_me`、`new_material`、`material_changed`、`new_grade`、`feedback`、`deadline_added`、`deadline_changed`。`deadline_soon` 在本地计算。
- **并发**：Claude Code、Codex、计划任务可能同时各开一个进程。SQLite 用 WAL 和 busy_timeout，同步用租约保证同一时刻只跑一个。
- **礼貌**：Ed 每秒最多 5 个请求，Moodle 每秒最多 4 个，单次同步有总时长上限。

### 2.5 MCP 工具（草案，约 14 个）

| 工具 | 用途 |
|---|---|
| `list_courses` | 本学期课程、Ed/Moodle 配对，以及各自最近一次成功同步的时间 |
| `course_overview` | 一门或全部课程的近况：新公告、临近截止、最新成绩、新课件（「动态汇总」的入口） |
| `whats_new` | 统一变更流，可按 since、course、kinds、staff_only 过滤，分页 |
| `upcoming_deadlines` | 未来 N 天的截止项（Moodle 作业/测验加 Ed Lessons），含提交状态 |
| `get_grades` | 成绩册、作业和测验的成绩与评语；可选 `refresh` |
| `list_posts` | Ed 帖子加 Moodle 论坛和公告，可按 course、category、type、staff、unanswered、mine、since 过滤 |
| `get_post` | 帖子全文和回复树（Markdown）；可选 `refresh` |
| `list_materials` | 课件和资源列表（Ed lessons/slides/resources，Moodle 文件/页面/链接），可按周、节、类型过滤 |
| `read_material` | 按页读提取出的文本（页范围或 offset + max_chars），同时返回本地路径 |
| `get_assessment` | 作业、测验或 Ed 课时的详情：要求、附件、截止、提交状态、反馈 |
| `search` | 全文搜索帖子、课件文本、页面和作业说明，返回片段和 id |
| `sync_start` / `sync_status` | 异步同步：立即返回 job_id，之后查询进度、计数和错误 |
| `get_status` | 凭证状态（不显示值）、上次成功同步、失败原因（例如 token 过期） |

通用约定（详见 [mcp-clients.md](research/mcp-clients.md)）：
- 结果是对象根的结构化输出，长 Markdown 放在字段里。每个结果约 8k tokens 以内，列表用 `limit` 和 `cursor` 分页，截断时明确标注。
- 所有工具标 `readOnlyHint`。结果带 `synced_at` / `stale`。读工具从不隐式触发同步。
- 资源 `lms://material/{id}`，供 Claude Code 用 @ 引用。提示 `weekly_digest`、`study_week` 在 Claude Code 里作为斜杠命令出现（Codex 不支持提示）。
- 服务器 instructions（1,500 字符以内）说明工作流：先查本地；数据过期时 `sync_start`；记得分页。
- 客户端配置：
  - Claude Code：`claude mcp add -s user lms -- "<exe>" mcp`
  - Codex：在 `config.toml` 里设 `tool_timeout_sec = 120`、`supports_parallel_tool_calls = true`。

## 3. 里程碑

### M0 计划与协作（本 PR）

交付 `docs/plan.md`、`docs/research/*`、`AGENTS.md`、`CLAUDE.md` 和 `.claude/agents/*`，不改产品代码。验收标准是用户审阅本计划并合并。

### M1 新骨架（能连上、能存储、能被调用）

1. 删除 v0.3.0 的同步、导出和存储代码（`internal/app` 里的 storage、sync、ed_lessons、ed_records、moodle_sync、setup，旧计划任务脚本）。保留可复用的 `internal/ed`、`internal/render`，以及 Moodle 的 Cookie 请求、HTML 发现和 iCal 解析代码，在 M1/M3 里改造。
2. 新建 `config`、`secrets`（`github.com/danieljoos/wincred`；没有凭据管理器时读环境变量；`ED_AUTH=proxy` 时 Ed 请求不带 Authorization 头，由云环境的 API credential 在代理层注入）、`httpx`（含只读守卫：Ed 只允许 GET；Moodle 只允许对 `/webservice/rest/server.php` 调用白名单函数和下载 pluginfile）。
3. `store`：建立 schema v1 和迁移框架，开启 WAL，实现租约。
4. 新增命令：
   - `auth ed`：隐藏输入粘贴 token，存进凭据管理器。
   - `auth moodle`：M1 只支持读取环境变量或代理注入的会话（云端开发用）。Windows 上用 Edge 配置文件登录放到 M3。
   - `auth status`：只显示是否已配置，不显示值。
   - `doctor`：检查凭证、连通性、身份和课程发现；Moodle 部分还要检查会话是否有效、能否拿到 sesskey、白名单里哪些 AJAX 函数可用，以及日历导出链接能否读取。
   - `capture`：采集脱敏样本，见 §5。
5. MCP 框架：工具注册约定、输出预算、cursor 编码、错误文案、同步任务框架（租约、goroutine、进度），以及 `list_courses`、`sync_start`、`sync_status`、`get_status`。
6. CI 精简为 ubuntu 和 windows 两个平台，Go 版本保留 1.25.x 和 stable。
7. 重写 README、`docs/architecture.md`，删掉 `docs/moodle.md` 等过时文档。

验收：
- 离线：gofmt、vet、test 全部通过；Windows 交叉编译成功；用内存传输的 MCP 集成测试检查工具列表、注解和输出结构。
- 云端真机：
  - Moodle 公开配置的探测结论写进 §4，只写结论，不写站点名。
  - `doctor` 对 Ed 和 Moodle 全部通过。Moodle 会话由云环境注入（D30）。
  - [ed-api.md](research/ed-api.md) 的六个未决问题有结论，并回写到那份笔记。
  - `capture` 生成的样本通过脱敏扫描，人工抽查后入库。

### M2 Ed 全量

- Ed 侧的课程发现与配对。
- Lessons：
  - 课时导出为 `lesson.md`（复用 render），下载 PDF 幻灯片和附件，视频只留链接。
  - 同步 quiz 题目、作答和已公布答案；challenge 描述；自己的提交和成绩（学生能读到的话）。
  - 课时的开放和截止时间写入 `deadlines`，成绩写入 `grades`。
- Resources：列表加下载。
- 讨论：增量加每日对账、详情、分类、公告、私密帖（D24）、作者显示规则（D20），以及检测「我的帖子」下的新回复。
- 事件：`staff_post`、`announcement`、`reply_to_me`、`new_material`、`material_changed`、`deadline_*`。
- MCP：`list_posts`、`get_post`、`list_materials`、`read_material`（先支持课时 Markdown 和纯文本）、`whats_new`、`upcoming_deadlines`（Ed 部分）、`get_assessment`（Ed 部分）。

验收：
- 在云端同步本学期全部 Ed 课程，与浏览器抽查对比课程数、课时数、帖子数和某个 PDF 幻灯片。
- 立刻再同步一次，应产生 0 个新事件。
- 每个工具的输出都在预算内。

### M3 Moodle 课件与资源

- Windows 上的 `auth moodle`：用 CDP 打开专用 Edge 配置文件的登录窗口，读取会话 cookie 存进凭据管理器；会话过期时先在后台静默续期，失败才通知你（D27、D29）。打预发布版 `v1.0.0-m3`，由你在 Windows 上验收这一项。
- Moodle 侧的课程发现与配对。
- 取各节和活动（具体接口见 §4.3）：
  - resource 和 folder 的文件做条件下载（流式、受大小上限约束、视频只留链接）。
  - page、book、label 转 Markdown，包括其中嵌入的文件。
  - url 只保留链接。
  - 下载活动简介里的附件。
  - 其他活动（assign、quiz、forum、lesson 等）先登记为条目，详情放到 M5。
- 增量：对比课程结构快照，再结合文件的 ETag 和 Last-Modified。按 D28，同一个活动的页面只在第一次发现时打开。
- MCP：`list_materials` 和 `read_material` 覆盖 Moodle，`whats_new` 加入 Moodle 资料事件。

验收：
- 文件清单与浏览器里的课程页逐节对照。
- 立刻再同步一次，应该没有重复下载。
- 修改过的文件能被检测到。

### M4 文档解析与全文搜索

- Spike：比较 PDF 提取方案，候选是 `klippa-app/go-pdfium` 的 WebAssembly 模式和纯 Go 库。用真实课件做样本（只临时下载到云端，不入库），比较提取质量、速度和二进制体积。
- 提取器：
  - PDF 按页。
  - PPTX 按页，含讲者备注。
  - DOCX、ipynb（按 cell）、HTML/Markdown、代码和纯文本。
  - 结果按内容哈希缓存。单个文件失败只记录，不阻塞同步。
- FTS5 索引覆盖帖子、课时、课件文本、Moodle 页面、论坛和作业说明。用 bm25 排序，带片段高亮。分词用 unicode61，必要时加 trigram。工具描述提示模型用课程原文语言（英文）的关键词搜索。
- MCP：新增 `search`；`read_material` 支持 PDF 和 PPTX 的页范围；新增资源 `lms://material/{id}`。

验收：
- 抽查 10 份真实课件的文本质量。
- 10 个典型查询都命中合理的结果。
- `read_material` 分页正确，且每次都在预算内。

### M5 Moodle 作业、测验、成绩、论坛、日历、站内通知

- 作业：要求、附件、截止和截止后宽限、提交状态、分数、评语和反馈文件。
- 测验：开放和关闭时间、尝试记录、最好成绩、反馈。按 D28，只在新出现或你问到时读测验页。
- 成绩册：逐项成绩、权重、评语和总评。按 D28，只在收到评分通知时或每天读一次。
- 论坛：公告论坛和课程论坛的讨论与帖子，作者显示规则同 D20。
- 日历和截止：action events 写入 `deadlines`。时区以站点和用户资料里的时区为准。
- 站内通知：只读。
- 事件：`new_grade`、`feedback`、`announcement`、`deadline_*`。
- MCP：`get_grades`、`upcoming_deadlines`（全量）、`get_assessment`（Moodle）、`list_posts` 和 `get_post`（Moodle 论坛）、`course_overview`。

验收：
- 与浏览器里的成绩页、作业页对照。
- 截止时间和时区都正确。
- 新成绩能产生事件。

### M6 后台同步、Windows 通知、安装与发布

- `lms-mcp schedule install|remove|status`：直接调用 `schtasks.exe` 或 Task Scheduler COM，不依赖 PowerShell 7。任务每小时运行，登录时也触发一次，只在用户登录时运行，窗口隐藏。
- 通知：
  - 先做 spike，确认纯 Go 的 toast 实现能在计划任务里弹出来（需要 AppUserModelID）。
  - 规则见 D9。一次同步产生的多条事件合并成一条通知，并去重；点击通知打开对应网页。
  - Moodle 会话过期、后台续期又失败时弹通知，点一下打开登录窗口（D29）。
- 发布：
  - CI 构建 Windows amd64/arm64 的 zip 和 checksums。
  - `install.ps1` 兼容 Windows PowerShell 5.1：下载最新版本、校验、安装到 `%LOCALAPPDATA%\Programs\lms-mcp` 并加入 PATH，然后运行 `lms-mcp setup`。
  - `setup` 是交互向导：选数据目录，填凭证，装计划任务，写好 Claude Code 和 Codex 的配置。
  - 升级就是重跑一遍安装脚本。
- 文档：中文 README，包括安装、首次设置、Moodle token 获取和常见问题。

验收（用户在自己的 Windows 上做）：
- 从零安装成功。
- Claude Code 和 Codex 都能调用工具。
- 计划任务每小时运行一次。
- 收到一条测试通知。

## 4. Moodle 访问方案（浏览器会话）

### 4.1 探测结论（2026-10-02）

- 学校的 Moodle 关闭了移动服务（`enablemobilewebservice=0`）。`typeoflogin=1`，通过 SAML2 登录，版本在 4.5 到 5.1 之间。学生拿不到任何 Web Service token，所以 [moodle-ws.md](research/moodle-ws.md) 里的 token 方案不可用。
- 改用浏览器会话：Moodle 的会话 cookie（`MoodleSession…`）加 `sesskey`，和你自己用浏览器访问是同一个身份。

### 4.2 会话从哪来

- **Windows（正式使用，M3 实现）**：
  - `lms-mcp auth moodle` 用 CDP 打开一个专用 Edge 配置文件（独立的 user-data-dir，不和日常浏览混用）的登录窗口。你完成学校统一登录和 MFA，最好勾选「保持登录」。工具读出会话 cookie 存进凭据管理器，然后关掉窗口。
  - 平时同步只用 cookie 发普通 HTTP 请求，不开浏览器。每小时一次的同步会顺便刷新 Moodle 会话的空闲计时。
  - 会话过期时（通常是电脑睡了一夜），先在后台无界面打开同一个配置文件静默续期；统一登录的会话还有效的话不需要你输入。续不上才弹通知，你点一下打开登录窗口（D29）。
- **云端（开发和验收，D30）**：
  - 在环境的 API credentials 里加一条：
    - **Allowed websites** 填 Moodle 域名。
    - **Custom headers** 的 Name 填 `Cookie`，清空 Prefix，Value 填 `MoodleSession…=<值>`。这个值从浏览器开发者工具 → Application → Cookies 里复制，找名字以 MoodleSession 开头的那条。
  - 环境变量里加 `MOODLE_AUTH=proxy`。
  - cookie 不进入云端机器。验收结束后在浏览器里点 Log out，cookie 立即失效。cookie 过期了就删掉这条 credential 重新加，正在运行的会话马上能用上。
- **日历保底（D31）**：在 Moodle 日历 → Import or export calendars → Export calendar 里，选所有课程和「Recent and next 60 days」，生成链接，放进 `MOODLE_ICAL_URL`（Windows 上存凭据管理器）。这个链接不依赖会话。

### 4.3 数据从哪里取（按访问记录从少到多）

1. **AJAX 接口**：`POST lib/ajax/service.php?sesskey=…`，只调用白名单里的只读函数。候选数据有课程列表、课程结构、日历事件、论坛讨论和帖子、站内通知、成绩。哪些函数可用、各有什么副作用，以 [moodle-session.md](research/moodle-session.md) 和 M1 实测为准。
2. **文件直链**：`pluginfile.php` 直接条件下载，不触发访问记录。
3. **日历导出链接**：截止日期。
4. **页面**（`mod/*/view.php`、成绩页等）：每打开一次就记一次查看，按 D28 尽量少读。从页面里拿到文件直链后记下来，以后直接用直链。

### 4.4 只读守卫（由 `internal/moodle` 强制执行）

- 只发 GET。唯一的例外是对 `lib/ajax/service.php` 的 POST，并且 `methodname` 必须在白名单里。白名单在 M1 根据调研和实测定稿，写进本节。
- 不提交任何表单。不跟随退出链接、带 `sesskey` 的链接，以及编辑、订阅、作答、提交类的 URL。
- 会打开的页面路径也设白名单，并记录每次打开的原因：首次发现、评分通知、每日检查，或你主动要求。这样可以核对 D28 有没有被遵守。

### 4.5 会话失效

- 被重定向到登录页或 SAML2，或者 AJAX 返回登录类错误，就判定会话已失效。
- 失效时：
  - 停止 Moodle 同步。Ed 照常同步，截止日期改由日历链接提供。
  - 把状态写进 `get_status`。
  - 按 §4.2 的顺序先尝试续期，续不上再通知你。

## 5. 测试与样本

- **离线测试**：用 `testdata/` 里的脱敏样本和 `httptest` 回环服务器。每个工具都有输出预算测试，估算 token 数必须在 8k 以内。每个 bug 修复都要配回归测试。
- **脱敏规则**（`internal/anonymize`，由 `capture` 使用）：
  - 所有 ID 重映射为稳定的小整数，并保持引用关系一致。
  - 所有自由文本（标题、正文、文件名、评语、课程名）替换为形状相同的合成文本：保留 Ed XML 和 HTML 的标签结构，保留长度量级和换行。
  - 姓名、邮箱、学号、头像 URL 换成合成值。URL 去掉 token 和 sesskey，主机改成 example 域名。
  - 所有时间整体平移。
  - 原始响应只在临时目录里，生成脱敏结果后立即删除。提交前用扫描器检查：已知姓名和邮箱、学号模式、类似 token 的长字符串。
- **真机验收**：每个里程碑结束时在云端跑一遍清单：doctor、同步一门课、抽查工具输出。结果写在 PR 描述里，只写计数和结论，不写真实内容。验收用的 token 用完即吊销（D14）。

## 6. 风险

| 风险 | 影响 | 对策 |
|---|---|---|
| 网页解析依赖主题和页面结构 | 学校改版后部分 Moodle 数据拿不到 | 优先用 AJAX 函数和文件直链；HTML 解析器配样本测试；`doctor` 能检测结构变化 |
| 几乎每天都要重新登录 | 后台同步每天可能中断一次 | 先在后台静默续期；续不上再通知，点一下就能登录；Ed 和日历保底不受影响 |
| 统一登录拦截自动化浏览器，或配置文件被占用 | 无法续期 | 用可见窗口登录；配置文件专用，不与日常浏览混用 |
| Moodle 会话 cookie 等同于账号权限 | 泄露风险 | 存凭据管理器；云端用代理注入；只读守卫禁止表单、退出链接和带 sesskey 的链接 |
| 会话绑定 IP（`tracksessionip`） | 云端用不了你浏览器里的 cookie | M1 第一件事就是验证；不行就只在 Windows 上做 Moodle 真机验收 |
| Ed API 处于 beta，可能随时变化 | 同步中断 | 宽松解析字段；`doctor` 能及时发现；用样本做回归测试 |
| Ed token 等同于密码 | 泄露后风险大 | 存凭据管理器；云端 token 用完吊销；日志脱敏；HTTP 层强制只读 |
| 读取帖子详情会被记为已读 | Ed 的未读状态失真 | 用户已接受（D7） |
| PDF 文本提取质量不好 | 「基于课件学习」效果差 | M4 先 spike；同时返回本地路径，让 Claude Code 直接读 PDF |
| 计划任务里弹不出 toast | 收不到通知 | M6 先 spike（AppUserModelID，只在登录时运行） |
| 多个进程同时访问数据库 | 锁冲突 | WAL、busy_timeout 加同步租约 |
| Cloudflare 拦截默认 User-Agent | 返回 403 | 发送自定义 UA |
| 课程资料版权和同学隐私 | 泄露 | 镜像只存在本机；仓库只放脱敏样本；同学匿名（D20） |

## 7. 工作方式

- 主会话负责架构、整合和审查。能独立完成的任务交给 `researcher` 或 `implementer` 子代理（Opus，high effort），交代时给出自洽的说明，包括目标、文件、约束和验收标准。新建的 agent 定义要到下一个会话才会加载；这之前用 general-purpose 加 `model: opus` 代替。
- 每个里程碑的流程：
  1. 在本文件里把任务拆细。
  2. 实现，可以按包切分给子代理并行做。
  3. 测试。
  4. 云端验收。
  5. 开 PR，用户合并。
  6. 更新进度表。
- 新会话开始时，依次读 `AGENTS.md`、本文件的进度表、当前里程碑那一节。

## 8. 用户待办（开始 M1 前）

1. ~~新建开发专用的 Ed token，放进云环境~~（已完成）。
2. Moodle 会话：按 §4.2，在 API credentials 里加 `Cookie`，并在环境变量里加 `MOODLE_AUTH=proxy`。
3. 日历链接：按 §4.2 生成，放进环境变量 `MOODLE_ICAL_URL`。
4. 云环境里不再需要 `MOODLE_TOKEN`，有的话删掉。
5. 合并 M0 PR，然后开新会话开始 M1。环境变量只在新会话里才会被读取。
