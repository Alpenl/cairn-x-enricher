# 2026-09-23 实体判断持久复用与恢复

源码 S `8f4f0ec7ed897d60e47950d6bad032fccbcd4fb0` / E `8db9f3f9ce25fcb630300f6065ebd58e81543155`；[Share Draft PR32](https://github.com/Alpenl/cairn-share/pull/32)、[Enricher Draft PR17](https://github.com/Alpenl/cairn-x-enricher/pull/17)。总控 [#10](https://github.com/Alpenl/cairn-x-enricher/issues/10)，统一验收 [#16](https://github.com/Alpenl/cairn-x-enricher/issues/16)。推进 B09-T01/T02/T04 的去重、候选身份与恢复部分，以及 SC10/SC30 和 G01，不代表整个 B09 或全部任务接受。

## 缺口与修复

旧实现有结果写入幂等和扩展日预算，但 `runExtensions` 每次分类完成都重新进行实体推断，进程重启不能复用同材料结果。用旧 E `6142692` 编译实际 CLI，连续两个独立进程重新分类同一材料，记录到 **2 次实体 HTTP，期望 1 次**：[修复前失败对照](logs/20260923-entity-cache/entity-cache-baseline.log)。这不是据静态搜索推断重复，也没有把单测 fake 当作实际调用。

现在 `serve` / `once` / `classify` 都连接 Worker/D1 实体缓存协议。key 覆盖收藏、content revision、不可变 evidence snapshot ID/hash、实际序列化 provider 请求、候选及精确原文 rune 位置、问题和本地选择策略 hash。Worker 校验实际材料与快照一致、候选确在对应 span 或当前来源链接内，固定模型沿用 `jev-1.13.0`。完整 typed Noul 原始概率持久保存，保存与命中时验证完整答案集合和 0..1 范围，不只缓存最终实体名。

首次原子 INSERT 才获得执行权；相同请求不同进程、同 owner 重放、pending/failed 均不重新授予。唯一 owner 仍需申请已有扩展日预算，缓存 claim 本身不扣额度；失败或未知结果不会自动重新调用模型。完成写入最多两次，每次失败可 GET 恢复已提交结果，收尾使用独立最多 5 秒上下文。未确认持久化不能当作成功。完整结果及 completed_empty 在新进程中复用，额度耗尽也不重新推断；没有候选时为本地确定性空结果。

结果写回实体状态使用缓存 key + 原始答案 hash 的稳定 operation key，最多三次相同幂等提交、总计最多 5 秒。首次与 hit 的状态写入省略本次 calls/tokens，避免零调用复用与首次写入 payload 冲突；调用次数和保守 token 预留仍在预算账本，运行返回值仍区分本次调用。没有完整问题/模型身份的历史 entity_states 不冒充有效命中。

人工 override、备注、阅读摘要和普通分类 decision 不属于客观实体缓存身份，因此它们不触发同材料实体推断；人工拒绝继续优先，缓存复用不覆盖它。来源、问题、候选或策略改变则另行处理。cached provider 请求与实际 Judge 请求由同一个构造器生成；这项与本批 CLI/Worker 验证共同覆盖生产入口，未引入另一条付费实现。

## 来源链接一致性修复

审查发现 0014 只对主文本/URL、0017 只对上下文变化递增 content revision；只改 related_links 时，旧有效实体仍可能显示为当前，迟到状态提交也可能通过。0028 补链接单独变化触发器，主文本变化继续由旧触发器处理；同值链接、备注修改不额外递增。新回归实际填入成功实体，再只改来源链接，证明旧实体立即 stale、有效集合为空、迟到缓存完成被拒绝，同值写入版本不变。

缓存 claim、complete 和读取都检查当前来源身份及链接；final SQL 内也检查，覆盖预检之后的材料变化。该修复保护实际状态写入口的既有 snapshot/content guard，不仅是在缓存读取前检查一次。

## 私有数据与升级

新增 **0028**，entity_cache 通过外键关联收藏和快照；删除任一所有者级联清除实际请求、候选和原始判断。全表删除验收已加入实际填充的新表，不能仅凭空表清零宣称删除完成。匿名全局预算不退款。完整合同见[扩展运行说明](../09-semantic-extensions.md)及 [Share 隐私说明](https://github.com/Alpenl/cairn-share/blob/8f4f0ec7ed897d60e47950d6bad032fccbcd4fb0/docs/privacy-deletion.md)。

生命周期自首次 claim 固定 24 小时，hit 不续期，pending/failed 也不提前重授；过期后可在预算允许时重试。部署最多 200 项，claim 和隐私 Cron 每次清理最多 100 条，当前过期 key 另在 claim 事务删除，防止扫描批次外的过期值被误用。过期即拒读，停机可能延迟物理清除。这只是实体缓存期限，不是对所有历史 runs、evidence 或 entity_operations 的保留期限声明。

先迁移并部署兼容 Worker，再升级消费者和评估实体 opt-in；缺少缓存协议或输入绑定时不回退到重复付费。回滚前关闭实体 flag，保留兼容清理 Worker/迁移。扩展仍默认关闭。本批未生产迁移、部署、切目标、merge、Ready 或关闭 issue。

## 验证

| 证据 | 准确范围 |
| --- | --- |
| [Worker 最终门禁](logs/20260923-entity-cache/entity-cache-worker-final-pass.log) | 22 文件 257 项、typecheck、deploy --dry-run 通过。新增 10 项覆盖唯一并发 owner、丢响应/幂等、完整概率、材料与 span、全部身份、人工/阅读编辑、SQL 最后窗口、过期/容量/有界清理、删除、访问/大小、链接变化的有效状态。 |
| [Go 完整门禁](logs/20260923-entity-cache/entity-cache-verify-pass.log) | vet / golangci-lint / race / 73 前端检查 / build 通过。新增消费者测试覆盖缓存不可用、pending/failed、畸形 hit、无绑定、空结果复用、未确认持久化、provider 失败不得重复调用。 |
| [全部实际跨仓服务](logs/20260923-entity-cache/entity-cache-real-pass.log) | 17 个独立 Worker/D1/R2 + Go 场景通过，包含新增五个实际 CLI 进程；只有模型提供方为本地 HTTP 夹具。 |
| [最终实际浏览器](logs/20260923-entity-cache/entity-cache-browser-final.log) | 实际 Chrome + Go serve + Worker/D1/R2，40/40。实体面板、阅读头部、人工拒绝/重置和导出走真实路径。 |

五次 CLI 场景依次验证：首次推断并故意丢弃已提交的缓存完成及实体状态响应后恢复；新进程重分类同材料不增加实体 HTTP；人工拒绝后把扩展全局额度收紧到已耗尽仍命中且保持人工结果；实际 SaveSource 和新 snapshot 改材料后产生第二次实体请求；第三份材料因预算耗尽明确 failed，不再发实体 HTTP。最后真实 App 删除收藏，三个来源身份的私有缓存均 404。另精确断言六次实体状态 POST（包括首次丢响应重试）均在 Worker 成功，无被零调用掩盖的幂等冲突。

该场景共 **5 次普通分类 + 2 次实体本地模拟 HTTP，0 次付费**，不是全部 17 场景的总调用数。当前普通分类默认不启用部分复用，因此它的五次调用符合现有设置；实体新缓存独立发挥作用。这些证据不证明真实实体 precision、canonical 误合并、p95 或模型收益，也没有据此选择上线阈值。

源码 CI：[S 35798517388](https://github.com/Alpenl/cairn-share/actions/runs/35798517388) 与 [E 35798550387](https://github.com/Alpenl/cairn-x-enricher/actions/runs/35798550387) 均在本文完整源码 SHA 成功；[S 元数据](logs/20260923-entity-cache/cairn-share-source-ci.json)、[E 元数据](logs/20260923-entity-cache/cairn-x-enricher-source-ci.json)。S 含常规 Android 门禁，E 含双架构容器构建，不等于设备或生产验收。本地 Go buildinfo `52de69b` 是带本批改动的工作区父版本；提交后的准确 SHA CI 再次通过。

## 失败记录

- [旧版本实际重复调用](logs/20260923-entity-cache/entity-cache-baseline.log)：第二个 CLI 进程后实体 HTTP=2，期望 1。对照在预期的前两步失败，未进入后续变更场景；临时测试覆盖文件已清理。
- [首次 Go 回归](logs/20260923-entity-cache/entity-cache-go-first.log)：新增完整集合校验先报告 incomplete，使旧未知候选诊断断言失败；保留精确 unknown candidate 诊断并继续完整性验证。
- [首次真实 CLI](logs/20260923-entity-cache/entity-cache-real-first.log)：夹具人工实体路由多写 /overrides，改为实际 /entities；前两次 CLI 已证明复用。
- [第二次实际 CLI](logs/20260923-entity-cache/entity-cache-real-second.log)：夹具只改快照未保存新主原文，既有主文一致性守卫正确拒领；补真实 SaveSource 流程，[随后通过](logs/20260923-entity-cache/entity-cache-real-third.log)，没有放宽生产守卫。
- [首次 lint](logs/20260923-entity-cache/entity-cache-verify-first.log)：新代理夹具 SSRF taint 和状态常量问题；改为校验 loopback 的标准反向代理及命名常量，未禁用规则。
- [首次 Worker 全量](logs/20260923-entity-cache/entity-cache-worker-final.log)：私有表枚举发现新增 entity_cache 未加入已填充删除集；补真实记录和清零断言。
- [链接版本修复后回归](logs/20260923-entity-cache/entity-cache-worker-pass.log)：旧“仅阅读编辑”夹具同时清空来源链接，却期待内容版本不变。该场景改为只编辑译文与图片，保留原精确版本断言；链接变化由新增独立测试验证 content revision/stale/迟到拒绝。

## 完整剩余范围

[原始 235 个编号](B10-20260923-scope.md)保留。此前普通分类预算、扩展预算、重排缓存与本批实体去重补齐了主要重复调用工程缺口，整条款仍须按全部证据复审。有限 canonical 匹配、实体类型/来源的完整产品表示、词表治理、所有历史保留、迁移兼容、Android、自动质量与独立最终审查等 G02–G11 未因本批通过而省略。

所有者已免除人工标注依赖，质量使用有来源的 `automatic_reference`，不冒称 human gold。本批未用冻结 dev/holdout 推理、拟合或质量评分。新增付费 **0**，累计 **211**。仍是工程交付，不自行接受整个 B09/G01 或全目标。
