# DeepSeek 执行协议、契约约束与验收证据

状态：待执行规范。与 REVIEW.md、README.md 及各批次文档共同使用。以下新文件/接口/测试名称均是要求实现的目标；不能把未实现命令写成已运行。

## 1. 执行者收到任务后

1. 读取当前仓库 AGENTS.md、目录内指引、TypeSafe 项目技能、总计划及本批规格。保留无关改动，不覆盖依赖更新 PR，不修改密钥或权限配置。
2. 将 cairn-share 与 cairn-x-enricher 放到相邻目录。核对两仓 main、当前 head/base、工作区和 PR diff；记录 SHA。不得假设创建计划时基线仍未变。
3. 保存未改动基线测试结果；v1 推断样本只有已获授权且已存在时才归档到私有目录。准备去重分组、数据授权和人工标注说明，不将用户收藏贴进公开PR。
4. 按 PR-INDEX 的 head branch 工作。本组是堆栈 PR：下游先 merge 对应上游最新 branch 获取实现，并记录同步提交；不 force-push、不改已有个人提交、不自动合并PR。跨仓库依赖固定到完整SHA，记录到版本受控的测试锁文件（不得包含部署密钥）。
5. 每项按“检索现状→失败测试→实现→局部测试→完整测试→证据”推进。行为变更之前先看到对应回归测试失败；重构纯函数则先冻结v1 fixture证明兼容。无法先失败的情况明确原因。
6. 任务不够小可在本PR内分多个提交；若必须拆额外PR，在总矩阵登记继承关系、未完成项和依赖，不能丢范围。不能把TODO、返回空数组、mock成功或被跳过的测试当实现完成。
7. 每项有提交和测试证据才能勾选。独立可完成工作继续做；缺密钥、gold、设备或授权时明确 `BLOCKED_EXTERNAL`，不伪造验证，不为了避免阻塞擅自消耗或部署。

本轮 DeepSeek 负责代码执行，不要求改变生产模型供应商。所有收费模型测试必须显式 opt-in、样本上限、调用/令牌预算和失败停止规则；普通测试和CI禁止访问收费端点。

## 2. 一致性规格（所有批次共用）

### 2.1 权威目标与任务身份

Worker 保存 desired target，至少包含不可变 spec ID/hash、requested pinned model、policy ID 和单调 generation。job领取时绑定 source/content revision、personal revision（若该任务需要）、目标 generation、lease token/expiry。消费者只声明支持的协议/spec/model，不得提交自身policy让Worker重写全库目标。

配置错误是组件状态，不是每条收藏的五次失败。旧消费者在新协议启用后应被明确拒绝或安全空闲，不能领取v2任务。灰度将限定任务显式绑定目标spec；回滚也是新generation指向旧spec，不减少generation。

完成事务必须同时满足 lease、未过期、输入revision、目标generation/spec和任务状态。重复的同一完成请求返回同一结果；相同idempotency key却不同payload返回冲突。过期结果可按策略留审计，但不得改当前投影。response丢失时查询run/operation确认是否已提交，不能直接重复付费推断。

### 2.2 版本、hash 与重用

`evidence_hash`：规范化后的实际客观证据，不含note/why/display。`question_spec_hash`：完整语义问题、criteria、候选及构建器版本。`resolved_model_id`：实际服务返回的模型。`decision_policy_version`：选择和展示投影规则。仅display标签变动不改变客观语义hash。

实现必须写明 canonical JSON 编码、Unicode/换行处理、array顺序和null/空值规则并以Go/TS共享golden vectors验证，不能分别随意JSON stringify。hash一致只能在实际发送state、全部相关候选和问题、模型解析一致时复用。问题级缓存至少保守包含完整batch/state hash；未证明批次变化不影响含义时不能按question ID跨batch拼答案。

优先落实整批 raw-run 可重放；只改一条定义的部分重评作为B04子任务，必须先定义依赖闭包和partial覆盖状态，缺失/跨model/跨evidence结果不可伪装完整run。服务暂不可用时不静默换模型并沿用旧校准。

### 2.3 数据层最小实体

建议以新增表/受约束JSON实现，不强制过度拆表：

- source snapshot / evidence snapshot：可恢复正文块、角色、URL、抓取信息、完整性、内容revision；旧payload无法判断的字段标unknown。
- question spec：不可变内容、hash、模型及协议兼容信息、语义/显示版本。
- classification run：输入/问题引用、原始typed answers、requested/resolved模型、usage、时间、错误类型、覆盖范围与幂等ID。
- classification decision：run引用、policy版本、字段决定、原因码、人工覆写前建议；policy-only重放追加记录。
- field overrides / curation events：明确操作、字段/tag、来源revision、run/decision、前后差异、事件ID和并发revision。
- current projection：供列表读取的materialized有效视图；与run/decision提交在同一原子边界内，缓存失效同步。

