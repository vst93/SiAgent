// Local protocol fixture only; never calls an external model service.
import { createServer } from "node:http";
const requests = [];
createServer(async (request, response) => {
  if (request.method === "GET" && request.url === "/requests") {
    response.setHeader("Content-Type", "application/json");
    response.end(JSON.stringify(requests));
    return;
  }
  if (request.method !== "POST" || request.url !== "/v1/chat/completions") {
    response.writeHead(404).end();
    return;
  }
  if (request.headers.authorization !== "Bearer container-fixture-key") {
    response.writeHead(401).end('{"error":"invalid fixture credential"}');
    return;
  }
  let raw = "";
  for await (const chunk of request) raw += chunk;
  const body = JSON.parse(raw);
  requests.push(body);
  const content = body.messages.at(-1).content;
  const question = typeof content === "string" ? content : content.filter((part) => part.type === "text").at(-1)?.text ?? "";
  const answer = `echo: ${question}`;
  if (!body.stream) {
    response.setHeader("Content-Type", "application/json");
    response.end(
      JSON.stringify({ choices: [{ message: { content: answer } }] }),
    );
    return;
  }
  response.setHeader("Content-Type", "text/event-stream");
  for (const choice of [
    { delta: { role: "assistant", content: answer }, finish_reason: null },
    { delta: {}, finish_reason: "stop" },
  ]) {
    response.write(
      `data: ${JSON.stringify({ id: "fixture", object: "chat.completion.chunk", created: 1, model: body.model, choices: [{ index: 0, ...choice }] })}\n\n`,
    );
  }
  response.end("data: [DONE]\n\n");
}).listen(Number(process.env.PORT || 8000), process.env.HOST || "127.0.0.1");
