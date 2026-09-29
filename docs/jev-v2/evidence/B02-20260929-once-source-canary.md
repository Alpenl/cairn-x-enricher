# `once` 空来源队列的 canary 边界（2026-09-29）

## 触发和实现

旧 `once` 在构建处理器时无条件发一次 Grok 阅读 canary，即使来源队列为空且只有已保存来源的分类任务。现在它先向 Worker 查询一次只读的 `source-claimable`；这个查询与实际定时领取共用候选 SQL 和参数。返回 `false` 时，本次只跑分类和证据恢复，不领取来源租约，也不要求 Grok 配置。查询出错或 Worker 不支持该接口时，退回原来的完整配置和启动 canary。返回 `true` 时，在领取任何来源任务前检查 Grok 配置和 canary。`serve` 始终走完整启动检查。

预查是一次性快照。若新来源任务在 `false` 响应之后到达，本次 `once` 不领取它，由下一次调度处理；这样不会发生“跳过 canary 后仍领取来源”的竞争。

## 可重复验证

使用真实本地 Wrangler Worker、迁移后的本地 D1、编译后的 Go CLI 和两个本地供应商 HTTP 夹具；没有真实付费调用。

```sh
CAIRN_SHARE_ROOT=/path/to/cairn-share \
  CAIRN_INTEGRATION_CASE=onceempty bash tests/local-integration/run.sh
```

`TestLocalWorkerOnceSkipsEmptySourceCanary` 证明：

1. 空来源队列、未配置 Grok key：`once --max-jobs 1` 成功，Grok 请求数为 0。
2. 已落库来源待分类、来源队列仍不可领取：分类 run 成功落库，Grok 请求数仍为 0。
3. 新来源待领取、未配置 Grok key：命令失败，Grok 请求数为 0。
4. 新来源待领取、合成 Grok canary 返回 422：仅有 1 次 canary 请求；来源仍 `pending`，尝试次数为 0。

Worker 的 `source-lease-admit` 测试还确认：只读预查使用内部令牌，不获取租约；已领且未过期的任务返回不可领，过期可重新领取的任务返回可领，付费结果未决的任务返回不可领。`claim-plan` 测试直接对生产候选 SQL 在 2,000 条合成收藏上执行查询计划与读取量检查，非空和空队列的 `rows_read` 都小于 20。Go 客户端单元测试要求认证、严格布尔响应，旧 Worker 的 404 不会被误认作空队列。处理器单元测试证明仅分类轮次不会领取随后出现的来源任务。

最终检查：Worker 38 个测试文件、386 项测试，`npm run typecheck` 和 `npm run deploy:dry-run` 通过；Go `make verify` 与 `make test-ablation` 通过。以上为本地夹具证据，未部署，未触发真实 Grok 或 TypeSafe 付费调用。#11 B02-T09 中组件暂停等其余验收仍未完成。
