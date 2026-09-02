# Architecture

## 边界

本服务是独立部署单元，不链接、不打包也不修改 Android App。它只依赖 Cairn Share Worker 的三个内部 HTTP 操作：领取、完成和失败。Worker 是 D1 前面的唯一数据访问层，避免把 Cloudflare 控制面 D1 REST API 当作高频应用数据 API。

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
| `internal/config` | 环境变量解析及启动时校验 |

## LLM 契约

请求只使用 `POST /responses`，强制 `tool_choice=required` 和 `tools=[{"type":"x_search"}]`，并通过 strict JSON Schema 要求 `original_text`、`summary`、`related_links` 三个字段。提示词保持为一句，字段形状不在提示词里重复。

适配器白名单识别官方 `x_search_call`，同时兼容目标端点实测返回的 `x_thread_fetch`、`x_keyword_search`、`x_semantic_search`、`x_user_search` 自定义调用。没有搜索证据、没有且仅有一个输出块、结构不合法或 URL 不安全时，任务失败而不写入结果。
