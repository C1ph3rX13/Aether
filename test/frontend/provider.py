"""Controlled HTTP Provider for native Catalog/Workspace acceptance."""
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
import sys
import json
import re


class Provider(BaseHTTPRequestHandler):
    def respond(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        request = json.loads(body) if body else {}
        if self.path.startswith("/worker-check/"):
            token = self.headers.get("Authorization", "").removeprefix("Bearer ")
            status = 429 if self.path.startswith("/worker-check/rate-limit/") else 401 if token == "rejected-worker-credential" else 200
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            # Deliberately echo the candidate credential on rejection. The real
            # API must redact and bound it before the browser sees the failure.
            detail = f"Rejected credential {token} " + "provider detail " * 100
            self.wfile.write(json.dumps({"detail": detail} if status != 200 else {}).encode())
            return
        if self.path == "/workspace/chat/completions" and request.get("stream"):
            self.complete(request)
            return
        self.send_response(401 if Path(sys.argv[2]).exists() and not self.path.startswith("/workspace/") else 200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b"{}")

    def complete(self, request):
        messages = request["messages"]
        if messages[-1].get("role") == "tool":
            delta = {"role": "assistant", "content": "Result file written."}
            finish = "stop"
        else:
            prompt = str(messages[-1].get("content", ""))
            target = re.search(r"/tmp/aether-invocations/[0-9a-f-]+/result\.toml", prompt)
            if not target:
                self.send_error(400, "Invocation result file is missing from the prompt")
                return
            content = '[outcome]\nkind = "conclude_intent"\n[outcome.fact]\ndescription = "Verified Bootstrap fixture evidence"\n' if "AETHER BOOTSTRAP INPUT BEGIN" in prompt else '[outcome]\nkind = "no_change"\n'
            print(f"Controlled Workspace outcome for {target.group()}: {content!r}", flush=True)
            delta = {"role": "assistant", "tool_calls": [{"index": 0, "id": "call_fixture", "type": "function", "function": {"name": "write", "arguments": json.dumps({"path": target.group(), "content": content})}}]}
            finish = "tool_calls"
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for delta, reason in [(delta, None), ({}, finish)]:
            chunk = {"id": "workspace-fixture", "object": "chat.completion.chunk", "created": 1, "model": request["model"], "choices": [{"index": 0, "delta": delta, "finish_reason": reason}]}
            self.wfile.write(f"data: {json.dumps(chunk)}\n\n".encode())
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    do_GET = respond
    do_POST = respond

    def log_message(self, *_args):
        pass


HTTPServer(("127.0.0.1", int(sys.argv[1])), Provider).serve_forever()