不要求把所有结果规范化成大量行。D1行/请求/语句上限实施时核实；大型原始快照采用已存在且授权的存储方案，不盲目复制每次prompt原文。审计不可变不阻止用户删除、保留期清理或备份清理。可检索的来源正文不得泄漏到公开fixture、日志、error body、截图或CI artifact。

### 2.4 v2 语义约定

客观维度：topics、多选content_functions、carrier、可选actionability/depth等有序判断。affordances是潜在用途，用户intent/status/stance独立；stance仅人工明确记录，引用内容不代表用户赞同或反对。

每维度状态区分未运行/成功/失败/过期，每项决定为accepted/rejected/abstained；原因码必须有可观察或专门判断支持。`none/not_applicable`可正常完成，不自动变全局uncertainty。所有完整分布可审计；默认UI不展示大串概率。高Noul不等于主题更重要；没有比较深度信息时使用稳定显式展示规则。

选择上限分三个：provider/request安全上限、有效结果策略上限、默认卡片展示数。语义结果不因卡片只显示3个而被删除。所有上限集中定义，Go/TS/UI/Android以同一合同验证。

### 2.5 人工覆写的操作语义

`unset`继承自动结果；`set []`明确无标签；逐标签`accept/reject`覆盖该标签；`reset field/tag`只移除对应覆写。人工决定优先于AI；保存why/status不写分类，普通浏览不生成接受事件。

事件带唯一operation ID与expected curation revision，支持CAS冲突提示及幂等重试。正文改变后保留人工历史，当前视图说明其来源已变化，不自动篡改或删除用户选择。更新policy不得撤销人工拒绝；清空拒绝必须显式操作。历史整组curation转成legacy override，来源unknown/review provenance unknown；不得标记为可靠新gold。

### 2.6 v1兼容与双写

旧六字段、旧include=enrichment和NAS旧接口保持原key/type。v2必须协商/opt-in，Go strict decoder不会收到意外字段。新旧cache key包含representation版本；不同投影不能共用错误缓存。

v1投影只表达旧topics/form/use，不表示v2信息不存在。旧客户端PATCH操作仅影响它能表达的维度：不得抹掉隐藏的第四个topic、v2多选功能、未展示的拒绝或实体状态。无法无歧义映射的旧写入应保持原语义并返回可操作兼容冲突，不能悄悄清空。`classification:null`的v1既有含义要文档化，不扩展成删除所有v2个人数据。

## 3. 强制验收场景（SC编号，批次需引用）

| ID | 场景与必须验证的结果 |
|---|---|
| SC01 | A/B旧新policy交替请求20轮：已完成任务不来回领取，不无限重置attempt |
| SC02 | 源revision/目标generation在推断途中变化：旧结果不覆盖当前投影 |
| SC03 | 保存why/status且未触摸标签：无分类写入，无accept事件 |
| SC04 | 阅读请求schema不包含original_text/links/images输出；已存源文仍完整保留 |
| SC05 | 改note不发X Search、不改客观state/hash；更改URL明确使来源过期 |
| SC06 | 两个topic明确、第三个模糊：前两者可生效，仅第三个局部弃权 |
| SC07 | none、词表外、未运行、失败、证据不足在API与UI中互不混淆 |
| SC08 | threshold/display-only重放在网络被禁情况下完成，模型调用计数严格为0 |
| SC09 | 同均值不同Score分布保留差异；confidence不被当独立证据相乘 |
| SC10 | 客观请求序列化完全不含note/why/stance；恶意内容不改变命令/词表 |
| SC11 | commit成功但响应丢失：重复提交不会生成双事件、双run或重发模型 |
| SC12 | 401/契约错误组件级停止，429/过载有界退避；stale 409不记模型失败 |
| SC13 | 四个有效topic：底层保留，卡片折叠，v1投影仍合法 |
| SC14 | reject/set-empty/reset-unset语义区分；policy replay不复活人工拒绝 |
| SC15 | 并发Web/Android编辑发生CAS冲突，不静默丢更新；离线重试幂等 |
| SC16 | v1客户端读取/写入不破坏隐藏v2维度，strict decoder与缓存协商正确 |
| SC17 | 引用/续帖/第三方评论分离；legacy unknown不被编造成有身份的证据 |
| SC18 | 超长或截断材料有明确状态；文本预算和字节/rune边界正确 |
| SC19 | 实体not_run/failed不清空同revision已有成功结果；来源变更标stale |
| SC20 | 补证据防SSRF/重定向/凭据泄漏、去重且预算到达立即停止 |
| SC21 | 重排失败降级原排序，不越过筛选/权限、不声称提高候选召回率 |
| SC22 | train/dev/test线程和近重复隔离；mock结果不冒充人工gold |
| SC23 | 新迁移从空库/0009历史库通过；回滚应用不会删除人工和来源数据 |
| SC24 | 删除收藏/保留期清理同步清理快照/runs/events/缓存，无悬挂私人内容 |
| SC25 | 普通make verify/CI不调用付费端点；日志、公开fixture和artifact无密钥/私人正文 |
| SC26 | 增量重评只复用同证据/同模型/同规格合法结果；不足覆盖不得假装complete |
| SC27 | legacy Generate/实验仍可回归，但不进入生产Jev路径；已有安全契约不降低 |
| SC28 | 真实浏览器检查键盘/手机/刷新竞争/dirty表单/局部确认/分类单独重试 |
| SC29 | 模型alias解析变化不沿用旧校准自动晋升；固定spec/spec generation可回滚 |
| SC30 | 后台重分类/补证据/重排有全局与单条预算、取消/耗尽状态、无暗中全库任务 |

