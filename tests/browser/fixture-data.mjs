// Deterministic, realistic bookmark fixtures for UI preview and browser tests.
//
// Everything here is synthetic. It exists so the dashboard can be developed and
// screenshot-tested against believable content without a Worker, D1, R2 or any
// paid model call.

export const TAXONOMY_VERSION = "2026-09-20.1";

const term = (id, label, active = true) => ({ id, label, active, aliases: [], description: "" });

export function taxonomyV1() {
  return {
    version: TAXONOMY_VERSION,
    topics: [
      term("llm", "LLM"), term("eng", "工程"), term("product", "产品"), term("design", "设计"),
      term("writing", "写作"), term("invest", "投资"), term("health", "健康"), term("psych", "心理"),
      term("manage", "管理"), term("media", "媒体"), term("law", "法律"), term("edu", "教育"),
      term("history", "历史"), term("science", "科学"), term("city", "城市"), term("life", "生活"),
      term("eval", "评估"), term("crypto", "加密货币", false)
    ],
    forms: [
      term("opinion", "观点"), term("method", "方法"), term("case", "案例"), term("data", "数据"),
      term("tool", "工具"), term("thread", "串推"), term("longform", "长文")
    ],
    uses: [term("quote", "可引用"), term("try", "待试"), term("contra", "反对"), term("background", "背景"), term("material", "素材")]
  };
}

export function taxonomyV2() {
  return {
    ...taxonomyV1(),
    definition_version: 2,
    content_functions: [term("method", "方法"), term("tool", "工具"), term("case", "案例"), term("data", "数据"), term("opinion", "观点")],
    carriers: [term("single", "单帖"), term("author_continuation", "作者续帖"), term("external_article", "外链长文"), term("unknown", "未知")],
    affordances: [term("quote", "可引用"), term("practice", "可实践"), term("background", "可作背景"), term("material", "可作素材")]
  };
}

