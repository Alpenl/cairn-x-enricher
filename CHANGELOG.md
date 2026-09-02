# Changelog

本项目遵循 [Semantic Versioning](https://semver.org/)。

## [0.1.0]

首个可运行版本：

- 新增定时和单次两种运行模式，从 Cairn Share Worker 原子领取 X 收藏。
- 使用 CloudWeGo Eino 工作流和 xAI Responses `x_search` 生成原文、简短总结及相关链接。
- 增加严格 JSON、搜索证据和 URL 校验，失败由 Worker 做有界退避重试。
- 提供非 root、无 shell Docker 镜像以及 `amd64`/`arm64` GHCR 发布。
- 提供 health/status 接口、结构化日志、race test、lint 和完整 GitHub Actions Release 流程。

容器：`ghcr.io/alpenl/cairn-x-enricher:0.1.0`
