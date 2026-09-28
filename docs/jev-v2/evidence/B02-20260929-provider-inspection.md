# B02-T08：付费尝试只读核对（2026-09-29）

本批在 [Enricher #11](https://github.com/Alpenl/cairn-x-enricher/issues/11) 与 [Share #28](https://github.com/Alpenl/cairn-share/issues/28) 的逐次付费账本上，增加操作员只读检查。它帮助查清一个已有许可的状态，不会发新的模型 POST、结算未知许可、解冻收藏或退还预算。

## 操作边界

- Worker 的 `GET /api/enrichment/provider-attempts/inspect?operation_key=…` 仅接受与 App/Enricher 不同的 Operator Token。按主键读一条尝试，返回阶段、模型、状态、内容版本、可得的响应 ID/用量和当前未决标记；不返回原文、prompt、请求摘要或租约摘要。若已有人工“确认未计费”回执，显示 `confirmed_not_billed` 和证据类别。
- Go `provider-inspect --operation-key … [--response-id …]` 先读 Worker 账本。账本已有 response ID 时，命令只允许查询相同 ID；账本没有 ID 时，外部提供的 ID 只能得到 `unverified` 关联，不能凭模型名或时间自动绑定操作。命令只调用 xAI 的 `GET /v1/responses/{response_id}`，输出状态、模型、时间和可得用量；provider 的 input/output、备注和正文不进入报告。供应商模型与账本不一致会明确标记。
- 没有 ID 时不调用供应商。404 标为 `not_found_billing_unknown`，重定向、鉴权失败、超时、畸形或不匹配响应均不能当作“未计费”。所有报告的 `billing` 都是 `not_determined_by_lookup`。人工已确认未计费却查到响应时报告冲突，仍不自动改变业务状态。
- xAI [官方 Responses 文档](https://docs.x.ai/developers/rest-api-reference/inference/responses)列出此 GET，说明已保存响应默认保留 30 天。过期、未存储、供应商故障或没有 ID 都可能让查询缺失；GET 404 不构成未计费证据。

## 私有运行方式

操作员在受控终端为单次命令提供 `CAIRN_OPERATOR_TOKEN`，与运行中的 App/Enricher Token 保持不同。服务端环境还需已有 `CAIRN_API_BASE_URL`、`GROK_MODELS_BASE_URL` 和 `XAI_API_KEY`。不要把 token、命令输出或真实操作 ID 放入公开 issue/CI。命令只读，不提供“重试模型”选项。查询结果与供应商账单/支持材料应在私有位置核对，再走单独受控恢复流程。

## 验证与剩余工作

本地单测用 HTTP 夹具固定仅 GET、权限隔离、账本 ID 冲突时不访问供应商、私有正文不出现在 JSON、404/重定向不推断计费；Worker 测试检查许可删除与人工核对后的状态。真实本地 Worker/D1↔Go 通过 `providerledger` 场景：已结算尝试由操作员读出匹配的响应 ID，未知尝试读出 `reserved`、空响应 ID 和未决标记；Enricher Token 被拒绝，模拟供应商实际 POST 总数保持原有 3 次。Go `make verify`、Worker 362 项/类型检查/部署 dry-run 均通过。测试只使用本地模拟响应，没有访问真实 xAI 或迁移远端 D1。

这只是 B02-T08/OBS 的局部能力。已计费结果的安全落库恢复、无 ID 时的真实账单/支持证据、响应保留期内的实际账号查询、跨重启与新旧版本组合、同负载开销，以及获批部署后的观察期仍未完成；不据此勾选完整验收。
