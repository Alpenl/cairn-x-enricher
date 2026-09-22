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

## NAS 私有图片缓存兼容（2026-09-23，代码准备，未部署）

- NAS 图片响应统一 `private, no-store`，包括旧 Worker 缺失缓存头或返回 public/immutable 的情况。图片 key 的哈希来自来源 URL，并不保证图片 bytes 永远不变。
- Go 图片客户端在取图前、取得响应后分别读取收藏详情，检查响应 ID 与图片所属 ID 一致；不存在、鉴权错误、后端故障或畸形详情均拒绝暴露图片，关闭已打开的响应。每次取图额外两次详情请求，不缓存存在性检查，不进行模型推断。旧 Worker 仍需保留原详情端点。
- 页面生成图片 URL 加 `?privacy=1`，绕开旧版本按原 URL 保存的长期缓存；新 URL 后续使用 no-store。旧版页面、已下载文件或用户手动访问的旧缓存 URL 无法由此次响应远程擦除；读取最终检查之后已开始发送的 bytes 也不可撤回。此项不承诺物理存储即时擦除，Worker 0026 与服务端清理机制仍需按授权部署。
- 验证：`make test-image-browser` 使用真实 Chrome、实际 Go 客户端/代理和旧 HTTP 合同夹具，验证正常缓存开启、旧 URL 命中、新 URL 避开旧缓存、同 URL 内容更新和删除后拒绝。`CAIRN_INTEGRATION_CASE=imageprivacy bash tests/local-integration/run.sh` 使用实际 Worker/D1/R2 + Go HTTP 生命周期；`CAIRN_SHARE_ROOT` 可指向配套 Share 工作树。两者均无收费模型调用。
- 语义依据：[RFC 9111 §5.2.2.5 no-store](https://www.rfc-editor.org/rfc/rfc9111.html#section-5.2.2.5)。新响应禁止后续存储，不等于撤回已经保存的副本。
