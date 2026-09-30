# B09 / 实体、补证据、重排与词表提案：设计规格

任务进度：[E #15](https://github.com/Alpenl/cairn-x-enricher/issues/15)，B09-T01–T14全文在Issue。依赖B03/B05后端、B04判断与B08离线工具。质量使用有来源的 automatic_reference，不等待人工标注；不默认启用。四项能力应按可审查改动分多个代码PR，不因原批次号塞成一个巨大PR。

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

每扩展独立flag默认off，单条/整体token和调用预算、timeout/取消/恢复/去重；用户材料和模型不能加额度。普通分类不因扩展未开失败。用B08分别off/on评估实体precision/span/canonical误合并、补材料收益/成本/重复、候选召回与rerank、p95和预算耗尽，没有可追溯参考时只报工程；允许 automatic_reference 质量评估，须报告来源和局限。

SC10/17–21/24–26/29–30；R12/R14–R17/R26/R30–R33/R38。恶意URL/注入/超预算/幂等/分页/实体状态/未审批词表均有断言，make verify与合同集成；交evidence/B09.md、flag/provider/unsupported范围/调用数和实现PR。关flag不清历史或人工提案，重排回原序，补材料仍可人工。未授权不收费/merge/部署。


## 持久预算执行（2026-09-23）

实体、补材料和重排在 `serve` / `classify` 生产入口共享 Worker/D1 账本，范围是一套 Worker 部署、UTC 自然日。三个 flag 仍独立、默认关闭。全局每天最多 20 次逻辑扩展操作，每条收藏最多 2 次；多候选重排对每条候选都计一次、同时全局只计一次。补材料的一次受控 fetch 算一次操作（重定向仍受独立跳数限制），不产生 Jev token。此额度不包含普通主分类；普通分类另有跨进程的每日 20 次全局 / 5 次逐条实际 HTTP 请求额度，见[分类运行说明](../jev-classification.md#持久调用预算)。

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

## 持久重排缓存（2026-09-23）

`serve` / `classify` 的扩展 Service 使用新增内部 `/api/v2/rerank-cache/claim`、`/:key/complete` 与 `GET /:key`。只有 enricher token 可访问；App token 无权读取或写入。Worker 必须先应用 **0027**，再部署兼容 Worker 和消费者；旧 Worker/缺少候选版本均明确降级，不回退到无持久缓存的付费执行。

首次 claim 原子取得唯一执行权；相同查询的并发请求回原序并报告 pending。已完成结果在新进程中复用，无需预算授权或新模型调用，因此已耗尽预算仍能读取当前有效结果。模型调用前仍执行上一节的逐条和全局持久授权；单纯取得缓存执行权不等于付费授权。失败、超时和未知结果不重跑模型。完成提交可在取消后用独立最多 5 秒的上下文重试同一幂等写入至多两次，每次失败可读取已提交结果；未确认持久化不显示为成功。

缓存 key 包含实际 provider JSON（固定模型、query、逐条材料及同一分级 Score 题目）、规范化筛选/游标/limit 的 hash、rubric/spec hash、候选顺序及每条 content/body/personal/decision/entity revision。只支持 `jev-1.13.0`，其它 model/alias 在调用前拒绝；不能沿用旧结果。0027 为旧的 note/why/curation_status 写入补 personal revision 触发器，为 title/summary/状态/URL 补 reading revision；同值写入不递增。数据库 claim/complete 在事务语句内核对全部版本，读取结果也检查当前版本。

`POST /api/rerank` 接收 `query`、`limit`（最多 20）、可选 URL 编码 `filters`，使用与收藏列表相同的筛选验证。只排列 **当前筛选页**（`scope=current_candidates`），保留原列表的 `next_before_id`；不承诺跨页全局相关性顺序或冻结整个收藏库。候选材料为明确标注 `reading_summary` 的 AI 标题和摘要（最多 400 字符），不是原始来源全文。无 UI 排序控件默认开启。返回前重新读取同一筛选页，即使命中缓存也一样；候选/版本/文本变化时返回最新页原序，读取失败不返回旧候选。

D1 私有缓存保留精确 provider 请求和原始分布，不含凭据；原文/查询不能写入公共日志或报告。每项从首次 claim 起固定 **24 小时**，pending/completed/failed 均不续期；期间失败/未知结果不自动重新授予，过期后可在预算允许时新尝试。部署硬上限 200 项，请求和五分钟隐私 Cron 各清理最多 100 条过期项；claim 另在同一事务删除当前过期 key，避免批量清理未扫到它时误命中。过期立即拒读；物理清理受 Cron/请求执行及故障影响，不声称严格 24 小时物理清除 SLA。任一候选删除时，同一 D1 事务清除整条多候选私有请求、答案和引用，匿名预算不退款。

回滚消费者前关闭扩展 flag，保留迁移和兼容清理 Worker。将全局 `TYPESAFE_MODEL` 从 alias 改为固定版本也会改变普通分类目标，应按 B01 desired target/new generation 受控操作，不自动激活。普通分类持久预算另见 B04；实体结果去重见下节；固定候选的真实相关性质量和逐 flag 收益仍待其余任务完成；本次工程验证不关闭整个 G01/B09，也不选择上线策略。


## 实体持久结果与恢复（2026-09-23）

`serve` / `once` / `classify` 的实体入口接入仅 enricher 可访问的 `/api/v2/entity-cache/claim`、
`/:key/complete` 和 `GET /:key`。先应用 **0028** 并部署兼容 Worker，再启用消费者。
缺少协议、缺失绑定、非法 hit 均停止该次实体扩展，不回退到重复付费；扩展仍默认关闭。

缓存身份包括收藏、content revision、不可变 snapshot ID/hash、实际序列化 Jev 请求、
候选及原文 rune 位置、问题和本地选择策略 hash。Worker 检查材料等于绑定快照、候选
span 精确匹配原文、URL 候选属于当前来源链接。固定模型沿用 `jev-1.13.0`；完整 Noul
答案集合及 0..1 范围在保存和消费者复用时校验。缓存保存实际原始概率，非仅选中实体名。

首次原子 claim 才取得执行权，之后仍需申请既有扩展 20/2 日预算。重启、重复 claim、
并发 pending、先前 failed 均不重新授予；完整成功结果（包括 completed_empty）可在
额度耗尽后复用。取得缓存执行权本身不扣模型额度。没有候选时为本地确定性空结果，不调用模型。
模型失败或未知结果不重跑；完成写入最多两次、间隔读取恢复已提交结果，取消后收尾最多 5 秒。
缓存成功后写实体状态有独立最多 5 秒、三次相同幂等提交；未确认缓存持久化不能宣称成功。

稳定结果操作 key 包含缓存 key 和原始答案 hash；重复分类不会重复更新相同实体结果。
缓存结果的状态写入不再携带本次调用数，以免零调用 hit 与首次写入发生 payload 冲突；
实际调用和保守 token 预留仍在既有预算账本。未知旧 entity_states 没有模型/问题身份，
不能作为缓存命中。人工 override 始终独立；人工备注、阅读摘要或分类 decision 改变不让
同一客观实体材料重新付费。来源文本、上下文、链接或问题改变则需重新处理。
0028 补来源链接单独变化的 content revision 触发器，使旧有效实体立即变 stale，
并拒绝旧快照的迟到提交；主文本变化沿用原触发器，同值链接与备注变化不重复递增。

私有请求、候选和原始答案自首次 claim 固定保留 24 小时，hit 不续期；pending/failed
也不提前重新授予，过期后可在预算允许时重试。部署最多 200 项，每次 claim 和隐私 Cron
清理最多 100 项，当前过期 key 在 claim 事务中单独清除；过期即拒读，停机可能延后物理删除。
删除收藏或其证据快照会级联删除整个私有缓存；匿名全局预算不退款。该期限只覆盖此缓存，
不是对全部历史 run/evidence/entity_operations 保留期限的声明。

回滚消费者前关闭实体 flag，保留迁移和兼容清理 Worker。无预算/无缓存旧消费者的启用
路径不具备这里的保证。此项解决重复推断和持久恢复，不声称完成有限 canonical 匹配、
实体类型/来源的全部产品展示、真实实体 precision、误合并率或整个 B09/G01 验收。


## 有限实体身份与逐处来源（0030，代码准备）

实体开启后使用明确 Choice 区分实质讨论、偶然提及、非实体和未知；同名的每个来源位置
分别判断并保留 block/rune 位置，旧显示名称列表仍去重，不据此合并身份。目录使用本地
`CAIRN_ENTITY_CATALOG_PATH` JSON，由运营者控制，不接受正文或模型生成的目录或路径。
未配置目录时仍运行实体相关性，身份明确 unknown，不伪造 canonical；四个扩展开关仍默认 off。

目录格式：`{"version":"team-1","entities":[{"id":"project-a","label":"Example","kind":"project",
"aliases":[],"identifiers":["https://example.com/project-a"]}]}`。kind 支持 person / organization /
product / project / place。最多 1 MiB、200 个实体、每项 16 个别名和 8 个 HTTP(S) 身份 URL；
ID 唯一且稳定，非法/重复身份、未知字段、额外 JSON、无身份 URL 会在开启实体的消费者启动
时拒绝。目录文件从其父目录的受限文件句柄读取，不能以符号链接逃逸；不要在日志公开私有目录。
实体关闭的流程忽略该配置，普通分类不因无目录而失败。

每个原文候选只得到同名/别名且有本来源块身份 URL 支持的有限选项；其它块的 URL 或仅同名
不能生成匹配候选。链接候选只匹配其本身 URL。候选表面名须符合既有存储的 120 个 UTF-16 单元限制；超长片段直接跳过，不截断造名。
每处最多 8 个身份；超出时整组为 unknown，
不截断后误选。URL 仅用于身份比较，不发抓取。Jev 在允许身份、none、unknown 中判断，
仍须确认 URL 与该出现的实际关系；真实误合并率须用自动参考评估，类型安全不等于语义正确。
身份问题与相关性问题在同一请求中独立提问，不能读取彼此答案。结果接受阈值暂保持 0.8，
低于阈值为 unknown；未据本批合成夹具选择阈值或启用线上策略。

请求保存实际材料、候选、有限目录定义/身份依据和目录版本；完整 Choice 分布和 confidence
进入私有持久缓存。目录内容/版本、候选、问题或策略变化改变缓存身份。每个结果的 observation
还保存原文候选、相关性判断、允许身份定义、完整 raw、选中身份和独立身份依据，0030 将
其与 entity_states/操作记录保存，缓存过期后仍可解释；删除收藏时一起清除。历史记录迁移
为 observations=[]，不补造旧身份依据。未知/非实体/偶然提及可组成 completed_empty，
不与 failed/not_run 混淆；旧消费者不能用无逐项来源的同材料结果覆盖已有逐项成功结果。

Worker `/api/v2/links/:id/entities` 增加 observations（归档判断）及 effective_observations
（当前有效且未经人工拒绝的相关候选）。人工结果优先，来源变 stale 后有效 observation
为空，归档仍在。Web 的“自动判断依据”区分原文、受控名称/类型、身份链接和当前/历史状态；
Android 逐项来源展示尚需后续接入，不把 Web 通过当作 Android 完成。

先应用 0030，再部署新版 Worker 和 Go；旧 Noul 缓存合同仍读取历史结果，新协议含
entity_protocol=2。新消费者遇不支持协议的旧 Worker 在缓存 claim 阶段拒绝，不回退到
未经持久化的付费路径。回滚保留迁移及数据，关闭实体 flag。仍沿用固定模型与逐条/全局
持久预算、实际请求字节上限和总超时，无新增付费回退。目录编辑目前是受控运维配置，
不是已完成的词表提案审批、影响 dry-run 或生产发布系统。

目录在消费者启动时读取；调整后重启消费者，不自动重新处理历史。全部目录/题目/usage 的长期重建与保留须继续按 B03/B10 完整要求验收。
