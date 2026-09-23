# 2026-09-23 来源复合更新只递增一次

源码 S `0ae27c858a8a3d65d23044971243785dc11e1f78` / E `57e1e5f7ed655e2636a2487057495e5e52a4f8ca`；[Share Draft PR32](https://github.com/Alpenl/cairn-share/pull/32)、[Enricher Draft PR17](https://github.com/Alpenl/cairn-x-enricher/pull/17)。总控 [#10](https://github.com/Alpenl/cairn-x-enricher/issues/10)，统一验收 [#16](https://github.com/Alpenl/cairn-x-enricher/issues/16)。对应 B03-T02 内容版本，以及迁移、事务、重复写入和跨仓证据的部分要求；不接受整个 B03/G05。

## 实际缺陷与修复

此前一次来源保存同时修改 context_text 和 related_links、主正文不变，会分别触发 0017 与 0028，各自加一次版本。实际 Worker fetch + D1 对照记录 revision **2 → 4，期望 3**：[修复前失败](logs/20260923-source-revision/source-revision-baseline.log)。该失败直接来自来源 POST，不是只检查 SQL 文本。

新增 0029 和来源 POST 调整：在一个 links UPDATE 中写入正文、私有上下文比较值与链接；统一触发器比较 URL、正文、上下文和规范化链接，一次有变化的更新递增一次。随后 archival source payload 在同一 D1 batch upsert。同步触发器写回相同上下文，不额外增版本。不同保存即便还没有新快照也各自递增，不通过快照版本上限掩盖独立变化。同值重试不递增。

旧的直接 links / source payload 写入继续触发失效保护，避免只适配当前 HTTP 路径。先单独直接修改链接、再保存新上下文的场景仍应增加第二次；不能根据旧 payload 的链接差异跳过上下文更新。无效租约完全不写入；注入归档 UPDATE 失败后 links 与 source 整批回滚。

`links.source_context_text` 仅是私有比较副本，保留原始 source payload 和不可变 EvidenceSnapshot 作为真实材料。回填只采纳 URL/正文匹配的 source；过期记录、非法 JSON、非文本 context 视为空值。迁移不改已有 content revision、不启动历史重评。App 旧/增强详情和列表不暴露字段，收藏删除时随 links 行和 source 清除。

## 验证

| 证据 | 准确范围 |
| --- | --- |
| [针对性回归](logs/20260923-source-revision/source-revision-worker-second.log) | 14 项实际 Worker/D1：三种来源字段的七种非空组合、重放、连续更新、旧写入、无效租约、事务回滚、历史回填、App 隔离与删除。 |
| [Worker 完整门禁](logs/20260923-source-revision/source-revision-worker-pass.log) | 23 文件 271 项、typecheck、deploy --dry-run 通过。 |
| [Go 完整门禁](logs/20260923-source-revision/source-revision-verify.log) | vet、golangci-lint、race、73 前端检查与构建通过。 |
| [真实跨仓服务](logs/20260923-source-revision/source-revision-real-full.log) | 18 个独立 Worker/D1/R2 + 实际 Go 场景通过。新增来源场景无模型：三次复合保存 revision 2 → 5，三次重复保存维持对应 snapshot ID/hash/revision，无效 lease 被拒绝。 |
| [实际浏览器](logs/20260923-source-revision/source-revision-browser-real.log) | Chrome + 实际 Go serve + Worker/D1/R2，40/40 通过。 |
| [页面夹具](logs/20260923-source-revision/source-revision-browser.log) | Chrome + mock Worker，57/57 通过；与上一行真实服务分开记录。 |
| [范围完整性](logs/20260923-source-revision/source-revision-scope.log) | 保留 235 个编号、原文、双向映射和 112 个固定版本文件；只证明索引完整，不是功能接受。 |

独立来源集成用例首次即通过，另存[单场景记录](logs/20260923-source-revision/source-revision-real-first.log)。所有模拟材料明确为合成 fixture，模型提供方仍仅本地模拟，不冒充真实质量。源码 CI：[Share 35800044307](https://github.com/Alpenl/cairn-share/actions/runs/35800044307)、[Enricher 35800053777](https://github.com/Alpenl/cairn-x-enricher/actions/runs/35800053777) 均在本文完整 SHA 成功；[S 元数据](logs/20260923-source-revision/cairn-share-source-ci.json)、[E 元数据](logs/20260923-source-revision/cairn-x-enricher-source-ci.json)。S 含常规 Android 门禁，E 含容器构建，不等同设备或生产验收。本地构建父版本为 92ed49b，提交后准确 SHA CI 再次通过。重复手动触发的 E 35800144851 已取消，未用于通过证据。

## 失败记录与兼容边界

- [首次针对性运行](logs/20260923-source-revision/source-revision-worker-first.log)：Wrangler 迁移分句器未识别紧贴等号的 CASE，导致 incomplete input；改为规范空格后，实际同一迁移运行通过，未绕过迁移加载器。
- [首次完整 Worker](logs/20260923-source-revision/source-revision-worker-full.log)：270 项通过、旧 0025 迁移测试失败。它加载了全部未来迁移，又逐字段比较旧表，新增私有列必然产生差异。现将该测试限制到自身目标 0025，保留全部数据/回滚断言；新 0029 历史库回填另有实际测试。
- 必须先应用 0029，再部署新 Worker；来源写入切换期间暂停旧 Worker。保留迁移做应用回滚；旧二进制可写入且会失效，但旧分步复合写法仍可能加两次，不能把新路径保证泛化到旧应用。
- 不扩大到 image URL 版本语义、不宣称所有历史保留期完成；既有 snapshot 来源读取及内容/租约一致性守卫保持。生产备份恢复和独立最终审查仍待完成，不能把本地迁移当作生产恢复演练。

## 剩余范围

完整 [235 个原始任务与控制编号](B10-20260923-scope.md)继续。有限 canonical 匹配、完整来源表示、词表治理、全部历史保留、Android、自动质量及独立最终审查等剩余任务未删减；本次仅补来源复合版本缺口。

所有者已授权无需人工标注，B08 使用可追溯的 `automatic_reference`，不称 human gold。本批未在冻结 dev/holdout 推理、拟合或质量评分；新增付费 **0**、累计 **211**。未生产迁移、部署、启用 flags、切换目标、merge、Ready 或关闭 issue，两 PR 仍 Draft。
