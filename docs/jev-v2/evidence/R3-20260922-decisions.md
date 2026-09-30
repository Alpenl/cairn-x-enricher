# R3-12 / 2026-09-22 决定事务 CAS 与完整运行引用

总控 [E #10](https://github.com/Alpenl/cairn-x-enricher/issues/10)，主责 [S #29](https://github.com/Alpenl/cairn-share/issues/29)，协作 [E #12](https://github.com/Alpenl/cairn-x-enricher/issues/12)，统一验收 [E #16](https://github.com/Alpenl/cairn-x-enricher/issues/16)。这是 R3-12 局部工程证据，不代表所有原 R/B/SC 或质量门槛通过。

## 受测代码

- Share：`3b93f172d2e14552dde251b63dc078a0728d2f21`；Enricher：`9fe17ab2791ed122b4c31a25b5b78aea8f0097d6`。
- 实现仍在 Draft [S #32](https://github.com/Alpenl/cairn-share/pull/32) / [E #17](https://github.com/Alpenl/cairn-x-enricher/pull/17)。证据文档在后续单独提交，远端 CI 按最终 HEAD 登记 #16。
- 新增迁移 `0021_decision_run_references.sql`，仅本地迁移验证，没有生产迁移/部署/合并。

## 修复

原实现只在事务外检查 `expected_revision`，最后 guard 只检查第一条 run；身份比较使用 requested alias 而非实际 resolved model，存储也只保留第一条引用。已成功操作的查询在当前版本检查之后，后续人工/来源变化会使合法重试失败。投影重建只守卫人工版本，同人工版本下两个 policy decision 的较早缓存可能覆盖较新结果。

现在新决定的最终事务固定 content revision、personal revision、当前 target generation，并重新检查 **每一条** run 的所属收藏、status/coverage、spec/hash、实际模型、请求模型、目标与 snapshot/source/wire 身份。使用最多 64 个唯一正整数 run IDs 和 JSON membership，避免参数数随全部字段×run 数增长。所有引用必须 succeeded/complete、同 spec、同实际模型、同原始材料 hash 和相同 bounded wire hash；有绑定的快照必须属于该收藏并匹配 revision/hash。多条未知输入不能被认证为同一输入；单条历史未知来源允许保守兼容重放，字段继续 unknown。

同 alias 对应不同实际版本拒绝合并，即便调用者未提供 resolved_model；不同 alias 对应相同实际版本可兼容。新 payload hash 绑定 run 集合、policy、automatic 以及声明的 spec/hash、requested/resolved model、content/CAS 字段。精确操作查询先于可变状态检查；已成功动作在后续人工、材料、目标变化后仍可确认原 decision ID，不创建第二条决定。异 payload 仍冲突；并发同操作也只插入一次。响应的 `revision` 是原接受版本，`effective` 明确为当前人工解析视图。

新决定保存完整有序 `run_ids`，同一 INSERT 的触发器写 `classification_decision_runs` 外键引用。正常分类 completion 自动保存 singleton；显式 policy replay 保存全集合。每条引用包括非首条都保护对应 run 不被保留期清理误删；删除收藏正常级联移除决定、引用和私有历史。历史行只能证明主引用，迁移保持 `run_references_complete=false` 和未知接受版本；不能从已丢字段补造完整集合。旧可执行程序迁移后仍能写旧 shape，触发器保留其已知主引用，继续标未知。版本 0 旧回执使用原 hash 语义，不能声称过去没有保存的 CAS 字段已被认证。

所有缓存写入共同守卫 personal/content revision 和最新 decision ID，使用同一次计算的 automatic/effective；失效时最多三次重新读取并计算，旧计算不能覆盖新缓存。Go 增加真实严格解码 `GetLatestDecision` / `StoredDecision`，读回完整引用及 unknown 标记；CLI/UI 的 policy replay 同时提交记录的 spec hash 和实际模型版本。

## 实际验证

| 验收路径 | 已执行的断言 |
|---|---|
| 真实 D1 最后 preflight 后、batch 前人工修改 | 返回 409 revision_conflict 和当前 revision；decision/reference 均 0，人工拒绝和投影保留 |
| 同一窗口变更 content、target、第二条 run 的模型/coverage/status/source/wire/spec/generation | 每种都拒绝；无决定、引用或投影成功副作用 |
| 真正 API 写入的同 alias/异实际模型及异 alias/同实际模型 | 前者不传 resolved_model 也拒混；后者成功，全部 run 引用读回 |
| 先成功，后人工/source/target 变化，再相同操作 | 确认原 decision ID/引用/接受 revision；当前 effective 保留人工拒绝；改 CAS/content/spec/model/automatic 冲突 |
| 并发同 key；延迟投影期间写较新 decision 或人工动作 | 仅一个同 key 决定；effective、current_projections、link_selections_v2、实际过滤结果一致 |
| 旧库迁移及旧写入 shape | 已知 singleton 保留，不伪造全引用/历史 CAS；非主 run 外键保护和收藏删除级联通过 |
| 真实 Go→Worker/D1/R2 生产处理器 | 3 次正常分类的实际存读；正常 completion singleton、纯 policy replay 多 run 引用、实际 alias 漂移拒绝、提交成功后丢响应再恢复；人工拒绝保留，decision 不增加模型调用或 run |

新增 `worker/test/decision-atomicity.test.ts` **18 项**；只注入调度时间，所有 SQL 和 batch 都在实际本地 D1 执行。生产跨仓 `TestLocalWorkerDecisionReferences` 没有预制完美历史：先走正常 claim→bound snapshot→classifier→complete，再用正式 Go 客户端提交/读取决定。付费模型边界替换为本地 HTTP 合同服务，该场景 **3 次本地调用**，本轮 **0 次付费调用**。

| 命令 | 结果 |
|---|---|
| Share `npm test` | 166/166，通过 |
| Share `npm run typecheck` / `npm run deploy:dry-run` | exit 0；构建不部署 |
| Enricher `make verify` / `make test-ablation` | exit 0；含 race/lint、73 前端检查和生产构建 |
| `tests/local-integration/run.sh` | exit 0；7 个真实跨仓场景 |

日志：[Worker](logs/20260922-decisions/worker-verify.log)、[Enricher](logs/20260922-decisions/enricher-verify.log)、[跨仓](logs/20260922-decisions/integration.log)。首次新增测试误以为删除返回 200、列表字段为 links，按实际 204/items 合同修正；首次新增 Go 场景词表缺少生产必需 form/use，补齐正常词表与 typed provider 答案后复跑，未修改生产校验或放宽验收。早先六个跨仓场景仍通过。

## 映射与剩余范围

本批对应 R3-12 / R2-01、R2-12、B03-T05/T06/T07/T10/T13/T14、B04 重放和 SC08/11/26 的局部工程证据，另有 B10 并发/迁移/故障验证。Android 设备、完整原任务矩阵、R3-06 补材料所有权/恢复、R3-07 离线队列及 B08 policy 拟合/消融/固定候选检索/留出集质量验收仍需完成。自动参考基准不需要人工标注，但本批工程通过不代表质量通过；未进行新的真实模型评估。所有原任务和历史证据继续保留，最终验收统一 #16。
