# R3-06 / 2026-09-22 补材料所有权、检查点与原子恢复

总控 [E #10](https://github.com/Alpenl/cairn-x-enricher/issues/10)，主责 [E #15](https://github.com/Alpenl/cairn-x-enricher/issues/15)，存储 [S #29](https://github.com/Alpenl/cairn-share/issues/29)，分类 [E #12](https://github.com/Alpenl/cairn-x-enricher/issues/12)，统一验收 [E #16](https://github.com/Alpenl/cairn-x-enricher/issues/16)。本报告是 R3-06 局部工程证据，不代表全部 B09 或原 R/B/SC、质量门槛通过。

## 受测版本与修复

Share `babe6bb5ed01efef6d5c10da52e54afcad2cd9db`；Enricher `c470d90c0bee57e28ef08e9bd17a625482290345`。PR 仍为 Draft [S #32](https://github.com/Alpenl/cairn-share/pull/32) / [E #17](https://github.com/Alpenl/cairn-x-enricher/pull/17)，报告是随后单独的文档提交，最终 HEAD 的远端 CI 在 #16 登记。

原流程 Go 严格 DTO 不识别 Worker 的 `replayed`；多个消费者都把 pending 当作执行许可。抓取后分别保存 snapshot、决定 request、retry queue，任一步失败都可能重复抓取或丢失入队。追加材料又从 plain text 重建，丢弃原始归档块；外链识别依赖 ID 前缀，去重键使用 InputRevision。

新迁移 **0022_evidence_request_execution.sql** 为请求增加 protocol、不可变输入/目标/URL/预算身份、owner token、lease、attempts、checkpoint/hash 和 receipt。旧行保持 protocol 0，不推定曾有所有者，不自动抓取。旧元数据接口兼容，但不能绕过 protocol 1 的领取/检查点/提交。

1. **登记意图**：实际 snapshot ID/hash/ContentRevision、target generation、来源已保存的 URL、预算一起绑定 dedupe。并发新建只有一条；相同 key 的身份或预算变化冲突。Go 正式 DTO 解码新建及重复响应，`replayed` 不等于执行权。
2. **领取执行权**：120 秒 owner lease；只有 owned=true 的消费者可抓取。重复同 token 确认原领取，其他消费者读不到 token；每请求最多两次保留尝试，过期和重启不重置预算。Go 每次轮询按 maxJobs、20 和配置总调用预算取最小值，取回最多 20 条待恢复任务；外链字节/超时上限分别为 2 MiB/30 秒，实际默认 2 MiB/15 秒，沿用消费者的 allowlist/重定向/网络控制。共享 HTTP 客户端先复制再设置重定向策略，避免并发改写。
3. **持久检查点**：保存有限的抓取结果及 hash；相同 owner/payload 的重复响应安全确认，异 owner、过期 owner 或异 payload 拒绝。`truncated=false` 显式发送，不依赖省略字段猜测。
4. **原子应用**：同一 D1 batch 取得内部 application marker，追加 snapshot、推进 content revision、重新入队并保存回执。最终输入/目标失效不写；活跃分类让检查点暂缓；SQL 失败全部回滚。相同 finalize 返回原回执，不重复入队。相同已归档外链材料无变化，容量不足明确 blocked，均不改变旧材料。
5. **新进程恢复**：在普通分类轮询入口处理 protocol 1 的 pending、过期 owned 和 checkpointed。已有检查点不重新 fetch，即使当前没有分类任务也可恢复原子应用。禁用 evidence flag 时不登记、不领取、不抓取。

追加以不可变原始 snapshot 的所有块为基础，保留 ID/role/text/URL/relation/acquired；新增块明确 external_article 和目标 URL。objective archive 原本排除 fetched_at，恢复不补造抓取时间。缺 URL 的旧块不能证明某条外链已抓取；gap 按 role+确切 URL 判断，不再使用 ID 前缀。成功添加的新输入进入下次正常领取和实际有界 provider state，原正文、阅读结果、人工覆盖继续保留。

**故障保证边界**：抓取完成但尚未形成检查点时，任意进程崩溃无法保证外部 HTTP exactly once；新 owner 最多使用余下的一次保留尝试。检查点之后，响应丢失、提交失败和新进程恢复都不需要再抓取。记录这一边界，不把有限重复可能性冒充完全零重复。

## 真实验收

新增 Worker `evidence-execution.test.ts` **11 项真实 D1 测试**：并发登记/领取、同 token 重放、owner 隔离、异 payload/预算/输入、未保存 URL、权限/上限、最终 preflight 后 content/target/active job 变化、SQL 失败全回滚、精确 receipt、两次尝试耗尽、已归档 no-op、自定义块 ID、旧库迁移及旧 pending 不自动执行、64-block 容量终止、删除级联。R3-12 的迁移测试改为固定 0021 边界，防止新迁移让历史验收误测成“最后一项”。

新增 `TestLocalWorkerEvidenceExecutionRecovery` 启动真实 Worker/D1/R2，调用正常 Go `NewStaged`、严格 Cairn client 和 classifier；只有外部服务由本地 HTTP 夹具代替：

| 时序 | 实际结果 |
|---|---|
| 首次正常分类、发现已保存外链 | 1 次模型合同调用；保存原分类后登记/领取补材料 |
| 外链响应故意等待；另一消费者重复登记、领取和普通轮询 | 严格解码 replayed=true/status=fetching；owned=false，尝试次数仍 1；总外链 fetch 为 1 |
| 首次 checkpoint 已成功但 HTTP 响应丢失 | 精确同 payload 再次确认，共 2 次 checkpoint 请求，不重复抓取 |
| finalize 连续 503 | 恰好 3 次有限重试，原分类仍成功；数据库保留 checkpointed，未单独改 source 或入队 |
| 新 Processor、无进程内结果 | 从数据库恢复完成追加和入队，再正常分类；外链 fetch 仍 1，模型合同总调用 2 |
| 读取实际 provider HTTP state | 第一次无新材料，第二次含唯一新增外链文字及 external_article 角色 |
| 比较实际入库前后的 archive | 原先 3 个自定义 ID/角色/关系块逐字段相同，只追加第 4 块 |
| 第三次无变化轮询 | 完成/失败均 0，source/queue 状态不变，fetch=1、模型调用=2，恢复队列为空 |

额外以 `GOFLAGS=-race CAIRN_INTEGRATION_CASE=escalation bash tests/local-integration/run.sh` 执行该真实并发场景，exit 0；不是把没有 Worker 环境而 skip 的包测试计作运行。本轮 **0 次付费模型调用**。

| 检查 | 结果 |
|---|---|
| Share `npm test` | 177/177，通过 |
| Share typecheck / deploy:dry-run | exit 0；仅构建 |
| Enricher `make verify` / `make test-ablation` | exit 0，包含全包 race/lint、73 前端检查和生产构建 |
| `tests/local-integration/run.sh` | exit 0；8 个真实跨仓场景 |
| 新补材料真实场景 `GOFLAGS=-race` | exit 0，无数据竞争 |

日志：[Worker](logs/20260922-escalation/worker-verify.log)、[Enricher](logs/20260922-escalation/enricher-verify.log)、[8 项集成](logs/20260922-escalation/integration.log)、[补材料真实 race](logs/20260922-escalation/escalation-race.log)。

开发过程中真实联调发现 archived snapshot 不含 fetched_at、Go omitempty 省略 false 导致 checkpoint 拒绝，修复实际合同并复跑；测试中的 quote 枚举、权限状态码改为现有 quoted/401 合同；新增导出 API 注释满足原 lint，未关闭规则或放宽来源校验。

## 范围与后续

本批对应 R3-06 / R2-07、B09-T05/T06、B03 扩展存储/幂等/恢复及 SC18/19/30 的局部工程证据。B09 整体全局预算/质量/收益、R3-07 Android 离线队列、B08 完整校准/消融/固定候选检索/留出集、所有原 R/B/SC 审计和独立验收仍须完成。自动参考无需人工标注；工程通过不表示质量门槛已过。

扩展依然默认 off，迁移仅本地；未进行生产迁移、部署、合并或新的付费评估。旧请求不会静默升级执行，新版 Go 与 Worker 协议需配套部署；旧 Worker 不支持新端点时显式记录扩展错误并保留已成功分类。所有原任务清单和历史证据保留，最终验收统一 #16。
