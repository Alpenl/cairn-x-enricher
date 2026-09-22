# B09 / 实体、补证据、重排与词表提案：设计规格

任务进度：[E #15](https://github.com/Alpenl/cairn-x-enricher/issues/15)，B09-T01–T14全文在Issue。依赖B03/B05后端、B04判断与B08离线工具。无gold可继续实现，但不默认启用。四项能力应按可审查改动分多个代码PR，不因原批次号塞成一个巨大PR。

## 实体

候选先从存档text/links解析surface/project URL/精确span；确需开放提取使用独立获授权生成能力，不让Jev造名字或重新抓X。Jev只做实质相关/偶然提及/有限canonical匹配，有none/unknown，同名无证据不合并，候选外值拒绝。

实体not_run/failed不覆盖同revision成功值，completed_empty明确；source变stale，人工优先、提交幂等、缓存索引同步。surface与canonical来源分开，UI状态通过B05合同表达。

## 按需补材料

仅有明确缺口（未取得外链、关键图文缺失、可观察截断）触发，不把合法none/边界歧义送强模型。记录why_needed/source/scope/budget，优先人工入口；无能力/授权明确blocked。

至少一个真实受控外链适配器，不只空接口。允许名单、实际连接地址、DNS重绑定、私网/loopback/link-local/metadata、IP/端口/scheme、每跳redirect、MIME/大小/timeout检查，禁止跨域凭据和正文指令驱动任意URL。测试用模拟DNS/本地server，不访问真实metadata。

图文字等适配器仅能力已存在且获授权；转录/译文标方法、可靠性与来源，不覆盖原帖，不声称Jev看图。取得内容才追加block/update revision/受控重评，重复内容幂等，失败保已有可读内容，有限attempt与预算。

## 重排与治理

先用B05权限过滤后的有界候选，再同query/rubric做相关性判断，稳定ties；不能比较不同Noul命题当全局重要性。失败/off/超预算回原序，不改变集合/权限/筛选。跨页冻结候选session或明确当前范围，cache含query/filter/候选content revision/spec/model，私人query不公开。

词表缺口由有依据词表外/人工纠正/混淆对提案，含定义/反例/计数/影响mapping；unknown原因不硬当词表外。人工批准/拒绝，批准有diff/稳定ID/version/影响dry-run/rollback；display-only重显，定义变化受控重评，未批准不改词表、不自动造空标签/全库队列。

## 预算与验收

每扩展独立flag默认off，单条/整体token和调用预算、timeout/取消/恢复/去重；用户材料和模型不能加额度。普通分类不因扩展未开失败。用B08分别off/on评估实体precision/span/canonical误合并、补材料收益/成本/重复、候选召回与rerank、p95和预算耗尽，无gold只报工程。

SC10/17–21/24–26/29–30；R12/R14–R17/R26/R30–R33/R38。恶意URL/注入/超预算/幂等/分页/实体状态/未审批词表均有断言，make verify与合同集成；交evidence/B09.md、flag/provider/unsupported范围/调用数和实现PR。关flag不清历史或人工提案，重排回原序，补材料仍可人工。未授权不收费/merge/部署。


## 持久预算执行（2026-09-23）

实体、补材料和重排在 `serve` / `classify` 生产入口共享 Worker/D1 账本，范围是一套 Worker 部署、UTC 自然日。三个 flag 仍独立、默认关闭。全局每天最多 20 次逻辑扩展操作，每条收藏最多 2 次；多候选重排对每条候选都计一次、同时全局只计一次。补材料的一次受控 fetch 算一次操作（重定向仍受独立跳数限制），不产生 Jev token。此额度不包含普通主分类，其整体预算仍属于 B04-T14 / SC30 的其余范围。

模型扩展只支持已核对上限的固定 `TYPESAFE_MODEL=jev-1.13.0`；移动 alias 或未核对型号在调用前明确拒绝。按 [官方模型合同](https://docs.typesafe.ai/models)（2026-09-23），每次请求预留 65,536 输入 token，输出免费；预留量不是实际 usage，也不使用字符数推算实际付费 token。请求自身仍经过真实序列化字节上限检查。每天全局预留最多 1,310,720，每条收藏最多 131,072；每个候选保守计整次预留。失败、超时及结果不明均不退款。

配置只能收紧 Worker 固定上限：

| 环境变量 | 默认 / 最大值 |
|---|---|
| `CAIRN_EXTENSION_MAX_CALLS` | 20 |
| `CAIRN_EXTENSION_MAX_CALLS_PER_ITEM` | 2 |
| `CAIRN_EXTENSION_MAX_INPUT_TOKENS` | 1310720 |
| `CAIRN_EXTENSION_MAX_INPUT_TOKENS_PER_ITEM` | 131072 |
| `CAIRN_EXTENSION_TIMEOUT` | 20s |

低于一次预留的 token 限额会阻止模型扩展；普通分类仍独立。客户端也有跨请求、并发安全的每日上限，但进程重启不重置 D1。多个客户端使用不同较低上限时，每次申请同时受该客户端上限和 Worker 上限约束；进程退出不删除消费记录。午夜是显式的预算重置点，跨午夜可能紧接两次额度；这不是滑动 24 小时窗口。

新增内部 `POST /api/v2/extension-budget/reserve`，只接受 enricher token。请求仅含随机 operation key、数字收藏 ID、kind 和额度；无原文或检索词。授权和全部逐条记账使用同一 D1 事务。重复 key 不再授权，异 payload 冲突；授权响应丢失时不发外部调用，保留额度。普通客户端不能提高上限，材料和模型答案不参与配置。删除收藏清除其逐条预算，匿名全局消费保留，因此删除/重建不能退回全局额度。复用已有 `budget_ledger` 和 0026 删除守卫，无新增迁移。

先部署具备此端点且已应用 0026 的 Worker，再升级消费者并评估 opt-in。旧 Worker 返回 404、断网、非法确认或额度耗尽时，扩展失败降级且不调用付费接口；不回退到仅内存额度。回滚前关闭所有扩展 flag；回滚到旧消费者的启用路径会重新引入旧预算缺陷，不能称为兼容保证。本轮未执行部署或生产迁移。

当前去重仅保证同一授权 key 不重复授权，以及现有补材料执行/检查点语义；**不是重排结果缓存**。相同查询再次进入生产 API 仍可消费剩余额度。query/filter/candidate/content revision/spec/model 完整缓存、跨进程结果恢复、固定候选质量与逐 flag 收益仍须继续完成，不因预算修复关闭 B09-T01/T10 或 G01。
