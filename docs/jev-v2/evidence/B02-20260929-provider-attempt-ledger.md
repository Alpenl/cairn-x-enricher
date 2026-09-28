# B02-T08：来源与阅读付费尝试的持久凭证

日期：2026-09-29。关联 [Enricher #11](https://github.com/Alpenl/cairn-x-enricher/issues/11)、[Share #28](https://github.com/Alpenl/cairn-share/issues/28)、[验收 #16](https://github.com/Alpenl/cairn-x-enricher/issues/16)。实现位于 Draft PR #17，配套 Worker 位于 Draft PR #32。

## 本地已实现

- `ResponsesClient` 每次 xAI POST 前，先向 Worker 申请单次发送凭证。凭证绑定租约、权威内容版本、阶段、提示词变体、尝试序号、请求摘要和模型；同一 operation 的重放不能再次发送。来源的第二个提示词仅在第一响应已确定、已结算且内容不可用时显式授权。HTTP/网络错误不自动重发 POST，也不改用第二个提示词。
- 收到可解析的 HTTP 200 后，在消费结果前保存响应 ID 和实际返回的 token、X Search、美元 ticks；非 200 保存状态。响应丢失或结算失败保留未决凭证。结算用脱离已取消任务的 15 秒有界上下文。启动阅读 canary 也领取独立限额内的持久凭证。
- Worker 预算拒绝发生在本阶段任何凭证创建前时，Go 用独立的有界请求退还租约与队列尝试，并等下一 UTC 预算窗口；若已有凭证，Worker 拒绝退还。人工粘贴正文走无付费来源写入，阅读阶段仍须单独取凭证。
- 新 Go 要求 Worker 握手声明 `provider_attempt_ledger`；旧 Go 无法新领任务或新准入付费阶段。升级前仍须停机并排空旧 Go 已领租约。

## 验证

- `make verify`：vet、lint、全量 race、457/457 前端检查、构建均通过。
- 本地真实 Worker/D1 联调：`providerledger`、`sourcelease`、`lifecycle` 均通过；xAI 由本地 HTTP 夹具替代，实际付费调用为 0。
- 定向测试覆盖凭证先于 POST、同键重放不再次发送、已知响应用量结算、响应丢失不降级、预算拒绝不报模型失败、任务上下文取消后仍退还未用租约，以及人工正文不占来源付费准入。

## 尚未验收

- 管理端已有私有列表与汇总查询，但尚无带审计的人工作废/恢复动作，也没有供应商 response ID 的实际查询对账路径；未知结果必须继续暂停，不能自动再付费。
- 每次付费尝试的可开关安全业务事件、跨端关联、指标和链路仍属于 OBS-02/03；持久凭证不受日志开关影响。需要同负载测量新增 D1 查询、延迟和预算拒绝率。
- 未经授权未运行生产迁移、部署、真实供应商调用或观察期；B02-T08、OBS 和最终组合验收保持未勾选。
