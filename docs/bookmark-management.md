# 收藏管理与稳定分类

> 当前生产源码的 AI 分类已改为 Jev 独立队列，词表增加语义定义，版本为
> `2026-09-20.1`。获取原文、阅读增强与分类分开，规则及第一版实体提取限制见
> [Jev 分类说明](jev-classification.md)。下文中的“同次生成/所有请求附词表”
> 描述的是原始实现；人工整理、稳定 ID 和优先级语义继续保留。

## 讨论归档

- [完整正文](research/grok-bookmark-management-discussion.md)：两轮实质问答及最后一轮文档交付说明，原样保留分享页可见文本。
- [结构化副本](research/grok-bookmark-management-discussion.json)：六条消息的角色、正文、表格、代码和引用地址。
- [原始 DOCX 手册](research/grok-bookmark-management-handbook.docx)：从分享页直接下载，含落地顺序、词表、提示词、校验伪代码和卡片示例。

讨论提及的 `taxonomy.yaml` 和 `ingest_prompt.txt` 没有单独的公开下载入口；示例内容在已归档手册的附录 A/B 中。项目按现有 Go/Worker 接口实现实际配置，没有将重写的文件冒充原始附件。

## 实现范围

沿用 Cloudflare D1 的 `links` 表、Worker 内部鉴权、Go 顺序富化流程和现有阅读界面。Android 通过独立的 App 接口读取同一份增强内容并保存整理结果。没有新增向量库、RAG、额外数据库或自动巡库任务。

| 讨论建议 | 本项目中的实现 |
| --- | --- |
| 封闭词表 | Worker 的 `src/taxonomy.json`，17 个主题、7 种形态、5 种用途，英文 ID / 中文显示名 |
| 每次附上词表 | 普通请求、降级请求和人工原文请求均附完整词表与 JSON Schema 枚举 |
| 程序校验 | 全角 ASCII/空白/大小写归一化、显式别名、去重、最多 3 个主题；未知值丢弃并记录，标记待确认 |
| 实体分离 | `entities` 保存人名/项目名，可以搜索，不会自动成为新主题 |
| 收藏原因 | 人工 `why` 与模型 `why_suggestion` 分开；采用并保存建议后才成为用户的收藏原因 |
| 整理状态 | `inbox` / `kept` / `compiled` / `drop`，与模型处理的 `status` 分开 |
| 分面筛选 | 整理状态、主题、形态、用途、来源、收藏起始日期和待确认分类，可与关键词组合 |
| 搜索改进 | 原文、译文、摘要、标题、备注、人工原因、AI 用途建议及实体；最多 10 个空白分隔关键词，各关键词跨字段 AND 匹配 |
| 主题笔记素材 | 当前已加载的筛选结果或单条收藏导出 Markdown，保留原文、译文、原因及来源，供人工编入主题笔记 |

保留 SQL `LIKE` 的中文子串匹配，并转义 `%`、`_` 和反斜杠。本次没有实现 FTS5 索引；先改善可检索内容和筛选，后续按实际数据量与延迟决定是否增加索引方案。

## 自动与人工

`classification` 保存已校验的模型建议，`curation` 保存人工确认的主题/形态/用途。查询和页面优先使用人工分类；再次运行模型只更新建议，不能覆盖人工分类、`why` 或 `curation_status`。恢复自动分类只清空人工分类覆盖。

新收藏始终进入 `inbox`。缺少标签、存在丢弃项或模型声明不确定，都进入待确认视图。没有用模型置信度自动保留、编入笔记或删除收藏，也不要求固定比例的删除。`drop` 是可恢复的搁置状态，不删除正文，自动队列会跳过；明确发起的手工处理仍然可用。

App 修改 URL/备注后，生成的正文和分类建议按原有策略失效，人工整理结果保留。来源内容改变时，需要重新检查自己的标签和收藏原因。

所有来源均可手工整理和筛选。自动原帖读取仍使用已有的 X Search；本次没有增加微信公众号抓取器。公众号和其他网页可使用原有链接、备注、人工分类及原因。

## 词表维护

