# B02-T10 来源先存与个人编辑复验

日期：2026-09-29。对应 [Enricher #11](https://github.com/Alpenl/cairn-x-enricher/issues/11) B02-T10 和 [#16](https://github.com/Alpenl/cairn-x-enricher/issues/16) 的 source-first 组合。

| 要求 | 当前证据 |
|---|---|
| 阅读失败保留来源 | `TestLocalWorkerSourceLeaseAdmission` 在真实本地 Worker/D1 上先保存来源和证据，再让阅读结果校验失败。`GetSource` 与详情仍有原文；Worker 标记阅读付费结果未决，并拒绝按 ID 重领，避免第二次付费。进程内 `TestSourceSurvivesReadingFailureAndRetryDoesNotFetch` 覆盖可安全重试时复用已存来源。已结算且绑定响应 ID 的另一恢复边界由 `TestLocalWorkerProviderReadingRecovery` 覆盖。 |
| 分类失败不重抓 | `TestClassificationFailureDoesNotFailSourceJob` 在分类失败后检查来源、来源任务和失败计数；分类处理器没有来源读取器。真实本地半开故障用例在分类暂停时完成来源及阅读，二者不共用故障状态。 |
| 只改备注不重抓 | `TestLocalWorkerSourceLeaseAdmission` 在来源和阅读完成后，经 App `PATCH /api/links/:id` 只改备注；真实 Worker 回读显示 content revision、原文、译文及摘要不变，来源队列空，抓取和阅读计数均未增加。 |
| 人工原文受控写入 | `TestLocalWorkerManualSourceSurvivesProcessExit` 通过管理入口在返回 accepted 前写 Worker/D1，提交进程立即退出后，新进程仍能读取相同原文和内容版本、重放同一操作并领取任务。 |
| 阅读完成不写分类 | 真实 Worker/D1 完成阅读后的详情里 `classification` 仍为 null；改备注和 why/status 后也保持 null。 |
| why/status 保存零模型调用 | 通过真实 Go client 的 `UpdateCuration` 只送 why/status，随后回读值、来源和阅读内容；抓取、阅读计数没有增加，来源队列仍为空，未启动分类调用。 |

[最终来源租约联调日志](logs/20260929-source-first/source-lease.log)保留了真实本地 Worker/D1 与 Go 处理器的通过结果。其余两个本地用例在当前两仓工作树分别用 `CAIRN_INTEGRATION_CASE=manualrestart` 和 `CAIRN_INTEGRATION_CASE=providerrecovery` 通过；外部提供方均由本地夹具代替。`make verify` 覆盖处理器单测、race、lint、前端 488 项检查及构建；`make test-ablation` 通过。

阅读调用已结算但校验失败时，原文仍安全可读，但不能把“来源未丢”推断为“可以自动再付费”：没有可恢复的响应 ID 时任务继续保持未决，须走独立对账。此复验覆盖 B02-T10 的行为清单；#16 的全负载、旧新客户端和生产观察仍开放。没有真实付费请求、远端迁移或部署。
