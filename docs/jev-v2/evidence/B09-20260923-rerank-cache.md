# 2026-09-23 重排持久缓存与恢复

源码 S `47662e34155bb44f1c07616daba1f45dec416c22` / E `b6f9043d110bd52ff3d11c181b9f3423c1499232`；[Share Draft PR32](https://github.com/Alpenl/cairn-share/pull/32)、[Enricher Draft PR17](https://github.com/Alpenl/cairn-x-enricher/pull/17)。总控 [#10](https://github.com/Alpenl/cairn-x-enricher/issues/10)，统一验收 [#16](https://github.com/Alpenl/cairn-x-enricher/issues/16)。本批推进 B09-T01/T09/T10、B05-T09、B10-T07 及审计 G01；不是全部条款接受。

## 修复与边界

[上批预算修复](B09-20260923-budget.md)已限制每日调用，但真实生产入口没有结果缓存，同查询仍可再次付费。现在 `serve` / `classify` 都接入 D1 持久重排协议，实际 dashboard handler 使用相同 Service。没有缓存支持的旧 Worker、非法确认或缺失候选版本不会调用模型。

新增迁移 0027 和仅 enricher 可用的 claim/complete/get 端点。每个 key 的 pending 首次 INSERT 才发放执行权；不同进程并发、同 owner 重放和失败项均不重授。只有新 owner 才可进一步请求上一批的全局/逐条预算；成功 hit 不消耗模型额度。模型失败或未知结果无自动模型重试；完成提交最多重试相同幂等写入两次，失败时 GET 恢复已提交结果。保存拥有独立最多 5 秒取消后收尾窗口，不能把未确认持久化的结果显示为成功。

key 覆盖精确 provider JSON（model、query、材料、同一 Score rubric）、filter/page/limit hash、spec hash、候选顺序与每条 source/body/personal/latest decision/entity revision。原始 typed answers 和分布持久保存；不是只存排序名次。固定 `jev-1.13.0` 预检及实际模型身份检查沿用，不支持 alias 静默复用。缓存 claim/complete 的 SQL 内核对全部当前版本；读回也核对。Score 题目仍是同一分级相关性标准，没有改成互不相关的 Noul 比较。

真实 UpdateCuration 测试发现旧 why 编辑不递增 personal revision，初稿因此错误命中缓存：[首次实际失败](logs/20260923-rerank-cache/rerank-real-first.log)。0027 补 note/why/curation_status 的个人版本触发器，以及 title/summary/enrichment status/URL 的 body 版本触发器；同值写入不递增，人工内容不被覆写。后续同一条实际编辑测试不再命中旧结果。这是实际写路径发现的遗漏，不以手动加版本的单测代替。

API 接收可选 URL 编码 filters，沿用收藏列表验证；候选材料为 `reading_summary` 标明的 AI 标题/摘要，最多 20 条、每条 400 字符。响应 `scope=current_candidates` 明确只重排当前筛选页，携带原始 `next_before_id`。没有全库相关性排序或冻结全库分页的保证，也未新增默认启用的前端控件。任何结果返回前重新读同一筛选页；集合、文本或版本改变就显示当前原序，复读失败不返回旧候选。最后检查后仍可能发生新的编辑/删除，不能承诺响应发出后可撤回客户端已收到的数据。

## 私有数据与升级

私有 D1 保存实际 query/provider 请求和答案，不含凭据；公共报告仅合成夹具日志和请求 hash，不含真实收藏。生命周期自首次 claim 固定 24 小时，pending/completed/failed 都不续期；期间未知结果不重新授予，过期后才能在预算允许时再尝试。硬上限 200 项，请求与五分钟 Cron 各清理最多 100 过期项；claim 在同一事务额外清除其当前过期 key，避免它落在扫描批次外时误用。过期即拒读，物理清理受停机/队列影响，不承诺严格 24 小时物理清除。

删除任意关联收藏会在同一 D1 事务删除整条多候选缓存（请求、答案及所有引用），其余收藏保留，匿名全局预算不退款。旧版本缓存不可读，历史失效行在期限内清理；不把这项局部期限宣称为所有历史 run/evidence 的保留策略。

先迁移 0027，再升级兼容 Worker 与消费者；回滚前关闭扩展并保留兼容清理 Worker。全局 `TYPESAFE_MODEL` 从 alias 改为固定版本时，还须按 B01 desired target / new generation 受控变更，不能自动激活。详见[扩展运维合同](../09-semantic-extensions.md)和 [Share 隐私合同](https://github.com/Alpenl/cairn-share/blob/47662e34155bb44f1c07616daba1f45dec416c22/docs/privacy-deletion.md)。本批未执行生产迁移、部署、目标切换、merge、Ready 或 issue 关闭。

## 验证

| 证据 | 准确范围 |
|---|---|
| [Worker 全量](logs/20260923-rerank-cache/rerank-worker-pass.log) | 20 文件 238 项、typecheck、deploy --dry-run 全通过；新缓存 10 项覆盖并发、同 owner 重放、精确请求保存、key 变化、全部版本、SQL 内最后窗口变更、完整答案/权限、整条删除、过期扫描批次和容量 |
| [Go 完整检查](logs/20260923-rerank-cache/rerank-verify-final.log) | vet / golangci-lint / race / 73 前端检查 / build 全通过；补缓存故障无模型重试、未确认保存、pending/failed/missing version/非法 hit、筛选/游标/复读错误与删除断言 |
| [全部实际服务](logs/20260923-rerank-cache/rerank-real-services.log) | 15 个独立实际 Worker/D1/R2 + Go 场景全部通过；原 14 个场景保留，新缓存场景为 7 次独立 Go 进程 |

新缓存场景使用实际 dashboard HTTP handler（最终夹具在进程内执行 handler）、实际 Go Cairn/Jev HTTP 客户端和独立 wrangler Worker/D1；只有付费提供方是本地 HTTP fixture。没有把 Worker 或缓存/预算仓库 mock 掉。七个进程依次证明：并发 pending 不调用模型；唯一 owner 调用、完成响应丢失后 GET 恢复；进程重启且预算收紧到已耗尽仍 hit；改变 filter scope 需新结果；真实 why 编辑使旧结果失效且逐条预算耗尽后明确 failed；再重启仍 failed；删除任意候选使缓存不可读，推理中再删除最后候选时最终复读返回空集合。共 3 次本地模拟提供方 HTTP、0 次付费。首次成功还对 claim 保存的 request 与实际 provider body 的 SHA-256 做精确比较。真实页游标和 topic/source 筛选均有断言。

这不证明真实相关性质量、候选召回、p95、实际模型收益或 UI 操作体验。本批没有重跑 Android 设备或完整浏览器；普通前端检查和实际 handler/Worker 行为已覆盖所改部分。历史设备/浏览器证据仍以其各自受测 SHA 解释。

源码 CI：[S 35793040980](https://github.com/Alpenl/cairn-share/actions/runs/35793040980)、[E 35793045395](https://github.com/Alpenl/cairn-x-enricher/actions/runs/35793045395) 均在本文完整源码 SHA 成功；[S 元数据](logs/20260923-rerank-cache/cairn-share-source-ci.json)、[E 元数据](logs/20260923-rerank-cache/cairn-x-enricher-source-ci.json)。S 含常规 Android 构建门禁，不等于真机验收。Go 本地日志的 buildinfo `cb2a58c` 是未提交工作区的父版本，已包含修复；提交后的准确 SHA CI 再次通过。

## 失败记录

- [首次 TypeScript 检查](logs/20260923-rerank-cache/rerank-typecheck.log)：新测试动态索引类型错误；修正测试类型。
- [Go 测试编译](logs/20260923-rerank-cache/rerank-go-second.log)：测试尝试不存在的 CurationUpdate.Note，改用真实 Why 接口。
- [首次实际缓存失败](logs/20260923-rerank-cache/rerank-real-first.log)：why 变化仍 hit；修复真实版本触发器后通过。
- [Worker 触发器初稿](logs/20260923-rerank-cache/rerank-worker-second.log)：把 URL 派生 source 误作数据库列，导致 45 个失败；改为真实 url 列。
- [Worker 随后两处失败](logs/20260923-rerank-cache/rerank-worker-final.log)：原 App 精确增量断言需计入新增触发器；新 fixture 改完标题/摘要后意外不匹配搜索。分别保留精确 +3 与实际 D1 版本一致断言，并使 fixture 继续匹配合成 query。没有删测试或放宽访问/失效断言。
- [首次 lint](logs/20260923-rerank-cache/rerank-verify-first.log)及[第二次](logs/20260923-rerank-cache/rerank-verify-second.log)：导入组格式，以及子进程环境数据使新测试服务器的出站请求触发 G704。最终 helper 使用固定 localhost URL 直接执行实际 handler，实际 Worker/provider HTTP 保留；未关闭规则或添加忽略注释。

## 剩余范围

完整 126 B、R01–39、SC01–30、F01–14、R2-01–14、R3-01–12 共 235 项仍保留，见[固定审计矩阵](B10-20260923-scope.md)。G01 本批完成重排结果缓存的工程路径，但实体结果去重、普通主分类全局预算及整条款证明仍开放。其它 canonical/词表治理/完整历史保留/旧接口迁移兼容/Android/质量/独立复审范围没有消失。

用户已免除人工标注依赖；真实质量使用有来源的 `automatic_reference`，不冒称 human gold。本批不使用冻结 dev/holdout 进行推理、拟合或质量评分，没有选择上线阈值。新增付费 **0**，累计 **211**。所有扩展 flags 仍默认关闭。
