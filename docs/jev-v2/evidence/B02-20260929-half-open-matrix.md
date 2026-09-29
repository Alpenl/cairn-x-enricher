# B02 R3-08 半开探测局部复验

日期：2026-09-29。对应 [Enricher #11](https://github.com/Alpenl/cairn-x-enricher/issues/11) 的 R3-08-验收、[#16](https://github.com/Alpenl/cairn-x-enricher/issues/16) 的故障组合矩阵。

## 覆盖的边界

| 场景 | 检查 |
|---|---|
| claim 返回 503 或网络中断 | 半开所有权释放，模型故障不误清除，下一次到期探测能恢复。 |
| 模型临时错误 | 本轮最多领取一个任务；下次到期可恢复。 |
| 探测前取消、领取中取消、maxJobs=0 | 领取中取消发生在取得半开所有权之后；返回后不留占用。随后可恢复。 |
| 空队列、正常成功 | 空队列不证明模型已恢复；成功模型调用才清除故障。 |
| 持续 401 | 退避期间不再领取；连续三个探测周期退避递增；来源抓取、阅读和完成继续运行，且不清除分类故障。 |

`internal/processor/stages_test.go` 的 `TestHalfOpenProbeAlwaysReleasesWithoutInventingRecovery`、`TestHalfOpenProbeReleasesAfterClaimIsCancelled` 和 `TestPersistentClassification401DoesNotStopSourceWork` 覆盖上表；既有 `TestCircuitBreakerProbeIsAtomic` 与 `TestCircuitBreakerBackoffGrowsAcrossFailedProbes` 验证单探测和递增退避。`cmd/cairn-x-enricher/scheduler_test.go` 的 `TestClassificationSchedulerRunsWhileSourceClaimIsBlocked` 验证独立调度的另一方向。

## 真实本地 Worker/D1 复验

`CAIRN_INTEGRATION_CASE=halfopen bash tests/local-integration/run.sh` 启动独立 Wrangler Worker、本地迁移后的 D1 和两个实际 Go `cairn.Client`/`Processor`。测试 `TestLocalWorkerHalfOpenClaimFaultsAndIndependentSource` 只在分类 claim HTTP 边界注入故障；target 握手、来源领取、付费阶段准入/结算、来源和阅读完成、分类成功提交均走真实 Worker。外部分类供应商使用本地契约夹具，无真实付费调用。

两个处理器各自先收到 HTTP 401，退避内重复调度没有再发 claim。其间一个处理器领取第二条收藏，真实完成来源抓取和阅读；D1 回读状态为 completed，分类模型调用仍为 0。30 秒退避到期后，一个处理器的 claim 收到 HTTP 503，另一个在 HTTP 传输层收到 `io.ErrUnexpectedEOF`；两者均未领取真实分类任务或调用模型，下一次退避增长到约 60 秒。退避到期后同时恢复，两者各领取一个真实分类任务、各提交一个 run，两个收藏均有持久 run，分类模型夹具调用恰为 2 次。测试用实际时间等待退避，耗时约 91 秒；[最终工作树日志](logs/20260929-half-open/worker-halfopen.log)。

503 和网络断开由 Go 客户端侧故障夹具注入，不能称为 Worker 自身故障。测试直接调用处理器轮次；后台调度器的独立性由上述 scheduler 回归证明，未把持续到达、积压和慢网络放在同一次生产负载测试中。#11 的 R3-08-验收各列举路径由进程内矩阵与这条真实本地 HTTP/D1 复验共同覆盖；#16 B10-T04/T11 的最终组合与性能验收仍开放。

## 运行

本轮执行 `make verify`：vet、golangci-lint、全包 race 测试、前端 488 项检查和构建通过。定向半开单测以 race 检测重复执行 5 次通过，真实本地 Worker/D1 的 `halfopen` 用例通过。没有调用真实供应商、迁移远端 D1 或部署。
