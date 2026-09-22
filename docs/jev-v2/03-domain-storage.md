# B03 / 可重放数据与人工事件：设计规格

任务与实时进度：[S #29](https://github.com/Alpenl/cairn-share/issues/29)，B03-T01–T14全文在Issue。前置B01目标合同，与B02核对握手；先冻结fixture供B04开发，不等B05公共词表。总控[E #10](https://github.com/Alpenl/cairn-x-enricher/issues/10)。

## 领域模型

以新增表或受约束JSON实现EvidenceSnapshot、QuestionSpec、Run、Decision、Override、CurationEvent、CurrentProjection，不要求过度拆表。ADR明确大小/枚举/null/空/未运行/错误码，Go/TS canonical JSON和hash golden vectors一致。

快照blocks有ID/角色/text/URL/关系依据/取得方式/时间/完整性/truncation；原帖/作者续帖/引用/外链/第三方分开，旧context无法确认标legacy_unknown。可恢复实际state，不仅存hash。content revision与personal/curation分离，客观不含note/why/status。

QuestionSpec不可变并保存完整问题、模型requested/resolved，display独立；同hash不同字节拒绝，历史未知不填当前模型。runs追加保存实际typed分布、usage、输入问题引用、幂等ID、attempt、时间和coverage；job仅管队列。decision绑定run(s)/policy，重放不新建模型run，partial不假装完整。

run/decision/job/projection/cache提交要原子，检查lease/input/target/spec；同key同payload幂等，不同payload冲突，旧结果不改有效投影。人工accept/reject/set-empty/reset按expected revision做CAS、operation ID去重；why/status无accept事件，reject在重放后保留。legacy整组人工结果迁移为来源/确认行为未知，不自动当gold。

## 失效与生命周期

| 变化 | 必须行为 |
|---|---|
| display | 只重显，source/run/人工不变 |
| 阈值policy | 复用run，新decision，0推断 |
| 问题含义 | 受控重评依赖闭包，旧建议可stale |
| 内容/URL | 新content revision，旧run失效，人工保留并标来源变化 |
| note/why | 客观state/hash不变，只个人维度 |
| 人工reject | CAS事件与确定性resolve |
| 模型版本 | 新评估后受控切换，人工不变 |

内部API必须实际实现target/spec、claim、run提交查询、replay、decision/override、curation操作、分类队列cursor。扩展存储含实体独立状态、补证据追加、权限受限候选、预算/去重。历史登记/遍历必须有界dry-run且不默认收费。单次逻辑输入变更不能因触发器误增多次revision。

删除和保留期覆盖snapshot/run/event/entity/cache；仍被当前decision引用的输入不能清掉后假称可重现。核实D1上限，必要时引用现有授权存储。只新增迁移，不改0009，不破坏性down，不公开私有fixture。

## 验收与交付

并发重复complete仅一个run；source变更与complete交错安全；replay的source/run计数不增；CAS/empty/reset/reject正确；旧写不清v2；错spec/model/coverage拒绝；删除完整；空库/历史库迁移和应用回滚；大小与非法JSON边界。实际Worker/D1测试不能全由mock替代。

SC02/05–08/10–11/14–19/23–26/29–30；R01/R08/R09/R15/R16/R18–R21/R25/R28–R30/R37/R38。运行Worker门禁，提交evidence/B03.md、合同fixture/向量和companion SHA。实际PR可按领域合同、历史记录、人工操作、迁移拆分，进度只在Issue。
