# Cairn X Enricher

一个独立于 Cairn Share App 的 Go 后台服务。Grok Responses API 和服务端 `x_search` 负责获取原帖，原文先保存到 D1；标题、译文和摘要由独立的阅读增强请求生成，相关图片复制到 R2。TypeSafe Jev 基于已存档原文，通过独立队列生成主题、形态和用途建议。

配套 Cairn Share App 可读取 AI 标题、双语正文、归档图片和人工整理结果。旧客户端默认的六字段响应仍兼容；新版 App 通过 `include=enrichment` 显式读取增强信息，继续使用独立的 App Token。

收藏管理支持固定词表分类、人工收藏原因、收件箱/精选/笔记/搁置状态、组合筛选及 Markdown 导出。实现依据的 [Grok 完整讨论与原始手册](docs/bookmark-management.md#讨论归档) 已保存到仓库，具体行为、词表维护和配套升级见 [收藏管理说明](docs/bookmark-management.md)。

## 数据流

```text
Cairn Share App -> 原有 Worker API -> D1 links
                                      |
                                      v
定时器 -> 原文任务 -> Grok x_search -> 保存原文快照
                                  |          |
                                  |          +-> 独立 Jev 队列 -> 自动分类建议
                                  +-> 阅读增强请求 -> 标题、译文、摘要、R2 图片

浏览器 -> NAS 收藏首页 -> 阅读页/搜索/后台重试 -> 同一处理流程
```

核心保证：

- Worker 用原子更新和 15 分钟 lease 分发任务，支持多实例并发而不重复领取。
- 成功结果只有在响应包含已完成的 X Search 证据且通过严格 JSON/URL 校验后才写入。
- 图片只接受 `https://pbs.twimg.com/media/...`，由 Worker 校验响应类型与大小后写入 R2，浏览器不接触 Cloudflare token。
- 失败由 Worker 按 `1m / 5m / 30m / 2h` 退避，最多尝试 5 次。
- 模型端点的临时 `408/429/5xx` 会在单次队列 attempt 内短重试；如果完整线程读取慢失败，会降级为只读原帖的结构化请求，避免上游抖动直接耗尽业务重试次数。
- 原文在阅读增强前保存。失败重试和普通重新处理复用快照；后台也支持粘贴原文后生成。
- 分类失败独立退避，不改变原文/阅读增强状态；旧分类租约不能覆盖更新后的输入。
- URL 或备注被 App 修改时，已有增强结果自动失效并重新入队。
- 日志不会输出 API key、完整提示词或模型响应。
- 标签只从 Worker 提供的版本化词表中选择，未知标签被丢弃并标记待确认；人工整理结果不会被重新处理覆盖。

## 流程设计

生产流程把获取、阅读增强与分类分开，并进一步把判断分为三个阶段：`Evaluate`（可联网，产生带完整分布的 typed raw judgments）、`Decide`（纯函数策略）、`Resolve`（纯函数人工覆盖解析）。Jev 对每个主题独立提出 Noul 问题，形态/用途使用 Choice，可选 Score 仅在有产品用途时启用；程序按版本化策略生成最终标签并保存原始分布，因此改变阈值可**零模型调用**重放。阅读使用独立的 `ReadingResult` schema，原文/链接/图片由程序从已存快照注入，模型从不回传原文。详细接口、状态、使用方式与限制见 [Jev 重构说明](docs/jev-classification.md)。旧单次生成器仅用于既有实验和基线回归；[简化与同步报告](docs/simplification-and-sync.md) 保留为历史记录。

分类目标由 Worker 侧的**权威目标**（不可变 spec + requested model + policy + 单调 generation）决定；消费者只声明能力，不能以自己的 policy 改写目标。扩展能力（实体、补证据、重排、词表提案）各自独立 flag 且**默认关闭**，可在 `GET /api/extensions` 查看。

## 配置

```bash
cp .env.example .env
chmod 600 .env
```

必须配置：

| 变量 | 含义 |
| --- | --- |
| `CAIRN_ENRICHER_TOKEN` | Worker 内部接口专用 Bearer token，不能复用 App token |
| `GROK_MODELS_BASE_URL` | Responses-compatible API 根地址，包含 `/v1` |
| `XAI_API_KEY` | 模型端点密钥 |
| `TYPESAFE_API_KEY` | Jev 分类密钥，仅服务端使用 |

其余变量及默认值均列在 `.env.example`。进程启动时会验证必填值、URL、数值范围和 duration 格式。

启动时还会做两项前置检查，任何一项失败都会让进程以非零码退出并在日志中给出原因：

1. 从 Worker 读取并校验版本化词表（需要配套的 curation 迁移和内部接口）。
2. 用一次极短的阅读增强请求做 Grok 契约自检，确认目标端点遵守 strict JSON Schema。Jev 密钥格式存在性在启动时检查，真实鉴权及 API 错误通过独立分类任务上报；不会在普通启动时发送 Jev 测试样本。

HTTP 服务在这两项检查通过后才开始监听，因此配置错误表现为“容器启动即退出 + 日志中的明确原因”，而不是一个长期返回 `503` 的半死进程。相反，运行期发生故障时 HTTP 服务仍会保持监听：`/healthz` 返回 `200`，`/readyz` 返回 `503` 并带上原因。

## 本地运行

```bash
go test ./...
make test-frontend   # 零依赖的前端逻辑检查，只需 Node
make test-browser    # 真实 Chrome 驱动的多维整理验收（21 项，无付费调用）
make ablation-architecture # 离线逐项移除行为，在临时副本运行回归
make verify          # vet + golangci-lint + 上述检查 + 构建
make test-ablation   # 离线重放已记录的消融结论，零模型调用
go run ./cmd/cairn-x-enricher once --max-jobs 10
go run ./cmd/cairn-x-enricher classify --max-jobs 10
go run ./cmd/cairn-x-enricher replay --id 12 --topic-accept 0.6  # 零调用重放旧判断
go run ./cmd/cairn-x-enricher refresh-source --id 12            # 显式重取原文
go run ./cmd/cairn-x-enricher serve
go run ./experiments/classification/main -dataset experiments/classification/testdata/synthetic-dataset.json
```

`classify` 只校验 Worker 与 Jev 配置，不要求未使用的 Grok 凭据；`serve` 按已启用组件校验。普通 `--help`、root 命令与所有 `make` 目标都不会产生付费调用。

`serve` 启动后立即执行一批任务，之后按 `POLL_INTERVAL` 运行，并在 `127.0.0.1:8080` 暴露：

- `/`：收藏首页，支持主题、形态、用途、来源、整理状态和时间筛选；搜索覆盖原文、译文、摘要、实体、备注及收藏原因，命中处高亮；当前加载结果可导出 Markdown。
- `/bookmarks/{id}`：阅读页，展示标题、图片、摘要和全文，原文默认收起；可确认分类、填写收藏原因、修改整理状态并导出单条收藏。
- `/backstage`：后台页，只展示服务状态和需要人工处理的失败收藏，平时不需要打开。
- `/api/backstage`：后台页使用的聚合状态，统一返回最近处理记录、失败计数和可手动重试条目。
- `/api/bookmarks`：处理台的同源收藏列表代理。
- `/api/bookmarks/{id}`：包含完整原文的单条详情。
- `/api/taxonomy`：当前标签词表的同源接口。
- `/api/bookmarks/{id}/curation`：通过 `PATCH` 保存人工整理，不调用模型。
- `/api/images/{key...}`：受控的 R2 图片同源代理。
- `/api/bookmarks/process`：提交最多 10 个收藏 ID 立即处理。
- `/api/bookmarks/{id}/source`：提交人工补充的原帖正文，绕过 X Search 直接生成标题、语言、译文和摘要。
- `/healthz`：进程存活。只要进程还在就不会失败，因此在 Worker 或模型不可达时也不会被重启策略反复杀死。
- `/readyz`：服务就绪。运行期被标记为降级时返回 `503`，响应体带上 `ready_reason` 与 `unhealthy_since`。
- `/status`：最近一批的匿名统计、错误状态、就绪原因和构建信息。

`once` 是适合 cron 和诊断的有界批处理命令，输出稳定 JSON；根命令不会隐式调用付费 API 或修改数据库。

`classify` 仅消费 Jev 队列，不调用 X Search 或生成阅读增强。`classify --id 123` 会先将指定的已有原文入队，再消费队列（可能包含其他待处理条目）。新抓取的原文自动入队；历史收藏不会全库回填。

## 消融实验

[消融实验](docs/ablation.md) 逐个移除流水线中的设计（strict JSON Schema、`x_search`、
线程读取、译文字段、分类词表、标题校验器、搜索证据门禁），用同一批已验证的真实帖子
对真实模型端点测量质量与成本变化。

结论摘要：

- strict JSON Schema 与 `x_search` 是**硬性前提**，移除后质量归零（前者输出不可解析，
  后者模型改写记忆而非读取原帖）。
- 在**真实收藏**上复测，线程评论读取对 14/17 条书签产生**逐字节相同**的原文，
  却多花约 25% token；价值在于它是少数帖子上的可靠性保险，而不是质量来源。
- 已有原文时 `source_only` 恢复路径质量 0.900 且比全流程便宜 30%。
- 结论由 `make test-ablation` 离线回归测试固定，不依赖付费调用。

实验代码与复现步骤见 [experiments/](experiments/README.md)。

## Docker

```bash
docker compose up -d --build
docker compose ps
curl -fsS http://127.0.0.1:8080/status
```

镜像是无 shell、非 root、只读文件系统的静态二进制，支持 `linux/amd64` 和 `linux/arm64`。正式版本发布到：

```text
ghcr.io/alpenl/cairn-x-enricher:<version>
```

完整部署顺序和 Cloudflare 前置改造见 [docs/deployment.md](docs/deployment.md) 与 [docs/cloudflare-backend.md](docs/cloudflare-backend.md)。
当前开发版需要配套 Worker 的全部迁移（截至 `0009_independent_classification.sql`）和新接口。迁移 0009 增加原文快照和分类任务表。先升级 Worker，再运行新版 Enricher；已有 App 协议保持兼容。不会自动回填历史收藏。修改代码不会自动升级 NAS 的固定版本镜像。
Momax NAS 使用 [deploy/nas/compose.yaml](deploy/nas/compose.yaml)，局域网阅读库映射到 `8088`；页面展示 Cloudflare 中全部收藏，只有 X 链接可以触发模型处理。旧版已完成记录会继续显示原内容，只有手动重新处理后才会生成新版标题、译文和图片。该清单只拉取 GitHub Actions 发布的镜像，不在 NAS 本地构建。

## 发布

所有构建都在 GitHub Actions 完成：

- 每次 push/PR：模块校验、静态检查、race test、二进制构建、双架构 Docker 构建。
- 推送 `vX.Y.Z` tag：GoReleaser 创建 GitHub Release，上传六个平台压缩包和 checksum，同时发布带 provenance/SBOM 的 GHCR 多架构镜像。
- Release 正文来自 `CHANGELOG.md` 对应版本，Action 和工具版本均固定。

## 安全

不要提交 `.env`。处理台不向浏览器发送 Worker token 或模型密钥，但会展示收藏内容并允许触发付费模型请求，因此端口 `8088` 只应开放在可信局域网，不应配置公网端口转发。任何曾出现在聊天、终端历史或日志中的 API key 都应立即轮换，再更新运行环境。漏洞报告流程见 [SECURITY.md](SECURITY.md)。

## License

[MIT](LICENSE)
