# B02-T05 / B01-T02：权威目标与消费者规格握手

日期：2026-09-29。范围：本地 Go CLI、Worker/D1 和合成供应商夹具；没有真实付费调用、远端迁移或部署。

## 发现与修复

- Worker 原来允许激活未注册的 v2 `spec_id/spec_hash`，`supported` 和 claim 只核对规格 ID 等能力，不核对已注册的 hash。这会让任务先领租约，再在 run 提交时失败。现在激活前检查已注册规格及 hash；读取目标和领取时也识别既存坏目标，拒绝领取。
- Go 原来相信 Worker 的 `supported`，未核对本地编译规格的 hash。`classify --id` 还在目标检查前重排任务。现在成功注册规格后保留本地 hash；各分类入口在领取前核对协议、规格、hash、词表、政策和模型。`classify --id` 先核对再重排。
- 握手到领取之间若目标切换，Go 在 claim 中提交 `expected_generation`，Worker 在领取前拒绝旧 generation。Worker 原有原子 UPDATE 仍检查目标指针；回包也由 Go 核对绑定的 generation 和规格。

## 验证

- Worker `npm test`：39 文件、393 项通过；`npm run typecheck` 与 `npm run deploy:dry-run` 通过。新增回归覆盖未注册/错误 hash 不能激活、历史坏目标令握手 unsupported 且不领取、握手后切目标不消耗 attempt。
- Go `make verify`：vet、lint、race、前端 488 项检查和构建通过；`make test-ablation` 通过。单测核对未注册或不同 hash 的本地规格在 claim 前暂停，匹配时 claim 携带 generation。
- 真实本地 Worker/D1：`CAIRN_INTEGRATION_CASE=cli` 验证编译后的 `classify --id` 在不兼容目标下不改 classification job、不请求模型；恢复正确目标后正常分类和显式重试。`onceempty` 验证保存来源在不兼容目标下不消耗 attempt，恢复后可继续。`competition` 验证先注册规格后的版本竞争；`lifecycle` 和 `manualrestart` 复验已有跨阶段及人工来源恢复场景。
- `CAIRN_INTEGRATION_CASE=servetarget` 让真实编译后的 `serve` 接受已持久化的人工原文，然后持续运行：目标模型不兼容时，分类任务前后状态完全相同，TypeSafe 请求为 0；切回兼容目标后，保持既定约 30 秒退避，到期的下一轮只调用一次本地模型夹具，保存匹配的 spec ID/hash 与原文。Grok 只执行一次启动 canary，受控持有的来源阅读租约没有被重新领取。该用例约 31 秒通过。最初夹具沿用旧阅读输出，带 `original_text` 时被严格 decoder 正确拒绝；改为当前仅含阅读字段的合同后通过。

## 仍需组合验收

本证据覆盖 B02-T05 原任务列出的 `serve`、`once`、`classify`、人工来源、目标权威性和不消耗 attempt 的本地路径，可签收该子项。新旧 Worker/Go 版本组合、生产数据迁移和长时间负载尚未覆盖；B01-T02/T03 的全合同及 #16 跨端总验收仍开放。
