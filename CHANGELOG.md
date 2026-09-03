# Changelog

本项目遵循 [Semantic Versioning](https://semver.org/)。

## [0.3.0]

- 将处理台升级为“收藏列表 + 独立阅读页”，列表适合筛选和批量操作，阅读页展示完整内容。
- 模型严格生成约 20 个简体中文字符的主标题；原有手动备注作为独立信息完整保留。
- 同时保存原始语言全文、原始语言标识、完整简体中文译文、中文摘要和内容相关链接。
- 识别 X 原帖及相关评论中的图片，将允许的 `pbs.twimg.com/media` 内容复制到 Cloudflare R2，再经 NAS 同源代理安全展示。
- 管理页展示 Cloudflare 中全部收藏；非 X 收藏归入“其他”且不会触发 X/LLM 处理。
- 列表页和阅读页都展示当前运行版本与提交号，并针对桌面和移动端优化阅读布局。

容器：`ghcr.io/alpenl/cairn-x-enricher:0.3.0`

## [0.2.0]

- 将状态页升级为中文 X 收藏处理台，展示收藏备注、原链接、处理状态、总结、相关链接和错误。
- 支持搜索、状态筛选、分页、完整原文展开，以及单条或最多 10 条批量立即处理/重新处理。
- 增加同源管理 API 和有界人工处理队列；人工任务与定时任务共用全局并发限制。
- 对接 Cloudflare 收藏列表、详情和按 ID 原子领取接口，浏览器不接触 Worker 或模型凭据。

容器：`ghcr.io/alpenl/cairn-x-enricher:0.2.0`

## [0.1.1]

- 增加内嵌的只读服务状态页，展示健康状态、最近批次统计、构建版本和错误摘要。
- 增加根页面安全响应头、精确路由和回归测试。
- 增加 Momax NAS 专用 Compose，通过 `8088` 提供局域网状态页且仅使用 Actions 发布镜像。

容器：`ghcr.io/alpenl/cairn-x-enricher:0.1.1`

## [0.1.0]

首个可运行版本：

- 新增定时和单次两种运行模式，从 Cairn Share Worker 原子领取 X 收藏。
- 使用 CloudWeGo Eino 工作流和 xAI Responses `x_search` 生成原文、简短总结及相关链接。
- 增加严格 JSON、搜索证据和 URL 校验，失败由 Worker 做有界退避重试。
- 提供非 root、无 shell Docker 镜像以及 `amd64`/`arm64` GHCR 发布。
- 提供 health/status 接口、结构化日志、race test、lint 和完整 GitHub Actions Release 流程。

容器：`ghcr.io/alpenl/cairn-x-enricher:0.1.0`
