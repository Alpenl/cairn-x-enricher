# 2026-09-23 普通分类持久调用预算

源码 S `7f02a92d3970c04b87c05d63e714f8476c15f6c3` / E `614269208c57a7793952f41f549bd2db3e19daeb`；[Share Draft PR32](https://github.com/Alpenl/cairn-share/pull/32)、[Enricher Draft PR17](https://github.com/Alpenl/cairn-x-enricher/pull/17)。总控 [#10](https://github.com/Alpenl/cairn-x-enricher/issues/10)，统一验收 [#16](https://github.com/Alpenl/cairn-x-enricher/issues/16)。本批推进 B04-T14、SC30 和审计 G01；不代表整个 B04 或全部任务接受。

## 实现与边界

此前已有单 job deadline、串行分类 worker、max-jobs、五次 attempt 与组件暂停；缺口是跨进程的普通分类实际模型调用预算。现在 `serve` / `once` / `classify` 生产工厂均接入 Worker/D1 持久授权。每个实际 provider HTTP 请求前申请一次，全量、部分重用与每个拆批使用同一入口。已领取任务完全复用旧判断、纯策略 replay 不申请额度。

预算范围是一套 Worker 部署和 UTC 自然日，全局 20 次 / 逐条 5 次。固定模型 `jev-1.13.0`，每次保守预留 65536 input tokens，全局 1310720 / 逐条 327680；数值不是实际 usage 或 tokenizer 估算。实际响应 usage 仍另存。依据 2026-09-23 查询的 [TypeSafe 模型说明](https://docs.typesafe.ai/models)及 [API](https://docs.typesafe.ai/api)，不将字符数冒充 token 数。四项环境配置只允许收紧，详见[运行合同](../../jev-classification.md#持久调用预算)。客户端较低设置约束自己的请求，Worker 硬上限约束全部消费者。它与默认关闭的扩展 20/2 额度独立，不能称为整个 TypeSafe 账户总额度或跨部署总额度；午夜是重置边界，非滑动窗口。

内部授权只接受 enricher token，校验固定模型、额度、实际序列化请求 hash。原子 SQL 同时验证有效 lease、job/input/content revision、spec、当前 target generation 和 canonical evidence snapshot ID/hash，然后写匿名全局与逐条账本。不存在或变化的材料不收费；并发最多在剩余额度内授予。重复随机 key 不重授，异 payload 冲突。授权确认丢失、错误或未知时停止模型调用，不重试授权、不退款；后续 provider 失败也不退款。全局记录没有收藏 ID、lease、原文或实际 provider 请求正文，删除收藏不返还全局消费。复用现有 budget_ledger 与 0026 删除守卫，没有新增迁移。

全局耗尽在 claim 前及最终 SQL 内守卫，逐条耗尽在候选查询跳过，不继续消耗 attempt。组件保留现有有界退避探测；恢复时仍先由后端检查预算。全局耗尽也不会额外领取可能复用的任务。拆批中途遇到预算、stale、组件错误或取消时停止后续请求，已实际调用仍记账，缺失问题标记 partial；拒绝于调用前不伪造 provider 调用记录。既有已领取任务的有界取消后收尾机制保留，并非即时撤回已经发送的请求。

## 兼容与升级

请求和 target 握手响应都需 `X-Cairn-Classification-Budget: 1`。新 Worker 拒绝没有协议声明的旧消费者 claim，attempt 不变；配置预算的新消费者面对缺少响应声明的旧 Worker 在 claim 前暂停。HTTP body 保持原握手字段；读取 App 和旧 enrichment 协议不受这项分类门禁影响。

先停止并排空旧分类消费者，再部署已具备现有迁移及端点的 Worker，随后升级消费者并按 B01 受控更新匹配目标。默认模型由 alias 改为固定版本；显式旧 alias 在启动网络操作前被拒绝。本批未自动激活新 target。升级前已签发 lease 不可能由新预算追溯计量，不能声称兼容旧二进制继续付费。回滚须停止无预算分类路径并保留账本/兼容 Worker，关闭扩展 flag 不会关闭普通分类预算。

本地研究/离线测试库可不配置此 store；生产两处工厂强制配置。普通分类调用不被扩展的 Judge 双重计费。文档已去除过时的 alias 默认、三主题截断描述、备注使原文失效、classify 强制 Grok 配置及人工 gold 前置要求；不改写历史实验结论。

## 验证

| 证据 | 准确范围 |
| --- | --- |
| [Worker 最终全量](logs/20260923-classification-budget/classify-budget-worker-final.log) | 21 文件 247 项、typecheck、deploy --dry-run 全通过。新增 9 项覆盖并发、每日重置、四种限额、重放冲突、lease/输入/目标/材料身份、事务最后窗口、删除、旧消费者、App 鉴权与 body 上限。workerd 的 deleteAllDurableObjects 清理日志未使测试失败，终态 exit 0。 |
| [Go 完整门禁](logs/20260923-classification-budget/classify-budget-verify-final.log) | vet / golangci-lint / race / 73 前端检查 / build 全通过。实际本地 HTTP 单测覆盖拆批逐次计费、完整重用零调用、单问题部分重用、丢授权/缺 lease/取消零请求、固定模型及配置校验、新消费者遇旧 Worker 不领取。 |
| [全部实际服务](logs/20260923-classification-budget/classify-budget-real-final.log) | 16 个独立 Worker/D1/R2 + Go 场景全部通过；含实际 CLI 二进制与新普通预算五进程场景。原服务场景保留其各自离线配置，不冒称全部启用新预算。 |
| [实际浏览器](logs/20260923-classification-budget/classify-budget-browser-pass.log) | 实际 Chrome + Go serve + Worker/D1/R2，40/40。默认生产工厂、固定目标与模型、完整 v2 run、人工拒绝/重置、筛选、实体和导出贯通。只有付费提供方是本地 HTTP 夹具。 |

新增场景运行五个独立 Go 处理器进程，实际 queue/client/processor/classifier 和独立 wrangler/D1，未 mock 预算仓库。前两次真实本地 provider HTTP 完成同条目两次分类；第三进程因逐条上限不领取；第四进程在 Worker 已持久授权后故意丢失响应，模型请求数保持 2；删除前一条再建新条，第五进程因全局上限在领取前暂停，新条 attempt=0/pending。证明重启和删除不能退回全局额度，合计 **2 次本地模拟 HTTP、0 次付费**。该数仅指新增五进程场景，不是所有 16 组夹具调用总数。

完整浏览器和 CLI 成功证明入口接线，不能证明真实模型准确率、p95、单日运行可靠性或整库成本。本地 buildinfo 为父提交 `7aefe95`，测试执行的是带本批改动的工作区。没有重跑 Android 设备；Share 常规 CI 也不能替代设备验收。

源码 CI：[S 35796016556](https://github.com/Alpenl/cairn-share/actions/runs/35796016556) 与 [E 35796319168](https://github.com/Alpenl/cairn-x-enricher/actions/runs/35796319168) 均在本文完整源码 SHA 成功；[S 元数据](logs/20260923-classification-budget/cairn-share-source-ci.json)、[E 元数据](logs/20260923-classification-budget/cairn-x-enricher-source-ci.json)。S 含常规 Android 门禁，E 含双架构容器构建；没有据此声称设备、真实质量或生产验收。

## 初次失败与修正

- [Go 首次编译](logs/20260923-classification-budget/classify-budget-first.log)：新增引用漏 import，补齐；[进程夹具编译](logs/20260923-classification-budget/classify-budget-process-compile.log)调用不存在的助手，改为实际 HTTP 状态读取。
- [Worker 首次](logs/20260923-classification-budget/classify-budget-worker-first.log)：合成 fixture 只插 link 未建 classification job；补显式 job，保留原 lease/预算断言。
- [拆批首次](logs/20260923-classification-budget/classify-budget-unit-first.log)：预算中断后的 coverage 为空，是真实实现遗漏；补 partial/缺失问题后[通过](logs/20260923-classification-budget/classify-budget-unit-pass.log)。
- [首次完整 lint](logs/20260923-classification-budget/classify-budget-verify-first.log)：变量名遮蔽 builtin 与新夹具上下文传播，改为 ceiling 和真实 queue.Claim(ctx)，未禁用规则。
- [浏览器首次启动](logs/20260923-classification-budget/classify-budget-browser.log)：隔离工作树缺 Playwright，尚未开始浏览器；复用已有本地依赖。[随后 39/40](logs/20260923-classification-budget/classify-budget-browser-final.log)仅因夹具仍期待旧模拟模型名，改为已固定的响应版本，最终 40/40。没有放宽实际模型身份检查。

## 剩余范围

[完整 235 个编号](B10-20260923-scope.md)继续保留；本批收拢普通分类预算工程缺口，G01 实体结果去重及其它 G02–G11、canonical/词表治理/历史保留/迁移兼容/Android/自动质量/独立审查仍未整体验收。不能把本批局部通过写成所有任务完成。

所有者已免除人工标注依赖，质量使用有来源与生成记录的 `automatic_reference`，不冒称 human gold。本批冻结 dev/holdout 未用于推理、拟合或质量评分，未选择上线阈值。新增付费 **0**，累计 **211**。两 PR 仍 Draft，未部署、迁移生产、切目标、merge、Ready 或关闭 issue。
