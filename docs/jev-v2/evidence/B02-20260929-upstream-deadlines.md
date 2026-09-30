# B02-T06/P2：按调用类型隔离 HTTP 客户端和时限

日期：2026-09-29。原运行入口把一个默认 3 分钟的 `http.Client` 同时交给 Worker、Grok 获取、Grok 阅读和 TypeSafe。Worker 挂起时，即使正常操作通常只需很短时间，该调用仍可占住调度器 3 分钟。

## 变更

- `serve`、`once`、`enrich` 的 Worker、Grok 获取、Grok 阅读、TypeSafe 调用分别使用独立客户端和连接池；`classify` 分开 Worker 与 TypeSafe。启动时的来源预查使用 Worker 客户端，合成阅读 canary 使用阅读客户端。所有认证调用继续不跟随重定向。
- 新增 `WORKER_REQUEST_TIMEOUT`（默认 20 秒）、`GROK_FETCH_TIMEOUT`（继承 `REQUEST_TIMEOUT`，默认 3 分钟）、`GROK_READING_TIMEOUT`（默认不超过 3 分钟）和 `TYPESAFE_REQUEST_TIMEOUT`（默认不超过 3 分钟）。`REQUEST_TIMEOUT` 保留为旧配置的回退值。获取/阅读各自不得超过 14 分钟，给 15 分钟来源租约留出至少 1 分钟；TypeSafe 不得超过当前 3 分钟单任务时限。
- 来源和阅读的付费阶段准入按两个实际客户端时限中较长者加提交余量检查；缩短 Worker 时限不改变来源租约准入或供应商持久账本。来源获取默认值暂不随意缩短：需要第 0 组生产延迟分布校准 p99，并把付费结果未知、积压和租约竞争一起验收。

## 验证

- 配置测试覆盖默认值、独立覆盖与 15 分钟来源租约／3 分钟分类任务上限；HTTP 路由测试分别计数获取、阅读、canary 请求。挂起的本地 Worker HTTP 夹具证明 30 毫秒客户端时限能取消请求；客户端与连接池互不复用，认证重定向被拒。
- `make verify`（含 race、lint、前端 488 项）和 `make test-ablation` 通过。真实本地编译 `serve`→Worker/D1 的目标切换、人工原文、合成 canary 与一次分类用例通过；`classify` CLI 与完整来源→阅读→分类→删除联调另行复验。TypeSafe/Grok 仅使用本地供应商夹具。

## 仍待验收

尚无生产 Grok/TypeSafe 耗时分布及同负载 p95/p99；20 秒 Worker 默认值在真实网络、图片上传、超限/响应丢失下需要观察。`TYPESAFE_REQUEST_TIMEOUT` 与分类任务时限当前同为 3 分钟，靠近截止点返回的成功响应还可能没有足够提交余量；B02-T08 的每任务 deadline/关停验收仍需解决。B02-T07 的跨进程持久退避、Retry-After/jitter/预算亦未由本次拆分完成。未做部署、真实付费请求或远端迁移。
