"""Record the HTTP protocol Claude Code headless speaks to ANTHROPIC_BASE_URL.

Starts a local listener that logs method, path, headers (secrets redacted) and
the shape of the JSON body, answers 401, and runs `claude -p` against it.

Usage: python3 -I recorder.py <out_dir> <label> <workdir> <status> [timeout_s] -- <claude args...>
Status 200 answers a fake SSE stream (one text block, end_turn); other statuses answer an error.
The claude binary comes from CLAUDE_BIN, else from PATH.
"""
import json
import os
import pathlib
import shutil
import subprocess
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

REDACT = {"x-api-key", "authorization", "cookie"}
TOKEN_PREFIX = "forja-run-token-"


def shape(value, depth=0):
    """Describe a JSON value without copying its content."""
    if isinstance(value, dict):
        if depth >= 2:
            return {"type": "object", "keys": sorted(value.keys())}
        return {k: shape(v, depth + 1) for k, v in value.items()}
    if isinstance(value, list):
        return {"type": "array", "len": len(value), "first": shape(value[0], depth + 1) if value else None}
    if isinstance(value, str):
        return {"type": "string", "len": len(value)}
    if isinstance(value, bool):
        return value
    if isinstance(value, (int, float)):
        return value
    return {"type": type(value).__name__}


def summarize_body(raw):
    out = {"bytes": len(raw)}
    try:
        body = json.loads(raw)
    except Exception as exc:  # noqa: BLE001 - recorded, not hidden
        out["json_error"] = str(exc)
        return out
    out["top_level_keys"] = sorted(body.keys()) if isinstance(body, dict) else None
    if not isinstance(body, dict):
        return out
    for key in ("model", "stream", "max_tokens", "temperature"):
        if key in body:
            out[key] = body[key]
    if "thinking" in body:
        out["thinking"] = body["thinking"]
    if "output_config" in body:
        out["output_config"] = body["output_config"]
    if "metadata" in body:
        out["metadata_keys"] = sorted(body["metadata"].keys()) if isinstance(body["metadata"], dict) else shape(body["metadata"])
    if "system" in body:
        out["system"] = shape(body["system"])
    if "messages" in body:
        msgs = body["messages"]
        out["messages"] = {"len": len(msgs), "roles": [m.get("role") for m in msgs]}
    if "tools" in body:
        out["tools"] = {"len": len(body["tools"]), "names": [t.get("name") for t in body["tools"]]}
    if "tool_choice" in body:
        out["tool_choice"] = body["tool_choice"]
    if "betas" in body:
        out["betas"] = body["betas"]
    return out


