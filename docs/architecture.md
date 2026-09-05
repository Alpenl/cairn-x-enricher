# Architecture

## 边界

本服务是独立部署单元，不链接、不打包也不修改 Android App。它通过 Cairn Share Worker 的内部 HTTP 操作读取收藏、领取任务、完成和上报失败。Worker 是 D1 前面的唯一数据访问层，避免把 Cloudflare 控制面 D1 REST API 当作高频应用数据 API。

## 一次任务的状态变化

```text
pending
  -> processing (attempt + 1, lease token, lease deadline)
  -> completed
  -> failed -> pending eligibility after next_retry_at
  -> exhausted after attempt 5
```

过期的 `processing` lease 可被重新领取。完成和失败写入均要求当前 lease token 匹配，因此旧实例不能覆盖新实例的结果。

## 组件

| 包 | 责任 |
| --- | --- |
| `internal/cairn` | 调用 Worker 内部队列 API |
| `internal/enrich` | xAI Responses 协议、Eino 工作流、严格输出校验 |
| `internal/processor` | 有界并发、批处理和失败上报 |
| `internal/health` | liveness、readiness 和最近一批状态 |
| `internal/dashboard` | 中文收藏列表、独立阅读页、同源查询/图片代理和有界人工处理队列 |
| `internal/config` | 环境变量解析及启动时校验 |

人工任务先按 ID 在 Worker 原子领取，再进入本机有界队列。定时任务与人工任务最终都通过 `processor.Process` 的共享信号量，因此总模型并发不会超过 `MAX_CONCURRENCY`。

## LLM 契约

请求只使用 `POST /responses`，强制 `tool_choice=required` 和 `tools=[{"type":"x_search"}]`。strict JSON Schema 要求模型一次返回 `ai_title`、`original_language`、`original_text`、`translated_text`、`summary`、`related_links` 和 `image_urls`；提示词明确原文保持原始语言、译文使用简体中文，标题约 20 个简体中文字符。

适配器白名单识别官方 `x_search_call`，同时兼容目标端点实测返回的 `x_thread_fetch`、`x_keyword_search`、`x_semantic_search`、`x_user_search` 自定义调用。没有搜索证据、没有且仅有一个输出块、结构不合法、标题不是合理长度的中文或 URL 不安全时，任务失败而不写入结果。

图片 URL 仅允许 `pbs.twimg.com/media`。模型结果通过校验后，Go 服务先让 Worker 在当前 lease 下抓取图片并写入 R2，再把 R2 对象引用随文本结果提交到 D1。Worker 完成事务前会确认引用对象存在；页面只能通过 Go 服务的同源 `/api/images/{key...}` 代理读取图片。

当 Worker 中的失败/耗尽记录已经有 `original_text`，或者后台人工提交了原帖正文，服务会跳过 `x_search`，只把可信原文交给模型补齐标题、语言、译文和摘要。这个路径仍使用同一套 strict JSON Schema 和结果校验，但不要求模型返回搜索证据，也不会接受模型新生成的图片 URL。
