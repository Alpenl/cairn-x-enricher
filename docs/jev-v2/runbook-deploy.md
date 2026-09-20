# 部署与受控回填 Runbook（仅准备，不执行）

本文件是 B10-T10 的**准备**产物。任何步骤在获得所有者明确授权前都不得执行。

## 版本兼容表

| 组件 | 兼容旧 Worker | 兼容新 Worker |
| --- | --- | --- |
| 旧六字段 App | 是 | 是（v1 投影稳定） |
| 旧 enrichment v1 | 是 | 是（v1 写入保留隐藏 v2） |
| 新 v2 App/Web | 否（`available:false` 只读降级） | 是 |
| Go classify | generation 0 legacy target | v2 target（能力握手） |

## 部署顺序

1. **先做 Worker + 迁移**（仅在授权后）：
   - 备份 D1 快照并验证可恢复；
   - 应用 `0010`、`0011`、`0012`（仅新增，非破坏性）；
   - 验证 `classification_target_state.generation=0`、`protocol=legacy`；
   - 验证旧消费者 claim 仍返回任务。
2. **再上 Go/Web**：部署新 enricher 镜像；确认 `GET /api/extensions` 全 off；`make verify` 通过。
3. **Android**：仅在获批准后签名发布；不自动推送应用商店。
4. **最后（单独授权）切换 spec/flags**：
   - `POST /api/enrichment/classifications/target` 带 `expected_generation`；
   - 灰度限定 job 显式目标；
   - 观察指标达标后再扩大。

## 目标 generation 回滚

- 使用**新 generation** 指向旧 spec（`spec_hash` 相同则视为 unchanged）。
- 永不倒退 generation；旧 run 保留为审计。

## 观察指标与停止阈值

| 指标 | 停止阈值 |
| --- | --- |
| `capability_mismatch` 比例 | >10% 任务 |
| `target_changed`/`input_changed` 比例 | >20% 任务 |
| accepted error（人工抽查） | >10% |
| 单条模型调用 | >预算上限 |
| `/readyz` 连续 503 | >5 分钟 |

任一超阈值：停止扩大、关闭对应 flag、回滚到旧 generation。

## 受控回填范围与预算

- 默认**不**全库回填。
- 仅在授权后以显式 ID 列表或 cursor 分批，`--dry-run` 先出计划；
- 每批有 ID 上限、调用/token 预算、取消与耗尽行为；
- 禁止隐式全库任务。

## 明确需要的授权

- 远端 D1 迁移：单独授权
- 收费模型调用/回填：单独授权
- NAS 镜像/生产切换：单独授权
- Android 签名发布：单独授权

以上任一未授权时保持“代码可审查、未部署”状态，不伪称已上线。
