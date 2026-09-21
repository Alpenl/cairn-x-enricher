// Local stand-in for the two paid external model boundaries. It implements the
// exact request/response shapes the Go clients expect, so the real processor,
// HTTP server, Worker and browser can run end to end without a paid call.
import { createServer } from "node:http";

function envelope(payload, { xSearch = false, model = "grok-mock" } = {}) {
  const output = [];
  if (xSearch) output.push({ type: "x_search_call", status: "completed" });
  output.push({
    type: "message", status: "completed",
    content: [{ type: "output_text", text: JSON.stringify(payload) }]
  });
  return { status: "completed", model, output };
}

function choiceAnswers(question) {
  const options = Object.keys(question.criteria || {});
  const pick = options.find((option) => option !== "none") ?? options[0];
  const rest = options.filter((option) => option !== pick);
  const probabilities = {};
  for (const option of options) {
    probabilities[option] = option === pick ? 0.95 : (rest.length ? 0.05 / rest.length : 0.05);
  }
  return { type: "choice", choice: pick, probabilities, confidence: 0.8 };
}

function scoreAnswer(question) {
  const levels = Array.isArray(question.criteria) ? question.criteria : [];
  const legend = {};
  const probabilities = {};
  levels.forEach((level, index) => {
    legend[String(index)] = typeof level === "string" ? level : JSON.stringify(level);
    probabilities[String(index)] = index === 0 ? 1 : 0;
  });
  return { type: "score", score: 0, legend, probabilities, confidence: 1 };
}

export function startMockModel() {
  const requests = [];
  const server = createServer(async (request, response) => {
    let raw = "";
    for await (const chunk of request) raw += chunk;
    let body = {};
    try { body = raw ? JSON.parse(raw) : {}; } catch { body = {}; }
    requests.push({ path: request.url, body });
    const send = (payload, status = 200) => {
      response.writeHead(status, { "Content-Type": "application/json" });
      response.end(JSON.stringify(payload));
    };
    if (request.url === "/v1/responses") {
      const name = body?.text?.format?.name;
      if (name === "x_reading") {
        return send(envelope({
          ai_title: "本地集成测试用的中文标题", original_language: "en",
          translated_text: "本地集成的中文译文。", summary: "本地集成的摘要。"
        }));
      }
      return send(envelope({
        original_text: "A practical guide to evaluating large language models. It compares methods, tools and data.",
        original_language: "en",
        context_text: "A related comment that is not the original post.",
        related_links: [],
        image_urls: []
      }, { xSearch: true }));
    }
    if (request.url === "/v1/systemone") {
      const questions = body?.questions || {};
      const answers = {};
      for (const [id, question] of Object.entries(questions)) {
        if (question.type === "noul") answers[id] = { type: "noul", noul: 0.93 };
        else if (question.type === "choice") answers[id] = choiceAnswers(question);
        else if (question.type === "score") answers[id] = scoreAnswer(question);
      }
      return send({ model: "jev-mock-1.0", answers, usage: { input_tokens: 111, output_tokens: 22 } });
    }
    return send({ error: "not_found" }, 404);
  });
  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => resolve({
      server, requests,
      url: `http://127.0.0.1:${server.address().port}`,
      close: () => new Promise((done) => server.close(done))
    }));
  });
}
