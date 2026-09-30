# B02 / OBS：Go 日志输出隐私边界（2026-09-29）

## 问题与改动

Go 的异步 JSON 导出器只过滤属性，但先前仍允许 `link_id` 和符合 UUID 格式的 `request_id` 输出；二者能关联私人收藏，不能安全地进入无法按收藏删除记录的长期 stdout 采集后端。`slog.Record.Message` 与 `WithGroup` 的名称也未经检查。未配置观测控制文件时，`serve`、`once`、`classify` 使用普通 JSON handler，绕过了属性过滤。

现在异步与同步 JSON handler 共用消息、属性和分组过滤：稳定的 `link_id` / `request_id` 不输出；只有已审查的固定消息保持原文，其他消息归为 `application_event`；未知组名归为 `group`。固定阶段、事件名、状态、数量和安全错误类别继续可见。付费调用次数仍以持久业务账本为准，日志开关和丢弃不改变它。

## 验证与边界

- 新回归把私人 URL、来源 token、UUID、错误文本放入消息、组名和属性，分别验证同步与异步 JSON 输出没有原值；验证允许的事件和安全维度仍可读。
- `go test ./internal/observability ./cmd/cairn-x-enricher` 与完整 `make verify` 通过；后者包含 lint、race 测试、488 项前端检查和构建。全部使用本地夹具，付费调用 0。
- 本批只约束 Go JSON 日志 handler。命令顶层错误打印、Worker/Web/Android 输出、采集器实际保留与逐项删除、跨端关联和同负载开销仍要按 #11、#16、#20 的 OBS-01–05 验收。尚无获批部署后的观察期证据。
