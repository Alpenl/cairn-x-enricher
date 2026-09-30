# B02-T04 阅读解码与来源保留验收（2026-09-29）

## 范围与修正

- 阅读输出仍只有标题、语言、译文、摘要。Go 对完整供应商响应设 4 MiB、结构化输出设 1 MiB 的**实际字节**上限；超限时拒绝整条响应，不再把截断后的合法 JSON 前缀当成功。拒绝缺字段、未知字段、重复键（含嵌套）、尾随第二个 JSON、非 JSON、非法 UTF-8 和超过 64 层的嵌套。原文在付费请求前校验为非空、合法 UTF-8、最多 100,000 字节。
- 阅读完成只从已存来源带回原文、链接、图片。Go、Worker 完成接口以及 Operator 的已结算阅读恢复均保留原文原始字节；来源已有语言时优先使用它，只有来源语言为空才使用阅读结果。Worker 的完成回执与来源身份守卫在同一 D1 batch；来源不匹配时没有回执或完成写入。
- 人工粘贴的两个浏览器入口、Go 管理端、Worker 受控写入及旧手动读取分支都只用去空白结果判空，保存、幂等哈希及后续读取使用原始字节。Worker 来源与完成入口统一按 UTF-8 **字节**校验 100 KB，避免多字节文本在 Worker 入库后才被 Go 拒绝。
- 旧 `Generate`/`Workflow` 仍在实验与单测代码中；生产 `serve`/`once` 创建 `NewStaged` 并使用 `FetchSource`/`Transform`，没有调用旧生成路径。

## 可重复验证

1. Go `make verify`：vet、lint、race 测试、488 项前端检查和构建通过。`internal/enrich` 定向回归覆盖合法 JSON 后的大量填充、第二个响应、重复键、非法/超深结构、100 KB 原文请求的精确保留及 100 KB+1 在付费 POST 前拒绝；`internal/processor` 覆盖来源语言、链接和图片随阅读完成带回。
2. Worker `npm test`：39 个文件、390 项通过；`npm run typecheck` 与 `npm run deploy:dry-run` 通过。定向回归覆盖人工来源首尾空白、100 KB 中文字节边界、伪造原文/语言/链接被拒且无回执、完成后精确回读，以及已结算阅读恢复时来源语言优先。
3. `CAIRN_INTEGRATION_CASE=providerrecovery CAIRN_SHARE_ROOT=<本地 Share 工作树> bash tests/local-integration/run.sh`：真实 Go Responses 客户端→本地 Worker/D1，合成 100,000 字节多字节原文（含首尾空白）、已存 `en` 与模型 `fr`，经过付费账本夹具、进程退出、Operator 回读恢复后原文、来源语言、链接不变；模型 POST 恰好一次。`CAIRN_INTEGRATION_CASE=manualrestart`：浏览器管理 API 接受粘贴后进程立即退出，新进程从 Worker/D1 回读原文、来源快照及阅读接口，首尾空白保持且同操作回放不重排。
4. 图片由处理器从已存 `image_urls` 调用 Worker R2 存储，再将返回的对象引用写入完成结果；Go 处理器回归覆盖来源图片传递，Worker 既有图片存储与恢复用例覆盖 R2 引用。上述本地测试只使用合成供应商响应，没有付费调用、远端迁移或部署。

## 边界

这份证据签收 #11 的 B02-T04 行为，不能替代 #16 的全量并发、旧新客户端、真实浏览器/Android、生产迁移与观察期验收。新增 `0045` 迁移只在本地 D1 应用；发布时需先完成 Worker 迁移及部署，再发布 Go。