class Recorder(BaseHTTPRequestHandler):
    log = []
    token = ""
    status = 401
    lock = threading.Lock()

    def log_message(self, fmt, *args):  # silence default stderr log
        return

    def _record(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        headers = {}
        for name, value in self.headers.items():
            lname = name.lower()
            if lname in REDACT:
                headers[lname] = {
                    "redacted": True,
                    "len": len(value),
                    "equals_run_token": value == self.token,
                }
            else:
                headers[lname] = value
        entry = {
            "t": time.time(),
            "method": self.command,
            "path": self.path,
            "http_version": self.request_version,
            "headers": headers,
            "body": summarize_body(raw) if raw else None,
            "raw_body": raw.decode("utf-8", "replace") if raw else None,
        }
        with self.lock:
            self.log.append(entry)
        if self.status == 200 and self.command == "POST":
            self._sse_ok()
            return
        kinds = {
            401: ("authentication_error", "invalid x-api-key"),
            400: ("invalid_request_error", "forja recorder: request recorded"),
            429: ("rate_limit_error", "forja: run token cap reached"),
        }
        kind, msg = kinds.get(self.status, ("api_error", "forja recorder"))
        payload = json.dumps({"type": "error", "error": {"type": kind, "message": msg}}).encode()
        self.send_response(self.status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.send_header("request-id", "req_forja_recorder")
        if self.status == 429:
            self.send_header("retry-after", "1")
        self.end_headers()
        self.wfile.write(payload)

    def _sse_ok(self):
        """Answer like the Messages API streaming a one-word end_turn reply."""
        events = [
            ("message_start", {"type": "message_start", "message": {
                "id": "msg_forja_recorder", "type": "message", "role": "assistant",
                "model": "claude-opus-5-5", "content": [], "stop_reason": None,
                "stop_sequence": None,
                "usage": {"input_tokens": 1234, "output_tokens": 1,
                          "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0}}}),
            ("content_block_start", {"type": "content_block_start", "index": 0,
                                     "content_block": {"type": "text", "text": ""}}),
            ("content_block_delta", {"type": "content_block_delta", "index": 0,
                                     "delta": {"type": "text_delta", "text": "forja-recorder-ok"}}),
            ("content_block_stop", {"type": "content_block_stop", "index": 0}),
            ("message_delta", {"type": "message_delta",
                               "delta": {"stop_reason": "end_turn", "stop_sequence": None},
                               "usage": {"output_tokens": 56}}),
            ("message_stop", {"type": "message_stop"}),
        ]
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream; charset=utf-8")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("request-id", "req_forja_recorder")
        self.send_header("Connection", "close")
        self.end_headers()
        for name, data in events:
            self.wfile.write(f"event: {name}\ndata: {json.dumps(data)}\n\n".encode())
            self.wfile.flush()

    do_GET = _record
    do_POST = _record
    do_PUT = _record
    do_DELETE = _record
    do_HEAD = _record
    do_OPTIONS = _record


def main():
    argv = sys.argv[1:]
    sep = argv.index("--")
    out_dir, label, workdir, status = argv[0], argv[1], argv[2], int(argv[3])
    timeout_s = int(argv[4]) if sep > 4 else 120
    claude_args = argv[sep + 1:]
    Recorder.status = status
    out = pathlib.Path(out_dir)
    out.mkdir(parents=True, exist_ok=True)
    pathlib.Path(workdir).mkdir(parents=True, exist_ok=True)

    token = TOKEN_PREFIX + label
    Recorder.token = token
    Recorder.log = []
    server = ThreadingHTTPServer(("127.0.0.1", 0), Recorder)
    port = server.server_address[1]
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()

    env = {k: v for k, v in os.environ.items()
           if not k.startswith("ANTHROPIC_") and k not in ("CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT")}
    env["ANTHROPIC_BASE_URL"] = f"http://127.0.0.1:{port}"
    env["ANTHROPIC_API_KEY"] = token
    env["HOME"] = env.get("HOME", "")

    claude_bin = os.environ.get("CLAUDE_BIN") or shutil.which("claude") or "claude"
    cmd = [claude_bin] + claude_args
    started = time.time()
    try:
        proc = subprocess.run(cmd, cwd=workdir, env=env, capture_output=True, text=True, timeout=timeout_s)
        exit_code, stdout, stderr, timed_out = proc.returncode, proc.stdout, proc.stderr, False
    except subprocess.TimeoutExpired as exc:
        exit_code, timed_out = None, True
        stdout = exc.stdout.decode() if isinstance(exc.stdout, bytes) else (exc.stdout or "")
        stderr = exc.stderr.decode() if isinstance(exc.stderr, bytes) else (exc.stderr or "")
    elapsed = time.time() - started
    time.sleep(1.0)  # let late requests land
    server.shutdown()

    result = {
        "label": label,
        "status_returned": status,
        "timeout_s": timeout_s,
        "claude_args": claude_args,
        "base_url": env["ANTHROPIC_BASE_URL"],
        "exit_code": exit_code,
        "timed_out": timed_out,
        "elapsed_s": round(elapsed, 2),
        "requests": Recorder.log,
        "stdout": stdout,
        "stderr": stderr,
    }
    path = out / f"{label}.json"
    path.write_text(json.dumps(result, indent=2))
    print(json.dumps({
        "label": label,
        "exit_code": exit_code,
        "timed_out": timed_out,
        "elapsed_s": result["elapsed_s"],
        "requests": [(r["method"], r["path"]) for r in Recorder.log],
        "saved": str(path),
    }))


if __name__ == "__main__":
    main()
