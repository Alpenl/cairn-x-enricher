package ablation

// Samples returns the frozen evaluation corpus.
//
// CORRECTNESS RULE: every URL here was resolved through the endpoint and the
// returned author handle was compared against the author in the URL path. A
// sample whose ID does not belong to the claimed author, or whose post is not
// retrievable, MUST NOT be scored: an earlier revision of this file used
// `xai/status/1991910395720925418`, but that ID belongs to @karpathy, so a
// correct model response looked like an identity failure.
//
// Each sample carries the post's real text as `ReferenceText` so the scorer can
// detect fabrication. `SourceText` is set only for samples used to exercise the
// trusted-source recovery path.
//
// Cases covered:
//
//   - 301 English long-form post, the primary live-search case
//   - 302 Japanese post, which must keep its original language
//   - 303 OpenAI announcement with a documentation link, exercising related_links
//   - 304 Chinese commentary post, the common non-English bookmark case
func Samples() []Sample {
	return []Sample{
		{
			ID:   301,
			URL:  "https://x.com/karpathy/status/1991910395720925418",
			Note: "关于动物智能与大模型智能差异的长文，想留作素材",
			ReferenceText: `Something I think people continue to have poor intuition for: The space of intelligences is large and animal intelligence (the only kind we've ever known) is only a single point, arising from a very specific kind of optimization that is fundamentally distinct from that of our technology.

Animal intelligence optimization pressure:
- innate and continuous stream of consciousness of an embodied "self", a drive for homeostasis and self-preservation in a dangerous, physical world.
- thoroughly optimized for natural selection => strong innate drives for power-seeking, status, dominance, reproduction. many packaged survival heuristics: fear, anger, disgust, ...
- fundamentally social => huge amount of compute dedicated to EQ, theory of mind of other agents, bonding, coalitions, alliances, friend & foe dynamics.
- exploration & exploitation tuning: curiosity, fun, play, world models.

LLM intelligence optimization pressure:
- the most supervision bits come from the statistical simulation of human text= >"shape shifter" token tumbler, statistical imitator of any region of the training data distribution. these are the primordial behaviors (token traces) on top of which everything else gets bolted on.
- increasingly finetuned by RL on problem distributions => innate urge to guess at the underlying environment/task to collect task rewards.
- increasingly selected by at-scale A/B tests for DAU => deeply craves an upvote from the average user, sycophancy.
- a lot more spiky/jagged depending on the details of the training data/task distribution. Animals experience pressure for a lot more "general" intelligence because of the highly multi-task and even actively adversarial multi-agent self-play environments they are min-max optimized within, where failing at *any* task means death. In a deep optimization pressure sense, LLM can't handle lots of different spiky tasks out of the box (e.g. count the number of 'r' in strawberry) because failing to do a task does not mean death.

The computational substrate is different (transformers vs. brain tissue and nuclei), the learning algorithms are different (SGD vs. ???), the present-day implementation is very different (continuously learning embodied self vs. an LLM with a knowledge cutoff that boots up from fixed weights, processes tokens and then dies). But most importantly (because it dictates asymptotics), the optimization pressure / objective is different. LLMs are shaped a lot less by biological evolution and a lot more by commercial evolution. It's a lot less survival of tribe in the jungle and a lot more solve the problem / get the upvote. LLMs are humanity's "first contact" with non-animal intelligence. Except it's muddled and confusing because they are still rooted within it by reflexively digesting human artifacts, which is why I attempted to give it a different name earlier (ghosts/spirits or whatever). People who build good internal models of this new intelligent entity will be better equipped to reason about it today and predict features of it in the future. People who don't will be stuck thinking about it incorrectly like an animal.`,
		},
		{
			ID:   302,
			URL:  "https://x.com/yoko_materialDX/status/1928406042108465617",
			Note: "日语技术帖，检查原文语言是否被保留",
			ReferenceText: `機械学習により偏微分方程式を解く論文。

Transformerとニューラル演算子の組み合わせ、点群を直接エンコードできるよう工夫することで、幾何学情報を効果的にとらえられ複雑な形状変化を高い精度で予測できたそうです。

この分野もTransformerが流行ってきてる？`,
		},
		{
			ID:   303,
			URL:  "https://x.com/OpenAI/status/1991634046624116784",
			Note: "OpenAI 官方公告，带帮助文档链接",
			ReferenceText: `We've expanded access to localized crisis helplines in ChatGPT.

When our systems detect potential signs that someone may be experiencing distress, our models now offer an easy way to reach real people directly via @ThroughlineCare.

Learn more here:
https://help.openai.com/en/articles/12677603-crisis-helpline-support-in-chatgpt`,
		},
		{
			ID:   304,
			URL:  "https://x.com/op7418/status/1873727668912566589",
			Note: "中文技术评论帖，常见的中文收藏场景",
			ReferenceText: `DeepSeek V3 的LLM竞技场评分出来了

目前排第七，不如GPT-4o最后版本、O1以及Gemini 2，超过了Claude 3.5 Sonnet

在复杂提示和编程方面表现出色

前十名中性价比最高的模型（每百万输入Token仅需0.14美元）`,
		},
	}
}

// SourceSamples returns the samples that also carry trusted source text. These
// exercise the recovery path where a failed bookmark is re-enriched from text
// already stored in D1.
func SourceSamples() []Sample {
	out := Samples()
	for i := range out {
		out[i].SourceText = out[i].ReferenceText
	}
	return out
}
