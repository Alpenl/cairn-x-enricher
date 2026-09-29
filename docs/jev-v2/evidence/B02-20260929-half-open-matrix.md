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

这些是进程内受控队列和模型夹具测试。503/网络错误由夹具注入已分类错误；尚未把真实 Go HTTP client、Worker/D1、两个调度器及慢网络放在同一故障场景中。因此本证据只补齐半开生命周期的可重复回归，不把 #11 R3-08-验收或 #16 的组合验收标为完成。

## 运行

在本提交工作树执行 `make verify`：vet、golangci-lint、全包 race 测试、前端 488 项检查和构建通过。定向半开测试以 race 检测重复执行 5 次通过。没有调用真实供应商、迁移远端 D1 或部署。