## 4. 测试命令与环境

基线已存在的Enricher命令：`make verify`（vet/lint/race tests/frontend/build）、`make test-ablation`，按改动运行 `make ablation-architecture`。仓库要求shell命令通过 `shnote --what "..." --why "..." run <command>`；工具不可用时记录环境差异，不伪造已遵循。

Worker基线：在worker目录 `npm ci`、`npm test`、`npm run typecheck`、`npm run deploy:dry-run`（仅构建，不是远端部署）。Android基线CI：JDK17及仓库指定SDK，`./gradlew --no-daemon --dependency-verification strict testDebugUnitTest lintDebug assembleDebug compileDebugAndroidTestKotlin`。不要为本轮顺手升级所有依赖。

各批次需要新增contract/eval/browser等可重复命令时，先实现target和fixture再在说明中引用。Python SQLite最小证明只能补充，不能代替Worker/D1事务集成测试。静态前端脚本不能代替真实浏览器操作。设备测试没设备就标未运行，不能用编译通过代替。

CI运行成功必须关联实际两仓SHA和运行URL/日志；本组计划PR创建时可能触发原有CI，但绿色的“只改文档”CI不证明任何未来功能。

## 5. 每批交付证据

在本PR新增 `docs/jev-v2/evidence/BNN.md`（初始不存在，由执行者生成），至少包含：

```text
Batch:
Head SHA / Base SHA / Companion SHA:
Task IDs:
Changed files + rationale:
Regression before fix: command / exit / safe output:
After fix: command / exit / safe output:
Scenario IDs covered:
Unit / integration / browser / device / live coverage:
Network and model-call counts:
Migration and rollback evidence:
Public-safe fixtures / private-data location policy:
Known limitations / BLOCKED_EXTERNAL:
engineering_done:
quality_verified:
deployment_authorized: false
```

为关键不变量提供真实断言，例如模型mock调用计数0、SQL状态revision不变、事件数量、旧返回值shape。不能只填“已完成”“人工检查通过”。真实gold与模型使用日志保存在授权的私有位置，公开证据只放聚合数、脱敏fixture和可重复方法。

## 6. 质量与发布门槛

B08先冻结评估协议再调参。最少报告基线与候选的每维precision/recall、自动coverage、accepted-error rate、review fields/bookmark、找回Recall@K/nDCG、p50/p95延迟和token/调用成本，并给样本数及区间。若某指标样本不足，标inconclusive，不捏造阈值。质量门槛由初始基线和风险设定，写入版本化配置后才看holdout；不能在holdout上选最好结果再称独立测试。

P0/人工数据保护/兼容/迁移不变量不允许统计性失败；必须全通过。真实效果缺数据或未达门槛时扩展默认off、不得自行晋升。完成代码与离线工具并记录阻塞，不强行声称全部质量目标达成。

B10交付总验收矩阵与复审说明，保留Draft/未合并。只有所有者后续明确批准才能merge、tag、远端迁移、部署或收费回填。最终复审应能从PR链接直达commit、tests、diff和未完成项，而不是只依赖执行者叙述。
