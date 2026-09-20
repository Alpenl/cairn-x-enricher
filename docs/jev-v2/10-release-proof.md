# B10 / 跨仓库集成、迁移回滚与最终复审证据

状态：待执行，**不是发布授权**。总计划#3；base B09 #8；前置为B01–B09全部工程实现，配套share末端为 https://github.com/Alpenl/cairn-share/pull/27 （B07）。必须锁定实际两仓完整SHA，不使用创建计划时的旧SHA冒充实现版本。覆盖R01/R18–R25/R34–R39，并总验收R01–R39。

本批不能只写“全部完成”：需要集成测试、遗漏修复、遗留代码和文档清理、可重复本地演练、任务到证据的追踪及待复审报告。不自动合并PR、打tag、发布镜像、升级NAS、迁移远端D1或收费回填。

## 任务

- [ ] B10-T01 汇总实际分支与依赖：同步同仓上游实现，固定companion SHA、schema/spec/model/policy版本和feature flags；检查PR diff只包含本批或明确依赖。已有无关个人改动与依赖更新PR保持。确认B00最新索引/执行协议已经进入最终工作树。
- [ ] B10-T02 生成完整追踪矩阵：REVIEW R01–R39 -> B01–B10任务ID -> 实现commit/文件 -> EXECUTION SC01–SC30 -> 测试命令/结果/日志引用。每个任务标not_started/in_progress/engineering_done/BLOCKED_EXTERNAL；没有实现或测试证据的格子不得勾完成。总计126个批次任务；若批准拆分，保留原ID与后继映射，不减少范围。
- [ ] B10-T03 在隔离的本地Worker/D1/R2、Go服务和模型mock运行完整source-first链路：新收藏、已存原文、人工原文、reading失败、classify失败、retry、policy replay、source refresh、实体/补材料。断言写入顺序、租约、revision、调用计数、人工覆盖及缓存更新，不使用真实私人生产数据。
- [ ] B10-T04 并发/故障注入：旧新consumer交替、同spec多实例领取、target切换、source更新、lease过期、完成响应丢失、重复提交、网络超时/401/422/429/过载、shutdown中断。证明不来回重跑、不重复收费模拟调用、不覆盖当前投影、不丢人工事件，配置错误不会耗尽所有任务。
- [ ] B10-T05 完成跨协议集成矩阵：旧六字段App、旧enrichment v1、新v2 App、Go strict decoder、NAS Web、Android当前实现，各自对旧/新Worker和flag on/off。真实浏览器与可用设备运行字段操作/冲突/dirty表单/分页/导出；设备缺失单独标明，不用编译成功代替执行。
- [ ] B10-T06 执行本地迁移演练：空库、0009历史库、含legacy curation/停用标签/已有实体/活跃lease的数据集，应用新增迁移后核对row counts、source hash、人工值、版本映射；模拟应用回滚/目标回滚。不得改已发布迁移，不做破坏性down迁移，不以清空数据库规避兼容问题。
- [ ] B10-T07 执行隐私和安全验收：删除收藏及保留期清理覆盖snapshot/run/event/entity/cache；令牌权限、日志和公开artifact脱敏；恶意URL/重定向/DNS目标/超大内容/HTML注入与预算耗尽。扫描本组diff、fixture、截图和CI产物，私人内容不能因“测试需要”被公开。
- [ ] B10-T08 验证无调用重放、实际输入budget和有界任务行为；使用B08工具生成baseline/candidate工程报告、真实质量报告或明确的质量阻塞。列每个feature flag是否实现/测试/有真实收益/可默认启用，未验证扩展保持off。记录数据规模和p50/p95条件，不以小fixture声称大库性能达标。
- [ ] B10-T09 清理生产耦合与同步文档：确认ReadingResult独立、Evaluate/Decide/Resolve边界、taxonomy不再混入旧整包生成职责；legacy Generate/Workflow保留可运行实验但不入生产路径。更新README、architecture、jev-classification、bookmark-management、deployment/cloudflare-backend、CLI/config说明和CHANGELOG；历史研究文档原样保留并清楚标历史，不能重写成当前事实。
- [ ] B10-T10 编写部署与受控回填runbook，仅准备不执行：先兼容Worker/新增迁移，再Go/Web与Android，再获批准的spec/flags切换；备份/恢复验证、版本兼容表、目标generation回滚、观察指标、停止阈值、明确ID/cursor范围及dry-run预算。收费live、远端迁移、NAS镜像/生产切换各需明确授权，不因用户要求完成代码而默认授权。
- [ ] B10-T11 完整门禁：Enricher make verify、test-ablation及受影响architecture回归；Worker tests/typecheck/deploy:dry-run；Android仓库CI等价unit/lint/build；新增契约/浏览器/迁移/故障测试。所有命令实际存在且执行后记录退出码、完整SHA、运行环境与安全日志/CI URL。修复失败后重跑受影响矩阵，不删除断言或跳过测试凑绿色。
- [ ] B10-T12 提交evidence/B10.md与FINAL-REVIEW.md：全量矩阵、关键diff导航、各PR最终head/base、companion SHA、运行结果、设计偏离及依据、质量未验证项、剩余风险、开关状态、未执行生产操作。向总PR发布交接评论；所有PR仍Draft/未合并，等待所有者及后续独立审查，不能自己批准自己上线。