// Each seed is one plausible saved post. Fields map directly onto the Worker's
// list/detail payload and the v2 selection.
const SEEDS = [
  {
    title: "用评估集驱动提示词迭代：一个可复用的六步流程",
    summary: "作者把提示词调优拆成六步：先写失败样例，再建最小评估集，每次只改一个变量并记录分数。核心观点是没有评估集的调优只是在凭感觉。",
    body: ["很多团队调提示词靠“看起来更好”。作者建议先收集 30 条真实失败样例，把它们变成带期望输出的小型评估集。", "之后每次只改动一个变量：指令措辞、示例顺序或输出格式，并记录通过率。分数下降就回滚，不要叠加多个改动。", "最后把评估集纳入 CI，任何提示词修改都必须跑一遍。作者说这让他们的回归问题减少了一半以上。"],
    original: "Most teams tune prompts by vibes. Start with 30 real failures, turn them into a tiny eval set, change one variable at a time, and put the evals in CI.",
    topics: ["llm", "eval", "eng"], functions: ["method", "case"], carrier: "author_continuation", affordances: ["practice", "quote"],
    form: "method", use: "try", entities: ["OpenAI Evals", "Braintrust"], why: "用在团队评审清单里", suggestion: "可作为团队提示词评审的检查清单。", images: 1
  },
  {
    title: "为什么小团队不该过早拆微服务",
    summary: "一位工程负责人复盘了把单体拆成 14 个服务后的代价：部署复杂度上升、跨服务调试困难、团队规模却没有增长。他建议先在单体内做清晰的模块边界。",
    body: ["拆分的初衷是提高独立部署能力，但十人团队根本用不上。", "他们最终合并回 4 个服务，并在单体内用包级别的依赖规则约束边界。", "作者的结论：组织结构决定架构，团队没有分开之前，服务也不该分开。"],
    original: "We split our monolith into 14 services with a team of 10. Here's what it cost us and why we merged back to 4.",
    topics: ["eng", "manage"], functions: ["case", "opinion"], carrier: "author_continuation", affordances: ["background", "quote"],
    form: "case", use: "background", entities: ["Conway's Law"], suggestion: "架构决策时可以作为反例引用。", images: 0
  },
  {
    title: "Figma 新的变量模式让设计令牌真正落地",
    summary: "介绍 Figma 变量模式如何把颜色、间距和字号统一成设计令牌，并与代码中的 CSS 变量一一对应，减少设计交付中的手工同步。",
    body: ["变量模式允许同一组令牌在浅色、深色和高对比主题之间切换。", "作者演示了把令牌导出为 JSON，再用脚本生成 CSS 自定义属性。", "他提醒：令牌命名要按用途而不是按颜色，比如 surface-subtle 而不是 gray-100。"],
    original: "Figma variables + modes finally make design tokens practical. Name tokens by purpose, not by value.",
    topics: ["design", "eng"], functions: ["tool", "method"], carrier: "single", affordances: ["practice"],
    form: "tool", use: "try", entities: ["Figma"], suggestion: "设计系统改版时可以直接照做。", images: 2
  },
  {
    title: "长期指数投资者最常犯的三个错误",
    summary: "作者总结了三个常见错误：在下跌时停止定投、频繁切换指数、以及忽视费率。用 20 年回测数据说明坚持比择时更重要。",
    body: ["回测显示，2008 年停止定投的投资者，十年后的收益比坚持者低约 40%。", "频繁切换“更好的指数”往往追在涨幅之后。", "费率差 0.5% 在 30 年后会吞掉接近 15% 的最终资产。"],
    original: "Three mistakes long-term index investors make: stopping contributions in drawdowns, index hopping, ignoring fees.",
    topics: ["invest"], functions: ["data", "opinion"], carrier: "single", affordances: ["background", "quote"],
    form: "data", use: "quote", entities: ["S&P 500"], why: "提醒自己别在下跌时停投", suggestion: "适合作为个人投资纪律的提醒。", images: 1
  },
  {
    title: "Claude 的工具调用在长任务里如何保持稳定",
    summary: "开发者分享了在 200 步以上的智能体任务中保持工具调用稳定的做法：显式的状态文件、每 20 步自检、以及把失败工具调用的错误原文回传给模型。",
    body: ["长任务最大的问题是上下文漂移。作者让智能体维护一个 state.md 文件，每一步都读写它。", "每 20 步强制一次自检：列出已完成、进行中和阻塞的事项。", "工具失败时不要吞掉错误，把原始报错交给模型，它通常能自己修正参数。"],
    original: "How we keep tool use stable across 200+ step agent runs: explicit state files, periodic self-checks, raw tool errors.",
    topics: ["llm", "eng"], functions: ["method", "case"], carrier: "author_continuation", affordances: ["practice"],
    form: "method", use: "try", entities: ["Claude", "Anthropic"], suggestion: "可以在自己的智能体项目里试用状态文件模式。", images: 1
  },
  {
    title: "睡眠债无法在周末一次性还清",
    summary: "一项追踪研究显示，工作日睡眠不足、周末补觉的人，胰岛素敏感度并没有恢复。规律作息比总时长更关键。",
    body: ["研究者把受试者分成三组：充足睡眠、持续不足、以及周末补觉。", "周末补觉组在周一的代谢指标与持续不足组几乎相同。", "作者建议固定起床时间，即使前一晚睡得晚。"],
    original: "Weekend catch-up sleep does not reverse the metabolic effects of weekday sleep debt.",
    topics: ["health", "science"], functions: ["data"], carrier: "external_article", affordances: ["background"],
    form: "data", use: "background", entities: [], suggestion: "可作为健康习惯的背景资料。", images: 0
  },
  {
    title: "写作时先写结论，再补论证",
    summary: "作者建议技术写作采用“倒金字塔”结构：第一段直接给结论和行动建议，后面再展开原因和细节，读者可以随时停下。",
    body: ["大多数读者只看第一段。", "把结论放在前面，也逼迫作者先想清楚自己到底要说什么。", "细节和论证放在后面，供需要的人深入阅读。"],
    original: "Write the conclusion first. Most readers stop after the first paragraph.",
    topics: ["writing"], functions: ["method", "opinion"], carrier: "single", affordances: ["practice", "quote"],
    form: "method", use: "quote", entities: [], why: "周报就这么写", suggestion: "可以用于改进周报和设计文档。", images: 0
  },
  {
    title: "一个 CSS 技巧：用 container queries 替代大部分媒体查询",
    summary: "演示如何用容器查询让组件根据自身宽度而不是视口宽度调整布局，从而在侧栏、卡片和弹窗中复用同一组件。",
    body: ["媒体查询只知道视口宽度，组件放进侧栏时就会失效。", "容器查询让组件对父容器宽度做出反应。", "作者给出了一个卡片组件在 3 种容器中的示例。"],
    original: "Container queries let components respond to their own width instead of the viewport.",
    topics: ["eng", "design"], functions: ["tool", "method"], carrier: "single", affordances: ["practice"],
    form: "tool", use: "try", entities: ["CSS"], suggestion: "前端组件库可以采用。", images: 3
  },
  {
    title: "产品经理如何判断一个需求值不值得做",
    summary: "作者提出三个问题：谁在什么场景下会用、不做会怎样、做了如何衡量。回答不出第三个问题的需求应该先搁置。",
    body: ["很多需求来自“某个客户提了一句”。", "作者要求每个需求都写出可以验证的成功指标。", "无法衡量的需求，先放进观察清单，而不是直接排期。"],
    original: "Three questions before building a feature: who and when, what if we don't, how will we measure it.",
    topics: ["product", "manage"], functions: ["method", "opinion"], carrier: "author_continuation", affordances: ["quote", "practice"],
    form: "method", use: "quote", entities: [], suggestion: "需求评审时可以引用这三个问题。", images: 0
  },
  {
    title: "开源向量数据库横评：延迟、召回率与内存占用",
    summary: "作者在同一台机器上比较了五个开源向量数据库在百万级数据上的查询延迟、召回率和内存占用，并公开了测试脚本。",
    body: ["测试使用 100 万条 768 维向量。", "在 95% 召回率下，最快和最慢的方案延迟相差 6 倍。", "内存占用差异更大，作者建议按数据规模而不是热度选型。"],
    original: "Benchmarking five open-source vector databases at 1M vectors: latency, recall, memory.",
    topics: ["eng", "llm", "eval"], functions: ["data", "tool"], carrier: "external_article", affordances: ["background", "material"],
    form: "data", use: "background", entities: ["Qdrant", "Milvus", "pgvector", "Weaviate"], suggestion: "选型时可以参考这份数据。", images: 2
  },
  {
    title: "城市更新不是拆旧建新",
    summary: "一位规划师讨论老城区更新的另一种方式：保留街道尺度、引入小规模商业和公共空间，而不是整片拆除重建。",
    body: ["整片拆除会破坏原有的社会网络。", "小规模、渐进的改造更能保留街区活力。", "作者举了三个国内外案例。"],
    original: "Urban renewal does not have to mean demolition. Keep the street scale.",
    topics: ["city", "history"], functions: ["case", "opinion"], carrier: "external_article", affordances: ["background", "material"],
    form: "opinion", use: "material", entities: [], suggestion: "可作为城市主题写作的素材。", images: 2
  },
  {
    title: "心流不是天赋，而是可以设计的环境",
    summary: "作者认为进入心流取决于任务难度与技能的匹配、明确的目标和及时反馈，并给出了在工作中设计这些条件的具体做法。",
    body: ["任务太难会焦虑，太简单会无聊。", "把大任务拆成有明确完成标准的小块。", "尽量缩短反馈周期，比如先写测试。"],
    original: "Flow is designed, not gifted: challenge-skill balance, clear goals, fast feedback.",
    topics: ["psych", "life"], functions: ["method", "opinion"], carrier: "single", affordances: ["practice", "quote"],
    form: "method", use: "try", entities: [], suggestion: "可以用来安排深度工作时间。", images: 0
  },
  {
    title: "如何给大模型应用做成本控制",
    summary: "总结了五种降低大模型调用成本的方法：缓存、路由到小模型、压缩上下文、批处理和限制输出长度，并给出每种方法的节省比例。",
    body: ["缓存命中率通常是最容易提升的指标。", "把简单请求路由到小模型，可以节省 60% 以上的费用。", "限制输出长度常被忽视，但对长文本生成非常有效。"],
    original: "Five ways to cut LLM costs: caching, routing, context compression, batching, output limits.",
    topics: ["llm", "eng", "product"], functions: ["method", "data"], carrier: "author_continuation", affordances: ["practice", "background"],
    form: "method", use: "try", entities: ["Prompt Caching"], why: "Cairn 自己的成本也要控制", suggestion: "可用于优化本项目的调用预算。", images: 1
  },
  {
    title: "学习一门新语言最有效的是可理解输入",
    summary: "作者结合语言习得研究，认为大量略高于当前水平的可理解输入，比背单词和语法练习更有效。",
    body: ["输入假说认为语言能力来自理解。", "选择自己能懂 80% 以上的材料。", "每天固定时间，比偶尔长时间学习更好。"],
    original: "Comprehensible input beats drills for language acquisition.",
    topics: ["edu", "psych"], functions: ["opinion", "method"], carrier: "single", affordances: ["practice"],
    form: "opinion", use: "try", entities: ["Stephen Krashen"], suggestion: "", images: 0
  },
  {
    title: "新的数据保护法规对个人开发者意味着什么",
    summary: "律师解读了新规对小型应用的影响：收集最少必要数据、提供删除入口、记录处理目的。个人开发者也需要隐私政策。",
    body: ["新规对规模没有豁免。", "最小必要原则意味着不要默认收集设备信息。", "提供可用的删除入口，并在合理时间内完成删除。"],
    original: "What the new data protection rules mean for indie developers.",
    topics: ["law", "product"], functions: ["opinion"], carrier: "external_article", affordances: ["background"],
    form: "opinion", use: "background", entities: [], suggestion: "", images: 0
  },
  {
    title: "媒体平台的推荐算法如何改变了新闻写作",
    summary: "作者分析了推荐算法对新闻标题和篇幅的影响：标题更情绪化、正文更短、同质化更严重，并讨论了编辑可以做的抵抗。",
    body: ["点击率成为唯一指标后，标题开始趋同。", "长篇调查报道的分发越来越依赖订阅用户。", "作者建议编辑部建立自己的质量指标。"],
    original: "How recommendation algorithms reshaped news writing.",
    topics: ["media", "writing"], functions: ["opinion", "case"], carrier: "author_continuation", affordances: ["quote", "material"],
    form: "opinion", use: "quote", entities: [], suggestion: "", images: 1
  }
];