唯一可执行词表位于配套仓库 `../cairn-share/worker/src/taxonomy.json`，经 `GET /api/enrichment/taxonomy` 提供给模型和浏览器。每项包含 `id`、`label`、`aliases`、`active`。

1. 新分类只能选择 `active=true` 的 ID；停用标签保留在历史记录和筛选中，不重写旧数据。
2. 别名必须显式配置。启动时拒绝冲突别名、重复 ID 和无可用项的词表。
3. 未知词记录在每条生成结果的 `discarded_tags`，最多 10 个，每个最多 80 字；日志只记录丢弃数量。
4. 修改词表时同步修改 `version`，部署 Worker 后重启 Enricher。词表在进程启动时加载；Worker 拒绝过期版本的模型分类。
5. 停用标签用 `active=false`；合并需明确的数据迁移，不直接删除已使用的 ID。仅修改收藏原因、未改人工分类时，历史标签继续保留。

## 数据与接口

迁移 `0007_add_bookmark_curation.sql` 在 `links` 表增加 `classification`、`curation`、`why`、`curation_status` 和整理状态分页索引。历史记录默认 `inbox`、未分类，不触发全库模型回填。

Worker 增加 `/api/enrichment/taxonomy`、`PATCH /api/enrichment/jobs/{id}/curation`，完成接口的可选 `classification`，以及列表参数 `curation_status`、`topic`、`form`、`use`、`source`、`uncertain=true`、`since`（RFC3339）。

Go 同源页面增加 `/api/taxonomy` 和 `/api/bookmarks/{id}/curation`。浏览器仍然不持有内部 token。

```json
{
  "why": "想把这些检查项加入项目评审清单。",
  "curation_status": "kept",
  "classification": { "topics": ["llm", "eval"], "form": "method", "use": "try" }
}
```

字段可单独提交；`classification: null` 恢复模型建议。整理操作不领取 lease、不调用模型、不清空原文。人工分类接受稳定 ID，不静默改写用户选择。

## 部署与使用

先在配套 Worker 仓库测试、检查类型、迁移并部署 Worker，再启动新版 Enricher。新增列表字段不兼容旧版 Go 客户端的严格 JSON 解码，两端需要配套升级；App 公开 `/api/links` 的字段和鉴权保持原有约定。

```bash
# cairn-share/worker
npm test
npm run typecheck
npx wrangler d1 migrations apply cairn-share --remote
npx wrangler deploy

# cairn-x-enricher
go test -race ./...
go run ./cmd/cairn-x-enricher serve
```

正式环境的配套切换与回滚顺序见 [部署说明](deployment.md)。

先让新收藏使用固定词表，按真实检索需求修订；历史数据继续可读、可手工整理，按需重新处理单条 X 收藏。每周从收件箱挑选内容，补收藏原因、确认标签，再筛选 `kept` 导出 Markdown。导出记录当前加载数量及是否仍有未加载结果，不作为全库备份，也不会自动改成 `compiled`。

后续可根据使用情况补充按时间聚合的非法标签统计、受控历史回填和独立主题笔记编辑。本次没有自动创建大量空笔记或展开新的知识管理子系统。

## 本次验证

2026-09-08 的本地验证结果：

- Go 全部测试及 race 检测通过；`go vet`、`go mod verify` 和 CI 同版本 golangci-lint 检查通过，Lint 为 0 issues。
- 配套 Worker 的 41 个测试及 TypeScript 检查通过，覆盖 D1 迁移、别名/非法分类、人工结果保留、多来源整理、组合筛选、分页与中文搜索。
- 使用独立的本地 D1/R2 和模拟模型，47 条测试收藏中 45 条 X 收藏成功走完真实 Go/Worker 处理流程。人工整理和导出没有增加模型请求。
- Playwright 验证 1440、768、375、320 像素视口的布局和资源；验证刷新恢复、筛选往返、三主题上限、原文/原因搜索、日期恢复、Markdown 下载、明确标注部分导出、失败重试和旧搜索请求竞争。
- 归档的六条消息与抓取结果逐项比对一致，原始 DOCX 通过压缩包完整性检查。

上述本地验证使用模拟模型，不包含生产迁移、部署或真实模型调用。