## 最终不变量必须全部通过

| 类别 | 需要的实证，不接受只有说明 |
|---|---|
| 目标权威 | A/B交替至少20轮，已完成目标不被旧consumer再次领取；generation只由受控目标更新变化 |
| 写入一致性 | input/spec/target/lease任一过期，旧结果不能覆盖；幂等重复不双写run/event |
| 人工主权 | why/status保存不接受标签；reject/empty/reset区分；重放或旧客户端不清隐藏选择 |
| 语义分层 | 主题/功能/载体/潜在用途/个人意图分开；合法none不是失败；第4标签不丢 |
| 成本边界 | threshold/display-only调用计数0；补材料/重排各有预算与失败降级；普通CI零付费 |
| 证据真实性 | 每个支持依据可回到真实block/span；legacy未知不伪造；实体候选不凭空生成 |
| 协议兼容 | 旧shape/type稳定；v1写入不抹v2；strict decoder和representation缓存正确 |
| 迁移回滚 | 历史原文/人工数据保留；回退应用或目标无需删除新增历史；flag关闭可用 |
| 质量诚实 | mock与人工gold分开；模型/spec/dataset一致；样本不足报告inconclusive |
| 隐私安全 | 删除清理完整、公开产物无私人源文/密钥、外链抓取不越界 |

EXECUTION的SC01–SC30每项应关联至少一个可执行测试或明确外部阻塞。安全/P0/数据完整性/协议行为是工程不变量，不能用“样本总体准确率高”抵消失败。

## 提交与报告结构

建议拆提交：①集成环境/版本锁；②故障与兼容回归及必要修复；③迁移/安全/预算；④遗留边界和文档清理；⑤最终证据。若修复属于上游PR，应先提交到其head再向下同步，避免最终PR悄悄包揽上游未完成内容而追踪表失真。

`FINAL-REVIEW.md`至少包括：

```text
Review baseline and final SHA pair
PR chain and per-batch evidence links
R01–R39 coverage / 126 task statuses / SC01–SC30 results
New/changed contracts, migrations and explicit legacy mappings
Regression-before / after-fix evidence for P0
Model-call counts for replay and failure recovery
Engineering gates vs real-quality evidence vs missing external prerequisites
Active/default-off flags and exact rollback path
Unresolved risks and deviations with rationale
Production operations performed: none (unless later separately authorized and logged)
Requested next action: independent review, not automatic merge
```

## 完成与阻塞的判定

工程完成表示实现、相关单元/契约/集成测试和证据齐全，不包括未经运行的浏览器/设备用例。真实分类质量需授权gold和对应真实模型run；无数据/密钥/设备时明确局部BLOCKED_EXTERNAL，并继续完成可独立执行的任务。远端部署未经授权是独立状态，不应阻止交付可审查的代码，但也不能说“已上线”。

这组计划PR初始只有文档。绿色文档CI不能作为本节任何不变量通过的证据；执行者必须在实际实现SHA上重新运行并交付。最终复审以diff、代码、可复现测试和真实证据为准，不以总结中“全部完成”四个字为准。