const HOSTS = ["x.com", "x.com", "x.com", "x.com", "x.com", "x.com", "mp.weixin.qq.com", "sspai.com"];
const AUTHORS = ["karpathy", "swyx", "simonw", "levelsio", "dan_abramov", "rauchg", "patio11", "sama", "hamelhusain", "eugeneyan"];

// A small seeded PRNG keeps screenshots and tests reproducible.
function prng(seed) {
  let value = seed >>> 0;
  return () => {
    value = (value + 0x6d2b79f5) >>> 0;
    let t = value;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function sourceOf(url) {
  if (/^https?:\/\/(x|twitter)\.com\//.test(url)) return "x";
  if (/^https?:\/\/mp\.weixin\.qq\.com\//.test(url)) return "wechat";
  return "other";
}

export function createBookmarks({ count = 96, seed = 7, now = Date.now() } = {}) {
  const random = prng(seed);
  const items = [];
  const day = 86400000;
  for (let index = 0; index < count; index++) {
    const id = 1000 + count - index; // newest first
    const base = SEEDS[index % SEEDS.length];
    const variant = Math.floor(index / SEEDS.length);
    const host = HOSTS[Math.floor(random() * HOSTS.length)];
    const author = AUTHORS[Math.floor(random() * AUTHORS.length)];
    const url = host === "x.com" ? `https://x.com/${author}/status/${18000000000 + id * 7919}`
      : host === "mp.weixin.qq.com" ? `https://mp.weixin.qq.com/s/${id.toString(36)}Qx${variant}`
        : `https://sspai.com/post/${80000 + id}`;
    const source = sourceOf(url);
    // Age grows with index: a few today, then spread over ~4 months.
    const ageDays = index < 5 ? random() * 0.4 : index < 18 ? 1 + random() * 6 : index < 40 ? 7 + random() * 22 : 30 + (index - 40) * 1.6 + random();
    const createdAt = new Date(now - ageDays * day).toISOString();

    let status = "completed";
    if (source !== "x") status = "unsupported";
    else if (index === 1) status = "processing";
    else if (index === 3) status = "pending";
    else if (index === 9 || index === 27) status = "failed";
    else if (index === 22 || index === 51) status = "exhausted";
    const enriched = status === "completed";

    const roll = random();
    const curationStatus = index < 14 ? (roll < 0.85 ? "inbox" : "kept")
      : roll < 0.45 ? "inbox" : roll < 0.72 ? "kept" : roll < 0.84 ? "compiled" : "drop";
    const reviewed = curationStatus !== "inbox";
    const uncertain = !reviewed && random() < 0.35;
    const title = variant === 0 ? base.title : `${base.title}（${["续", "补充", "讨论", "译"][variant % 4]}）`;
    const topics = base.topics.slice();
    const automatic = {
      topics, content_functions: base.functions.slice(), carriers: [base.carrier], affordances: base.affordances.slice(),
      form: base.form, use: base.use
    };
    const hasImages = enriched && base.images > 0 && random() < 0.85;
    const images = hasImages ? Array.from({ length: base.images }, (_, n) => ({
      key: `enrichment/${id}/${(id * 31 + n).toString(16).padStart(64, "a")}.svg`, content_type: "image/svg+xml"
    })) : [];
    const why = curationStatus === "inbox" ? "" : (base.why && random() < 0.8 ? base.why : "");
    items.push({
      id, url, note: random() < 0.12 ? "朋友推荐" : "", created_at: createdAt, status,
      processable: source === "x", attempts: status === "failed" ? 2 : status === "exhausted" ? 5 : status === "completed" ? 1 : 0,
      next_retry_at: status === "failed" ? new Date(now + (20 + index) * 60000).toISOString() : "",
      ai_title: enriched ? title : "", original_language: enriched ? "en" : "",
      original_text: enriched ? base.original : "",
      translated_text: enriched ? base.body.join("\n\n") : "",
      summary: enriched ? base.summary : "",
      related_links: enriched && random() < 0.4 ? [`https://github.com/example/${author}-notes`, "https://arxiv.org/abs/2409.01234"] : [],
      images, model: enriched ? "grok-4" : "",
      error: status === "failed" ? "x_search 没有返回原帖内容" : status === "exhausted" ? "原帖已删除或不可见（已重试 5 次）" : "",
      updated_at: createdAt, enriched_at: enriched ? createdAt : "", source,
      why, curation_status: curationStatus,
      classification: enriched ? {
        topics: topics.slice(0, 3), form: base.form, use: base.use, why_suggestion: base.suggestion,
        entities: base.entities, uncertainty: uncertain, taxonomy_version: TAXONOMY_VERSION, discarded_tags: []
      } : null,
      classification_reviewed: enriched && reviewed,
      // v2 state kept alongside the v1 projection.
      v2: enriched ? {
        revision: 1 + Math.floor(random() * 4), automatic, selection: structuredClone(automatic), empty: {}
      } : null,
      entities: enriched ? {
        state: base.entities.length ? "completed_nonempty" : "completed_empty", stale: false,
        entities: base.entities.slice(), human: [], revision: 1,
        observations: base.entities.map((surface, n) => ({
          candidate: { surface, block_id: "b1", start: 10 + n * 20, end: 10 + n * 20 + surface.length },
          decision: "relevant", canonical_state: n === 0 ? "matched" : "unknown",
          canonical_label: surface, canonical_kind: "product",
          canonical_evidence: n === 0 ? [{ identifier: `https://en.wikipedia.org/wiki/${encodeURIComponent(surface)}` }] : []
        }))
      } : { state: "not_run", stale: false, entities: [], human: [], revision: 0, observations: [] },
      classificationJob: enriched ? { status: "completed", attempts: 1, error: null } : { status: "waiting_source", attempts: 0, error: null }
    });
  }
  return items;
}

// An SVG "photo" with soft shapes. Served as image/svg+xml, which the Go proxy
// also accepts because it only requires an image/* type.
export function fixtureImage(key) {
  let hash = 0;
  for (const char of key) hash = (hash * 33 + char.charCodeAt(0)) >>> 0;
  const palettes = [
    ["#1f3b73", "#3f7cc4", "#f2c14e"], ["#2e4d3a", "#6fa36b", "#f0e6c8"], ["#5b2a4a", "#c65f7d", "#f7d1ba"],
    ["#21313c", "#4f6d7a", "#e8dab2"], ["#3d2c1e", "#b5793b", "#f3e3c3"], ["#1c2541", "#5bc0be", "#fce9d8"]
  ];
  const [a, b, c] = palettes[hash % palettes.length];
  const x = 120 + (hash % 360);
  const y = 80 + ((hash >> 5) % 200);
  const r = 60 + ((hash >> 9) % 80);
  return `<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="${hash % 3 === 0 ? 900 : 675}" viewBox="0 0 800 ${hash % 3 === 0 ? 600 : 450}">
<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="${a}"/><stop offset="1" stop-color="${b}"/></linearGradient></defs>
<rect width="800" height="600" fill="url(#g)"/>
<circle cx="${x}" cy="${y}" r="${r}" fill="${c}" opacity=".85"/>
<rect x="${(hash >> 3) % 500}" y="${250 + ((hash >> 7) % 120)}" width="${220 + (hash % 160)}" height="${40 + (hash % 50)}" rx="10" fill="#ffffff" opacity=".18"/>
<path d="M0 ${380 + (hash % 60)} Q 200 ${300 + (hash % 90)} 400 ${370 + (hash % 40)} T 800 ${340 + (hash % 70)} V 600 H 0 Z" fill="#000" opacity=".22"/>
</svg>`;
}
