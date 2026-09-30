# B07 / 2026-09-22 完整账号绑定、旧队列恢复与自动基线

主责 [S #31](https://github.com/Alpenl/cairn-share/issues/31)，总控 [E #10](https://github.com/Alpenl/cairn-x-enricher/issues/10)，统一验收 [E #16](https://github.com/Alpenl/cairn-x-enricher/issues/16)。本批修复前一份 R3-07 报告明确保留的账号尾缀碰撞、旧队列升级和 reset 草稿问题，并补充 [B07 十条原任务审计](B07-20260922-scope-audit.md)。整体验收仍有具体未完成项，不以本批通过关闭 S #31。

## 固定实现

最终代码 Share `3a891f1eceda116b5c76007b8887fde8d36e0dd5` / Enricher `86b82b5e0ccf0096cd67445ce80e02c76f89b505`。主体修复先提交为 S `093b5b65ce4127b1579ed7ce538091854070aeae` / E `ac71472717b498a1481ba859ff7b5da7ed8b651d`，S `bd04f26` / E `86b82b5` 补齐可选响应协商，避免破坏旧 Go 严格解析器。Share 后续 `b7e6c5c` 修正设备脚本和离屏定位，`3a891f1` 修正设置页乐观 token 更新漏掉清理的入口。两个实现 PR 仍为 Draft [S #32](https://github.com/Alpenl/cairn-share/pull/32) / [E #17](https://github.com/Alpenl/cairn-x-enricher/pull/17)。报告是随后单独文档提交，最终文档 HEAD 与 CI 见 #16。

1. **账号身份**：`accountKeyFor` 改为完整规范化服务地址 + NUL 分隔符 + 完整 trimmed token 的 SHA-256 指纹，带 v2 前缀。新动作不保存原始 token，服务或完整 token 不同就不能命中同一队列；保留原先尾缀格式仅用于寻找待人工确认归属的旧动作，绝不作为发送身份。
2. **旧队列恢复**：原 JSON、operation key、CAS、依赖、未确认结果均保留。详情提供“查看并恢复”及原始字段/动作预览，确认属于当前账号和收藏后才转换账号身份。取消不改变存储；账号改变关闭旧确认，回调还校验被确认的账号。恢复先以当前凭据读取真实收藏；鉴权失败、服务不可用或已有独立新队列时保留旧记录，不合并猜测顺序。缺失旧 expectedRevision 标成可处理的冲突，仍为空，不直接猜远端版本发送；用户再明确重应用才绑定最新版本。旧成功响应丢失继续用同 operation key 确认，旧独立动作随后出现的真实 CAS 冲突仍可处理。
3. **自动基线**：Worker 从同一次 `computeEffective` 返回用于计算有效值的独立 automatic，人工 override 不进入自动基线。新 Android 与 Go 读取显式请求 `include_automatic=1`；未请求的旧客户端获得原响应字段集合，旧 Worker 忽略该查询仍由新客户端视为 baseline unknown。Go 严格 DTO 保存可空基线，看板透传，不用 effective 值填充缺失自动结果。
4. **草稿与显示**：所有本地操作、确认后的基线更新、冲突重应用和进程恢复都使用 selection.automatic。旧服务没有完整合法基线时，reset 标记为“等待服务端确认”，不把人工标签显示成自动结果；进一步局部添加/删除不会解除这种不确定性，明确清空或完整替换才可解除。Markdown 对应字段同样标为待确认。v2 词表在账号切换时失效，跨异步读取边界重复校验账号；旧账号响应不写进新账号的 v2 草稿。

**部署兼容**：新增 automatic 响应是显式 opt-in，旧版严格 Worker 消费者无需接受未知字段；新客户端读旧服务也不制造基线。新旧 Android 队列归属格式不同，旧版回退不能承担新版队列的同步；队列原动作保留，重新升级后按显式恢复/冲突路径处理。没有迁移生产数据库，没有部署或合并。

## 真正执行的场景

本地隔离 API26 x86_64 AVD `cairn-r307-api26`，实际 Android JSON/HTTP/Repository/CurationActionStore/DataStore/ViewModel 和 App 授权 Worker/D1/R2。旧数据直接写入真实 preferences protobuf 文件，再关闭旧 DataStore 后由生产 DataStore 打开；不是用内存列表冒充磁盘升级。七个阶段之间均执行 `am force-stop`，不会沿用 ViewModel 或 DataStore 进程缓存。

- 复跑前批三个阶段：五条 accept/reject/单值/set_empty/reset，提交丢响应、进程重启、两次真实其他客户端 CAS、中途断网、单收藏丢弃及不同账号保留。
- 同尾缀账号：测试 token `test-a-12345678` / `test-b-12345678` 的旧身份确实相同，新身份不同。切换后原账号动作没有新增任何发送，错误凭据不能恢复旧记录。账号切换失败不会清除旧动作。
- 旧格式：原 first 已在真实 Worker 成功但本地仍保留；新进程启动不自动绑定旧尾缀。显式恢复后原 first 只返回原 revision，second 的旧 CAS 产生真实冲突；用户重应用后完成。另一条没有旧 CAS 的记录保留 null 并进入冲突，明确重应用后完成。其他服务器旧记录始终未发送；新旧独立队列不能静默合并。
- 自动预览：真实 D1 的既有 AI-only 基线是 llm，通过真实人工动作得到有效主题 eng。离线 reset 立即预览 llm，Worker 仍为 eng；强制结束进程后从磁盘原动作 + 真实基线恢复同一 llm 草稿，网络恢复后服务端最终 llm，automatic 未变化。AI fixture 是固定旧分类数据，不是人工参考标签或新模型调用。
- 两项新增 Compose 设备测试：可检查的旧动作确认弹窗、取消保留、同尾缀账号切换关闭旧弹窗、再次正确确认后发送；旧服务无 automatic 且请求失败时显示 reset pending，隐藏误导性的旧人工选中值。组件 HTTP 服务为 MockWebServer，这部分明确区别于上述真实 Worker 场景。

Go 真实 `TestLocalWorkerDecisionReferences` 还检验：effective 因人工 reject 为空，automatic 仍为 llm，实际 dashboard handler 将两个不同值一起正确透传。Worker 增加 legacy AI baseline→人工清空/选择→reset 的真实 D1 回归，并同时证明未 opt-in 的两种旧读取路径没有 automatic 字段。

## 门禁与证据

| 检查 | 结果 |
|---|---|
| 最终 Worker npm test / typecheck / deploy:dry-run | 180/180，exit 0；不部署 |
| 最终 Go make verify / make test-ablation | exit 0，含 race/lint、73 前端检查与构建 |
| 最终 Go/Worker/D1/R2 场景 | 8/8，包含生产严格读取与实际看板的自动/有效值分离 |
| 本地 Android strict unit/lint/build/compile androidTest | 64/64 unit，全部 exit 0；为新增 query opt-in 前的主体业务版本 |
| 最终 Android strict unit/lint/build/compile androidTest | 64/64 unit，全部 exit 0；含设置页入口修复，[日志](logs/20260922-account-baseline/android-settings-verify.log) |
| 本地普通 connected instrumentation | XML 20 项，13 实际通过、7 明确 skip（未传真实 Worker 参数） |
| 本地真实 Worker 设备 harness | 7 个独立进程阶段各 OK (1 test)，exit 0，补足上述 7 项；本地日志按“协商前”命名，不能代替最终 Head 设备证据 |
| 最终 Head 的 API26 / API35 远端设备矩阵 | [35728181687](https://github.com/Alpenl/cairn-share/actions/runs/35728181687) 固定 S `3a891f1`，两项成功；每个 API 的 XML 20 项 = 13 passed + 7 skipped，7 个真实 Worker 独立进程阶段全部 OK (1 test) |

最终设备工作流 [.github/workflows/device.yml](https://github.com/Alpenl/cairn-share/blob/bd04f26ffe849b03bd853a82efbc7119d1a94b3b/.github/workflows/device.yml) 除普通 connected 外，还安装本地 Worker 依赖、启动隔离 D1/R2、执行七阶段真实业务恢复，并上传 XML、日志与不含凭据的动作历史。模型 API 全程不参与，**本批 0 次付费调用**；此前 B08 的 43 次付费总数不变。

日志：[Worker](logs/20260922-account-baseline/worker-verify.log)、[Go 和 8 场景](logs/20260922-account-baseline/enricher-verify.log)、[本地 Android 协商前](logs/20260922-account-baseline/android-local-before-negotiation.log)、[本地七阶段协商前](logs/20260922-account-baseline/device-local-before-negotiation.log)、[本地动作历史](logs/20260922-account-baseline/local-transport-history.json)。最终远端归档：[API26 XML](logs/20260922-account-baseline/api26/connected.xml) / [API35 XML](logs/20260922-account-baseline/api35/connected.xml)；[API26 七阶段和动作历史](logs/20260922-account-baseline/api26/) / [API35 七阶段和动作历史](logs/20260922-account-baseline/api35/)。两端各记录 25 次真实动作传输，含有限重试；传输次数不等于生效次数，幂等/CAS/最终 revision 由各阶段实际断言。最终代码的普通 [Share CI 35728126503](https://github.com/Alpenl/cairn-share/actions/runs/35728126503) 与 [Enricher CI 35726601521](https://github.com/Alpenl/cairn-x-enricher/actions/runs/35726601521) 均成功。

## 真实失败与修复

设备测试首先因 Kotlin 推断出 JSON 返回类型，被 JUnit 拒绝（测试必须 Unit），已显式修正后重跑。随后旧队列恢复被拒时，“请先同步现有动作”被无条件自动重试覆盖，实际 UI 等待失败；修复为恢复成功才触发发送，保留 [失败日志](logs/20260922-account-baseline/legacy-recovery-before-fix.log)，全七阶段再通过。Go 新测试请求最初未显式传 context，被原 noctx lint 拒绝；改用 NewRequestWithContext，未关闭规则。

第一轮远端设备运行 [35726272328](https://github.com/Alpenl/cairn-share/actions/runs/35726272328) 的 API35 在 SDK 安装阶段出现 `Error on ZipFile unknown archive`，测试尚未启动。其 API26 后因最终协商代码的新矩阵运行而被并发策略取消；整轮不计通过。最终结果只引用后续固定 Head 的实际完成记录，不把环境失败、取消或编译记成设备成功。

第二轮远端 [35726622351](https://github.com/Alpenl/cairn-share/actions/runs/35726622351) 两个 job 真实失败：API26 普通设备测试通过后，harness 因 runner 未安装 rg 中止，改用标准 grep 保留同样的成功结果断言；API35 旧动作测试在账号切回后等待离屏的 LazyColumn 行超时，改为滚动定位并断言可见，不延长超时或跳过测试。[原 API26 XML](logs/20260922-account-baseline/initial-api26.xml) / [原 API35 XML](logs/20260922-account-baseline/initial-api35.xml) 保留。

额外审计发现设置页先乐观更新 uiState token，原监听器再比较 uiState 时会漏掉账号变化。已在实际 setApiToken 入口同步清空 v2 内存状态，并让持久化监听比较连续的已观察 token；同尾缀真实 Worker 设备阶段改为直接经过该设置入口，检查清空后才能继续恢复。完整 v1 读取缓存的隔离和迟到响应仍按 B07-T02 继续审计。此前仅设备断言修复的 [35727865177](https://github.com/Alpenl/cairn-share/actions/runs/35727865177) 因新增设置入口代码而被新矩阵取消，不计通过。

## 原范围仍需完成

十条原任务的逐项结论及下一步见 [B07 审计](B07-20260922-scope-audit.md)：状态/候选/人工来源、完整读取缓存与能力身份、实体/来源/partial 导出、全词表小屏和可访问性、完整旧/新客户端读写矩阵等还需实现或更强证据。API26/35 模拟器也不是物理真机或完整 Web 浏览器并发证明。B08 自动参考拟合/受控消融/固定候选检索/holdout、B09 全局预算与质量收益、全部 R/B/SC 以及独立复审仍在原目标内。原复选框与历史记录保留，最终统一 #16。
