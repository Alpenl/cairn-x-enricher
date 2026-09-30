# Jev 分类决策与人工整理 v2：完整审查记录

状态：设计审查与实施输入，**不是已实现功能或测试通过报告**。日期：2026-09-20。

审查基线：Enricher `cb13d0e543e54f18baf6e151630d728b3175e792`；配套 cairn-share `5cf3b0d45c8fbae07d0fb5be1772e7d747c69b4b`。两仓库在创建计划前重新核对，仍为上述版本。实现时必须记录实际基线并检查漂移。

本文件完整收录前次讨论的分析要点、原因、设计取舍与后续扩展，按 R01–R39 编号，供各批次逐项追踪。不把建议伪装成生产事实；不把一次合成样本的 API 成功当作准确率证明。前次讨论称曾做 SQLite 最小复现，但该次运行产物未随仓库保存，因此本计划仍要求执行者重新生成可审查的回归证据，不以聊天中的声明作为验收依据。

## 一、总体判断与应该保留的设计

### R01 保留原文、阅读增强、分类的边界

现有代码已经先保存原文，再做阅读增强；分类独立领取任务、失败独立退避，完成结果受租约与 revision 保护，且不覆盖人工整理。这不是仅替换了一个模型接口。下一轮应建立在这些基础上，而不是推倒重来。重点参考 [processor/stages.go](https://github.com/Alpenl/cairn-x-enricher/blob/cb13d0e543e54f18baf6e151630d728b3175e792/internal/processor/stages.go)、[Worker classification.ts](https://github.com/Alpenl/cairn-share/blob/5cf3b0d45c8fbae07d0fb5be1772e7d747c69b4b/worker/src/classification.ts)。

### R02 重构的主要欠缺在语义与产品模型

当前已经一次提交多个独立问题、保存完整 answers/model/usage。不要把“并行问问题”“保存概率”当作尚未实现的成果。真正的问题是丰富判断最终被压回“最多三个主题＋一种形态＋一种用途＋一个 uncertainty”。[分类器基线](https://github.com/Alpenl/cairn-x-enricher/blob/cb13d0e543e54f18baf6e151630d728b3175e792/internal/classify/jev.go)。

目标：Jev 回答边界清楚的语义问题，程序执行可重放的选择策略，人决定收藏意图和最终整理。标签是这些结果的展示，不是唯一底层事实。

## 二、词表和标签结构

### R03 分离内容主题、内容功能与内容载体

词表把 method/tool/data 与 thread/longform 放进同一 form。工具介绍可以同时包含方法、案例和数据，也可以以串推发布。强迫它们单选会提前丢失信息。建议 topics 多选、content_functions 多选、carrier 由真实来源结构优先确定。可保留 primary_form 作为卡片摘要，但不能作为唯一事实。[词表基线](https://github.com/Alpenl/cairn-share/blob/5cf3b0d45c8fbae07d0fb5be1772e7d747c69b4b/worker/src/taxonomy.json)。

### R04 潜在用途不等于用户意图或用户立场

quote/try/background/material 是可能并存的用途；contra 是用户对某个主张的明确反对关系，不应与它们争夺单值 use。actionable 表示内容提供可执行材料；to_try 表示用户决定以后尝试，必须分开。允许用户不填写意图；不能从作者立场、引用关系或用户收藏动作推断用户赞成/反对。敏感观点不做自动偏好画像。

### R05 同义词、上下位概念与相关概念分开

现有 llm aliases 含 AI/AIGC/agent，eng aliases 含前端/后端/基础设施。真正同义词用于精确归一化；上下位关系用于层级浏览；相关概念用于检索提示，不能无条件视为等价。eval 更像跨领域的方法维度。保留已使用稳定 ID；任何迁移必须显式映射、保留历史含义，不能偷偷改变旧 ID 的语义。不要一下扩成几百个主题，先由实际找回任务证明细分价值。

### R06 三主题上限应是展示规则，不是分散的事实约束

限制分布于 Go selectAnswers、Normalize、ValidateSelection、Worker validateSelection 及阅读页。只改分类器没有用。底层保存完整有效判断，策略保留实质相关主题，卡片默认显示三个并可展开。仍保留防异常膨胀的可配置安全上限；第四个高质量主题本身不应导致不确定。[Go taxonomy](https://github.com/Alpenl/cairn-x-enricher/blob/cb13d0e543e54f18baf6e151630d728b3175e792/internal/taxonomy/taxonomy.go)、[Worker curation](https://github.com/Alpenl/cairn-share/blob/5cf3b0d45c8fbae07d0fb5be1772e7d747c69b4b/worker/src/curation.ts)、[reader.js](https://github.com/Alpenl/cairn-x-enricher/blob/cb13d0e543e54f18baf6e151630d728b3175e792/internal/dashboard/reader.js)。

### R07 概率不是重要性排名

独立 Noul 的概率表示不同命题成立的把握，不天然适合跨主题比较重要性。宽主题可能更容易成立，不代表更值得展示。需要核心程度时采用有清晰等级的判断或明确展示规则，不能简单按概率把宽主题挤到前面。[Noul](https://docs.typesafe.ai/primitives/noul)。

## 三、不确定性与决策

### R08 全局 uncertainty 放大人工负担

基线任一主题处于 0.2<p<0.8 就令整条不确定；强匹配超过三个、none、空值也同样处理。应改为字段级 accepted/rejected/abstained，让已明确主题正常生效，边缘候选可选择确认。人工队列优先处理会改变有效检索或整理结果的分歧，而不是机械审查所有中间概率。

### R09 合法空值、词表外、证据不足与服务失败必须区分

建议原因代码：not_applicable、out_of_taxonomy、insufficient_evidence、ambiguous_boundary。任务 completed/failed/waiting_source 与字段判断是两条轴。没有匹配不必重试 API；材料不足才考虑补证据；问题定义重叠应改问题；HTTP 失败走工程重试。未知原因不能由概率凭空编造，优先使用可观察的完整性元数据。

### R10 当前 Choice margin 条件冗余

最高概率>=0.65 时，归一化分布中的次高<=0.35，差距至少0.30，因此 margin>=0.15 不增加约束。允许的小幅概率和误差也不足以使它生效。用表驱动测试说明此性质，再通过离线验证保留真正有效的规则组合。不要靠继续叠阈值假装更严谨。

### R11 confidence 不是独立证据

现代码校验保存 Choice confidence 而未用于选择。可以评估它的用途，但官方说明其来源是已有概率分布，不是独立正确率。禁止默认 probability×confidence；不得以更高 confidence 等同更高准确率。稀有标签采用共享/分组策略，不用极少样本为每个标签拟合精确阈值。[Confidence](https://docs.typesafe.ai/confidence)。

## 四、充分而克制地使用 Jev

### R12 按问题含义选择原语

Noul：是否实质讨论主题、是否有步骤/实验结果、是否依赖未取得材料。Choice：真的需要单选的主要功能、有限实体候选对齐、明确的原因选项。Score：可执行程度、背景完整程度、主题讨论深度。Score 使用自洽且具体的有序描述，保留分布，不能只保留均值；相同均值可来自完全不同的分布。不要对所有标签机械地同时使用 Noul 与 Score。[Score](https://docs.typesafe.ai/primitives/score)。

可执行程度示例：没有动作；仅方向且缺关键步骤；具体步骤和必要条件；可运行示例与结果检查方式。这是设计示例，不是生产校准等级。

### R13 词条成为可执行语义定义

重要或易混淆词条补 definition/includes/excludes/positive_examples/boundary_examples/related_terms/broader_terms。优先工程vs工具推荐、产品vs设计、评估vs泛泛体验。instructions 与 criteria 可直接结构化，不再硬拼长字符串；Go 请求类型需要支持对象/数组及 Score 的有序数组，并保留类型与大小验证。[结构化问题](https://docs.typesafe.ai/primitives/advanced)。

### R14 问题 ID 不提供语义，问题之间不共享答案

topic_eval 之类 ID 只用于代码关联，完整问题含义必须在 instructions/criteria。一个请求中的问题看到同一 state、独立作答，不能引用另一问题尚未产生的答案。同证据独立问题继续批量；只有获取新证据或构造新候选确实依赖前一步结果时才多调用。[HTTP API](https://docs.typesafe.ai/api)、[fan-out](https://docs.typesafe.ai/patterns/fan-out)。

### R15 区分来源角色并保留 provenance

现有 original_text/context_text/note 分离值得保留，但 context_text 合并引用与评论。建议 evidence blocks：primary_post、author_continuation、quoted_post、external_article、third_party_comment。记录 block ID、URL、作者关系的依据、抓取方式、时间、完整性及截断。引用不等于认同；第三方评论不能代替原帖。旧快照无法确认角色时标 legacy_unknown，不能编造作者/链接。[source.go](https://github.com/Alpenl/cairn-x-enricher/blob/cb13d0e543e54f18baf6e151630d728b3175e792/internal/enrich/source.go)。

### R16 客观判断与个人备注物理隔离

结构化字段只是提示区分，不是信息隔离。若要求修改备注不影响主题，则客观分类的请求体中根本不放 note/why/个人状态；个人维度单独请求或纯人工。所有问题共享同一 state，不能只写“忽略备注”而声称获得严格隔离。[State](https://docs.typesafe.ai/concepts/state)。

### R17 控制材料长度并验证语言表现

按实施时官方文档核实模型上下文预算、问题数、选项数；不能用字符数冒充准确 token 数，也不能静默截断。当前文档说明文本输入及语言能力边界，中文/英文/混合应分别评估。比较中英文问题规格，但原文仍是证据，翻译只能作为有 provenance 的辅助，不得取代原文。

## 五、模型判断与程序决策分层

### R18 分成证据、问题、原始判断、决策、有效视图

推荐 Evaluate(ctx,evidence,spec)->RawJudgments；Decide(raw,policy)->Proposals；Resolve(proposals,overrides)->EffectiveClassification。后两者是无网络纯函数。把 HTTP、响应校验、问题编译、选择、模板建议从 Client.Classify 拆开；保留现有 Go 接口和单进程。

### R19 分离失效键

evidence_hash、question_spec_hash、resolved_model_id、decision_policy_version 分开。显示名变化只改展示；阈值/排序变化重放决策；定义变化重评相关问题；原文变化重评内容；备注变化只影响个人维度。问题级重用必须证明实际发送 state、候选、问题字节和模型一致；不能只因 ID 相同就复用。

### R20 审计历史需要可重放快照

classification_jobs.result 会失效清空或被覆盖，不是长期实验记录。建议 append-only classification_runs 与可恢复的 evidence/spec 引用；links 保留当前投影。只有 hash 没有快照不可复现。审计不可变指业务写入不可篡改，不得妨碍用户删除其内容及明确的保留期清理。[迁移0009](https://github.com/Alpenl/cairn-share/blob/5cf3b0d45c8fbae07d0fb5be1772e7d747c69b4b/worker/migrations/0009_independent_classification.sql)。

### R21 固定模型并管理漂移

jev-latest 可移动，而请求字符串不变。校准绑定实际模型和问题规格；上线使用经验证的固定模型或明确禁止未确认漂移自动晋升。旧结果记录 requested/resolved 两者。不得凭计划指定一个未来仍必然可用的版本。[Models](https://docs.typesafe.ai/models)。

## 六、必须优先修复的具体问题

### R22 P0：客户端版本驱动领取会交替重跑

同一词表下，A 请求policy-v2完成后，B 请求policy-v1会因版本不等再次领取；A又可反向领取，attempt在变版本时重置。这是代码允许的路径，不代表生产已出现事故。必须以实际 Worker 测试重现并锁住回归。Worker 统一目标规格，job绑定 target spec；客户端只声明支持能力，不改全库目标。灰度按任务显式分配目标，不按消费者最后一次请求决定。

### R23 P0：首次保存 why/status 可隐式确认全部标签

reader.js 的 !classification_reviewed 条件会发送当前 classification，即使用户只改原因；Worker 以整份 curation 覆盖且有效 uncertainty=false。保存原因/状态只写这些字段，接受建议必须明确操作。否则既锁错标签，又污染未来“人工确认”评估数据。原有历史确认不能被自动视为可靠训练标签。

### R24 阅读增强仍重复输出已存档原文

Transform 从旧 enrichmentSchema 删除 classification，仍要求完整原文/链接/图片，再用存档字段覆盖。建立独立 ReadingResult/schema，仅生成标题、译文、摘要及确有需要的语言信息，程序注入源引用。既避免输出浪费，也切断旧整包生成模型的耦合；保留严格校验和source-first安全。

### R25 工程错误不能混入语义弃权

区分鉴权/契约错误、限流/过载/网络临时错误、旧租约冲突。配置类错误应触发组件级降级并停止烧完每条任务额度；临时错误按有上限退避处理；重复提交要幂等；409 stale/superseded属于正常并发结果。保存成功响应丢失后应查找已存run，而不是重新付费评估。任何异常不得回显密钥、完整源文或提供方响应。

## 七、整理交互与反馈

### R26 按字段少打扰地解释

默认显示可用标签，边缘候选可接受/忽略；详情才展示分布、版本、原因。证据必须指向真实 block/span，必要时从受控候选ID选择并允许none；概率不是证据，生成的解释不是原文证明。

### R27 三种状态和三种重做动作

分开源文/阅读处理、AI分类、人工inbox/kept/compiled/drop。提供只重试分类、只重放策略、明确刷新来源等动作；分类失败不重新抓文。Web后台尚无独立分类面板的事实见[现有说明](https://github.com/Alpenl/cairn-x-enricher/blob/cb13d0e543e54f18baf6e151630d728b3175e792/docs/jev-classification.md)。不要由模型自动改整理状态。

### R28 字段/标签级人工覆盖

独立接受、拒绝、设为空、恢复自动。unset表示不覆盖，明确空集合表示用户希望无该维度，拒绝某标签要防其在同一有效证据上重放后悄悄回来。原文变化保留人工行为但标注适用来源变化，不能未经用户同意重释。优先级和冲突解决有确定性及测试。

### R29 反馈事件不是偏好画像

curation_events关联当时输入revision、run/spec、明确动作、前后变化。只用明确接受/拒绝/修改作反馈，不用单纯查看或保存why当标签确认。首先服务回归集、词条边界、阈值校准；拒绝某条“工程”不代表用户不喜欢工程。历史事件删除和隐私遵从同源数据策略。

## 八、扩展任务也必须进入计划

### R30 独立实体生命周期

现Jev分类entities=[]会替换旧AI实体建议。恢复实体候选提取→有限候选验证/对齐→有来源的存储，不能让Jev自由生成名字。候选可由解析器或获授权的生成模型提供；未知可保留surface form但不自动建全局身份。not_run/failed/completed_empty/completed_nonempty分开。旧结果不无故清空，也不得跨来源版本伪装新鲜。

### R31 按需补证据，而不是所有低置信都升级大模型

区分缺图文、外链未抓取、截断与词条边界歧义。仅明确缺口触发受预算、去重、安全URL约束的补材料流程；补充块有provenance，不能反写原帖。无授权/不支持来源时给可操作状态，不可编造成功。

### R32 先召回、后重排

可在已有关键词与分面候选上进行有预算的小范围Jev相关性重排；需同规格、同相关性等级，不把跨问题概率当排序分。失败降级原排序，保持筛选、分页与授权。不引入向量库作为先决条件，也不声称重排能找回未入候选的收藏。

### R33 受控词表演进

汇总词表外及混淆边界；候选提案由人批准，不能由discarded_tags或单次模型输出自动增标签。停用/合并需历史别名与迁移记录；display变更不调用模型，语义变更按影响范围受控重评。

## 九、评估、迁移与完成标准

### R34 真实评估集与证据边界

当前测试主要是接口和选择契约，一条合成live样本不证明准确率。初始约200–300条真实授权样本是建议规模，不保证覆盖稀有标签。覆盖中英混合、多主题、短/长/纯链接/依赖图片、引用反驳、词表外；线程和近重复分组隔离训练/调参/测试，禁止泄漏。没有人工gold时只能报告工程通过、质量未验证。

### R35 评估指标必须包括找回与人工负担

每维度precision/recall、接受错误率、自动覆盖率、每条需确认字段数、补证据比例、成本/延迟，以及真实检索任务Recall@K/nDCG等。报告样本数和不确定范围。不能通过无限弃权换取漂亮precision，也不能以更高confidence宣称质量提升。

### R36 逐项消融而不是一起改

保留v1基线；分别比较局部弃权、词条边界、维度单多选、必要Score、中英文规格。变更问题后需要新推断，改阈值使用同一answers重放。冻结验收策略再看测试集；模拟响应只证明工程行为。

### R37 跨仓库兼容、代码清理及部署顺序

v2通过显式版本/opt-in，不把use字符串直接改数组。旧App六字段及旧include=enrichment投影保持；Go严格JSON解析要通过协商避免新字段致崩溃。协调Worker、Go、Web、Android缓存和导出。只新增迁移，不改已发布0009；先兼容后端再客户端，再启用开关，回滚不销毁历史。分类器拆transport/questions/policy，taxonomy保留概念约束，旧Generate/Workflow移到明确legacy适配边界并保留实验。

### R38 运行安全、预算与审计

保留独立token、服务端密钥、受控R2图源、原文不回显日志等保证。原文和note不得为评估擅自发布到公开GitHub。增加输入/输出边界、超时、退避预算、幂等、组件健康和分类队列指标。新的外链抓取必须防SSRF、跨域携带凭据和重定向绕过；只有通过安全与质量门槛才能打开。

### R39 分阶段验证与人工最终审查

优先P0，再持久化契约/纯策略，再标签v2与前端，再评估和可控扩展。Go/Worker/Android单元及契约、真实浏览器、迁移/回滚、双版本实例、超长state、用户覆盖保护均纳入。每PR提供任务→提交→测试→结果证据链。单元通过不等于生产部署，部署未获授权不得执行。整个程序不需Redis、RAG或微服务；扩展功能先完成实现和离线验证，真实效果不通过则保持关闭并明确未达发布门槛。

## 执行导航

批次、顺序、跨仓库依赖与DeepSeek执行协议见同目录 `README.md`；逐批任务见各草稿PR中的 `docs/jev-v2/NN-*.md`。总清单不会把未做工作勾为完成。任何与本审查不同的实现决定，必须说明新证据、影响和回归测试，不允许静默缩减范围。
