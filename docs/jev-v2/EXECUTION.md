# DeepSeek 执行协议：Issue 驱动的实现与复审

工作流版本`issues-v1`。本协议取代旧的“提前创建十个计划PR、向固定head分支逐级追加实现”流程。任务状态在[总控Issue #10](https://github.com/Alpenl/cairn-x-enricher/issues/10)及其关联Issues；稳定映射见[ISSUE-INDEX](ISSUE-INDEX.md)。本文件保留契约和SC01–SC30，不是一份实施成绩单。

## 1. 工作入口与执行生命周期

读取当前仓库AGENTS.md、TypeSafe项目技能、总控/当前Issue、本目录REVIEW/README/对应规格。设计PR #3未合并时按明确分支SHA读取文档，不把文档分支当生产代码依赖。两仓放相邻目录，检查main、当前分支、工作区和他人改动，记录实际SHA；先保存baseline测试和B08-T01数据授权/已有raw引用，私人内容不公开。

选择无阻塞任务或当前Issue可独立部分，在Issue正文/评论登记范围与阻塞。默认由最新main建立真实实现分支，首个可审查代码或回归提交后再建Draft代码PR。一个Issue可多个PR，批次编号不是PR边界；原计划分支仅历史，禁止向其追加实现或再建纯待办占位PR。

仅依赖尚未合并真实代码时可短堆叠，记录上游PR/精确SHA、base选择原因和跨仓companion SHA；未经授权不为推进后继自行合并main。可用隔离工作树联调已验证实现SHA，不能假称依赖已合并。上游修复回到对应真实PR，更新后继锁定版本；保留无关改动，不force-push/删分支。

每项执行“核查现状→失败回归→实现→局部测试→完整门禁→证据”。重构先冻结fixture；确实不能先失败则写原因。拆更多Issue/PR必须保留原Bxx-Txx及继承关系，不能以拆分静默减范围。TODO、空数组、mock成功、跳过测试不是实现。

状态统一为not_started、in_progress、blocked_external、ready_for_review、accepted；blocked_external可只针对若干任务，其余继续。每个任务勾选需实际代码和相应测试证据，勾选不等于独立审查通过。Issue正文是实时进度唯一来源；仓库规格不维护第二套勾选，证据报告只记录带日期/SHA的验证快照。

## 2. PR、Issue关闭及审批

真实代码PR标题描述实际改动，正文至少列Issue、覆盖任务ID、未覆盖项、设计依据、base/head/companion SHA、测试结果、迁移回滚及风险。模板见[CODE-PR](templates/CODE-PR.md)。部分PR只用普通引用，例如`Refs Alpenl/cairn-share#28`，不要用自动关闭关键词，也不要将批次Issue设置为会被部分PR合并自动关闭的Development关联。

GitHub会在默认分支相关合并场景解释关闭关键词；手工Development关联也可能随合并关闭Issue。不能以“不是Closes”就假定任何链接都无关闭作用。参见[GitHub官方关联说明](https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/linking-a-pull-request-to-an-issue)。本工作流用普通正文引用和Issue实现清单避免提前关闭。

Issue关闭必须由所有者/独立审查者确认全部任务、跨仓验收和必要合并已完成，或明确批准例外并建立剩余工作的后继记录；不得由执行者为了清空待办自行关闭。完整单一修复Issue只有全部闭合条件满足且获批时才使用自动关闭。设计PR #3只合并文档，不关闭总控或实施Issue。

单列engineering_done、quality_verified、review_accepted、merged、deployment_authorized。代码mock通过不等于模型效果；设备编译不等于运行；未获发布授权不阻止交付代码，也不允许伪称已上线。无gold/收费授权/设备等明确局部阻塞，不编造结果。

## 3. 不可省略的合同

### 3.1 权威目标、租约、幂等

Worker管理desired target：不可变spec/hash、requested model、policy及单调generation。job绑定content revision、必要时personal revision、generation/spec、lease/有效期。consumer声明能力不改全库目标；旧consumer不得领取v2任务。灰度限定job显式目标；回滚新generation指向旧spec。

complete/fail检查lease/input/spec/generation/状态。相同operation key和payload返回既有结果，不同payload冲突。run/decision/job/projection及缓存失效处于正确原子边界；旧结果可审计但不改当前视图。完成响应丢失先幂等重试/查询，不能重新付费推断。提供方在响应未知/进程崩溃时若不支持幂等，不能宣称端到端exactly-once计费；记录剩余不确定性和预算，至少确保已知提交成功场景不重复调用。

配置/鉴权/契约错误、临时网络/限流/过载、stale/superseded分开，逐案确定暂停组件还是输入问题；不要只凭一个HTTP码吞错误。单层有界退避，组件健康与liveness分开，每job deadline及shutdown上限，不继续领取新job。

### 3.2 版本与重用

evidence_hash覆盖实际客观证据，不含note/why/display；question_spec_hash覆盖完整语义问题/criteria/candidates及构建版本；resolved_model_id记录实际响应；policy版本只负责决定/展示规则。requested/resolved都留存，alias变化不自动沿用校准。

canonical JSON、Unicode/换行、array顺序、null/空规则以Go/TS共享golden vectors验证。复用须实际state/问题/候选/model一致，不能只看ID；保守整批cache优先。部分重评需依赖闭包和coverage，跨evidence/model或缺答案不能伪装完整run。只改label重显、阈值重放；问题或有效来源变化受控新推断。

### 3.3 数据与隐私

最小实体：可恢复evidence snapshots；不可变question specs；追加runs；policy decisions；人工overrides/events；用于查询的current projection。table/受约束JSON均可，不强迫大量拆表。run保存typed分布、usage、来源/问题/模型引用、时间、幂等和coverage，job不作唯一历史。只有hash不能复现。

blocks角色包括primary、author_continuation、quoted、external_article、third_party、legacy_unknown；URL/作者关系有依据，缺失不编造。客观请求物理排除note/why/status/stance。source-first，模型不覆盖原文，reading不覆盖分类。

D1与提供方预算实施时核实，大快照必要时引用现有受控存储；限字节/rune/估算token区别明确，不静默截断。审计不可变不阻止用户删除及保留期清理；删除覆盖snapshot/run/event/entity/cache，仍被当前decision引用的输入不可清掉后假称可复现。公开Issue/PR/日志/fixture/截图不含私人原文、备注、标注或密钥。

### 3.4 语义及人工覆盖

topics、多选content_functions、carrier、affordances、必要有序程度各自独立；用户intent/status/stance人工或明确独立建议，不能从作者/收藏/引用推断立场。Noul概率不是程度/重要性，Choice只互斥，Score等级具体且保留分布。confidence来自分布而非独立正确率，不默认乘p。

字段决定accepted/rejected/abstained；任务未运行/失败/stale与not_applicable/out_of_taxonomy/insufficient_evidence原因分开，原因有观察或专门判断支持，unknown允许；合法none/empty可completed。请求安全上限、有效标签上限、卡片显示数不同，第四topic不因展示三项被删。

unset继承自动；set []明确为空；accept/reject覆盖某标签；reset只移除明确范围的覆盖。operation ID+expected curation revision保证CAS/重试幂等，why/status不生成accept事件。source变化保留人工历史并说明适用范围变化；policy replay不撤销reject。legacy整组curation标来源/确认行为未知，不自动作gold。

### 3.5 v1兼容

旧六字段/旧include=enrichment/NAS原key/type稳定；v2 opt-in协商，strict解码收到相应shape，cache区分representation。v1投影不表示隐藏数据不存在。

旧PATCH仅能影响可表达部分，不能抹第四topic、隐藏多选、reject、实体或无关人工字段。没有revision的旧协议不能凭空提供CAS语义；可无歧义范围适配，否则保护数据并返回可操作冲突，不猜用户意图。v1 classification:null保持旧恢复自动范围，不扩大成删全部v2个人记录。

### 3.6 有界扩展

实体候选提取有真实span/URL，Jev只有限验证与对齐；not_run/failed/empty/stale分离，人工优先。只有明确材料缺口才补证据，来源块追加不覆盖原帖；真实外链适配器有allowlist、实际连接IP/DNS重绑定/私网/metadata、逐跳redirect、MIME/大小/超时防护，不带凭据跨域。

检索先召回过滤再同rubric重排，预算/cache/失败回原序，分页固定候选或明确当前范围，不能声称补回未召回内容。词表提案需人工审批、语义diff/mapping/版本与影响dry-run，不自动造标签。每能力单独flag默认off，质量门槛不满足不晋升，工程实现不能只留接口。

## 4. 强制验收场景

| ID | 必须验证的结果 |
|---|---|
| SC01 | A/B旧新policy交替至少20轮：完成任务不循环领取、不无限重置attempt |
| SC02 | 推断途中源revision/目标generation变化：旧结果不覆盖当前投影 |
| SC03 | 仅保存why/status且未触摸标签：无分类写入、无accept事件 |
| SC04 | 阅读输出schema不含原文/links/images，已存来源完整保留 |
| SC05 | note-only不X Search、不改客观state/hash；URL变化明确来源过期 |
| SC06 | 两topic明确一模糊：前两者生效，仅第三项弃权 |
| SC07 | none、词表外、未运行、失败、证据不足在API/UI分开 |
| SC08 | threshold/display-only禁网重放完成，模型调用计数严格0 |
| SC09 | 同均值不同Score分布保留差异，confidence不当独立证据乘分 |
| SC10 | 客观body不含note/why/stance，恶意内容不改变操作/词表 |
| SC11 | complete已成功但响应丢失：不双run/event，不重发模型 |
| SC12 | 配置/契约问题按类型处理，429/过载有界，stale409非模型失败 |
| SC13 | 四个有效topic底层完整、卡片折叠、v1合法 |
| SC14 | reject/set-empty/reset-unset区别，重放不复活人工拒绝 |
| SC15 | Web/Android并发CAS不丢更新，离线重试幂等 |
| SC16 | v1读写不破坏隐藏v2，strict解码与表示缓存正确 |
| SC17 | 引用/续帖/第三方分离，legacy unknown不编身份 |
| SC18 | 超长/截断有明确状态，字节/rune/预算边界正确 |
| SC19 | 实体not_run/failed不清同revision成功值，source变化stale |
| SC20 | 补证据防SSRF/redirect/凭据泄漏、去重且预算到界停 |
| SC21 | 重排失败原序、不越筛选权限、不冒称提高候选召回 |
| SC22 | train/dev/test线程近重复隔离，mock不冒充人工gold |
| SC23 | 空库/0009迁移通过，应用回滚不删人工和来源 |
| SC24 | 删除/保留期覆盖snapshots/runs/events/entity/cache，无私人悬挂 |
| SC25 | 普通CI/make verify不收费，公开日志/fixture/artifact无密钥私文 |
| SC26 | 部分重评仅复用同证据/模型/语义合法结果，缺coverage不假complete |
| SC27 | legacy实验可回归而不入生产Jev路径，安全合同不降低 |
| SC28 | 真实浏览器键盘/手机/刷新竞争/dirty/局部确认/只分类重试 |
| SC29 | alias模型变化不沿用校准自动晋升，spec/generation可回滚 |
| SC30 | 后台分类/补证据/重排单条及全局预算、取消/耗尽，无隐式全库 |

## 5. 测试与证据格式

核对当前仓库后运行实际命令：E `make verify`、`make test-ablation`，按影响运行`make ablation-architecture`；Worker `npm ci`、`npm test`、`npm run typecheck`、`npm run deploy:dry-run`；Android仓库当前CI等价JDK/SDK与`./gradlew --no-daemon --dependency-verification strict testDebugUnitTest lintDebug assembleDebug compileDebugAndroidTestKotlin`。新增target先实现再引用；遵循AGENTS的shnote包装，环境无工具准确说明。

普通模型mock与静态前端测试只证明相应工程行为。SQLite不能代替Worker/D1事务测试，截图不能代替浏览器操作，编译androidTest不能代替设备执行。live显式授权、样本数/调用/token预算、timeout/失败停止与私有输出，环境有key不是授权。

每批在实际实现仓库提交`docs/jev-v2/evidence/Bxx.md`，模板见[EVIDENCE](templates/EVIDENCE.md)，包含Issue/任务/PR、head/base/companion SHA、变更、失败→通过命令与退出码、SC编号、unit/integration/browser/device/live覆盖、调用计数、迁移回滚、脱敏/私有数据策略、未验证项及工程/质量/审查/合并/授权状态。总索引只保存稳定链接，不复制动态状态。

## 6. 质量门槛与最终交接

B08先冻结风险/coverage/人工负担/成本门槛，再看holdout；precision/recall、accepted错误、coverage、review字段数、candidate recall/rerank、token/调用/latency都报告条件/样本/区间。缺数据inconclusive，不能用无限弃权或高confidence称质量进步，不能拿改变任务后的指标直接比较。

P0/人工保护/协议/迁移不变量必须通过；统计质量不能抵消工程失败。真实gold、设备或live授权不足记录局部阻塞，不自批完成。B10 Issue #16交FINAL-REVIEW.md，包含实际两仓SHA、全部Issue/真实代码PR、R/任务/SC证据矩阵、偏离/风险、flags、rollback、未做生产操作，并回链总控。

未经所有者后续批准，不擅自merge、转Ready、关闭实施Issues、删除分支、force-push、打tag、远端迁移、发布Worker/NAS/App或收费回填。DeepSeek是实现者，不是生产供应商替换要求。当前10个计划PR的迁移关闭仅由本轮用户授权执行，不构成以后任意关闭实现Issue的授权。
