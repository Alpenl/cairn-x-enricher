# B02 / Go 与 Web P0：显式确认、阅读契约、错误与幂等

状态：待实现。总计划PR #3；Worker依赖 https://github.com/Alpenl/cairn-share/pull/24 。阅读REVIEW/EXECUTION，覆盖R23–R25及R01/R19/R22/R37/R38。此head基于B00；实现前同步B00最新提交并锁定B01实现SHA。

## 范围

`internal/dashboard/reader.js`及前端测试；`internal/enrich/source.go`与相关schema/decoder/validator；`internal/processor/stages.go`；`internal/cairn/stages.go`；配置、健康和CLI。只做安全与职责清理，不在本批扩展v2词表或把所有旧workflow删掉。

## 任务

- [ ] B02-T01 基线测试：未reviewed收藏仅修改why或status，断言PATCH不含classification；现逻辑应失败。标签编辑与保存原因分别维护dirty状态，不能用整个表单dirty替代。用户明确编辑标签可算人工选择，但未触摸的AI建议不能被隐式确认。
- [ ] B02-T02 提供独立“确认这些标签”动作，普通保存只提交用户改动字段。取消/恢复自动/失败重试不能偷带classification；不改变既有人工curation的优先级。DOM测试覆盖首次保存、已确认、仅原因、仅状态、明确标签改动及网络失败。
- [ ] B02-T03 单独定义ReadingResult与strict schema，输出只含title/translation/summary及确有需要的语言字段。不得从旧enrichmentSchema删除几个属性拼装。请求不要求original_text/related_links/image_urls；程序从source snapshot构建最终Completion。
- [ ] B02-T04 拆阅读decoder/validator，不能因为省略旧字段而放宽JSON或标题/长度校验。无重复原文输出，但存档原文、链接、图片和语言来源完整保留。长文超预算不能静默截断，模型超长/非JSON/尾随内容仍失败。旧Generate/Workflow仅保留baseline/experiment路径，不进生产reading。
- [ ] B02-T05 实现B01 capabilities/target握手并绑定job规格；消费者不会在claim发送任意当前policy重置服务器目标。版本不支持显示明确组件原因，不把所有bookmark烧完重试额度。serve/once/classify/人工source调用一致。
- [ ] B02-T06 引入typed错误：configuration/auth/contract、retryable transport/throttle/overload、stale input/target/lease、already completed。实施时复核官方HTTP错误码；至少用fixture测试401、422、429、5xx、网络超时、非法响应与409区分。不能仅凭HTTP号认定所有409都可忽略。
- [ ] B02-T07 暂时错误由一个持久化退避层负责；尊重合理且有上限的Retry-After、jitter及预算。配置错误触发组件降级/暂停而非每个job重复失败；恢复有明确方式。健康状态区分source/reading/classification组件，liveness不依赖外部API。
- [ ] B02-T08 完成提交网络失败时，用同一operation ID幂等重试/查询B01确认结果；仅最终明确未执行才重新走相应步骤。stale job被替代不记模型失败，不能中断整批有效任务。避免无限WithoutCancel：每job显式deadline、shutdown等待有上限且不领取新任务。
- [ ] B02-T09 配置按CLI职责校验：classify不应因不使用的Grok配置或启动self-test失败而无法运行；serve按已启用组件验证。普通root/help命令与测试零付费调用。保持默认行为兼容并覆盖缺key/无可用source/组件暂停场景。
- [ ] B02-T10 回归source-first：Transform失败不撤销source，分类失败不调用X Search，note-only不重抓，人工原文沿用受控路径。阅读完成绝不写分类，保存why/status不产生模型调用。
- [ ] B02-T11 执行make verify、make test-ablation及受影响实验回归；真实浏览器验证保存原因无暗中确认、dirty表单不被轮询覆盖。提交evidence/B02.md及变化前后请求fixture（合成内容，无密钥）。

## 必须验证的组合

| 路径 | 输入/故障 | 必须发生/禁止发生 |
|---|---|---|
| Reader save | 只填why / 只改status | 只PATCH对应字段，classification缺席 |
| Reader confirm | 主动确认/主动编辑 | classification明确提交，保存失败保留草稿 |
| Reading | 已存原文含长文和图片 | 模型只生成阅读字段，最终source不丢失 |
| Reading | 畸形JSON/遗漏字段/尾随JSON | 明确失败，不能用旧decoder默认空值混过 |
| Classify | 缺Grok配置但Jev/Worker完整 | 只分类命令可运行且不碰Grok |
| Model | 401/422 | 配置/契约组件状态，不耗尽全队列 |
| Model | 429/过载/超时 | 单层有界退避，保留source |
| Commit | 成功后响应丢失 | 幂等确认，不再次调用Jev |
| Commit | revision/target变更 | 正常superseded，当前分类不覆盖 |
| Shutdown | 已领取/未领取 | 有界结束当前任务，不再领取 |

总场景SC02–SC05、SC10–SC12、SC25、SC27、SC28。

## 提交顺序与回滚

建议：①隐式确认失败测试和修复；②ReadingResult/schema/验证；③握手+错误类型；④幂等/shutdown/CLI隔离；⑤浏览器与回归证据。B01兼容能力未就绪时本批不可开启新协议。回滚保留存档原文和人工结果，配置开关退到安全legacy模式，不恢复旧consumer抢新任务漏洞。

禁止：为让测试过而删除严格JSON测试、让new ReadingResult仍要求回传source、将HTTP错误直接变uncertainty、用“保存整理”自动勾全标签、只写toast不修请求体。文档和fixture完成不等于功能完成。
