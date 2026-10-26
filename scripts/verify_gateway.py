#!/usr/bin/env python3
"""Run isolated real-model API checks. Does not touch a running deployment."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import time
import urllib.request
import urllib.error


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", required=True)
    parser.add_argument("--work-dir", required=True)
    parser.add_argument("--port", type=int, default=19134)
    args = parser.parse_args()
    work = Path(args.work_dir).resolve()
    work.mkdir(parents=True, exist_ok=True)
    base = f"http://127.0.0.1:{args.port}"
    compat = f"http://127.0.0.1:{args.port + 1}"
    env = dict(os.environ, OLLAMA_HOST=f"127.0.0.1:{args.port}",
               OLLAMA_COMPAT_LISTEN=f"127.0.0.1:{args.port + 1}",
               OLLAMA_MODELS=str(work / "models"), OLLAMA_NUM_PARALLEL="2",
               OLLAMA_CONTEXT_LENGTH="2048")
    env.pop("MODEL_HUB_REGISTER_URL", None)
    log = open(work / "server.log", "w")
    server = subprocess.Popen([str(Path(args.binary).resolve()), "serve"], env=env, stdout=log, stderr=log)

    def request(path, body=None, endpoint=base):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(endpoint + path, data=data,
                                     headers={"Content-Type": "application/json", "X-Task-Id": "gateway-verification"})
        try:
            return urllib.request.urlopen(req, timeout=1800)
        except urllib.error.HTTPError as exc:
            raise RuntimeError(f"{path}: HTTP {exc.code}: {exc.read().decode()}") from exc

    def api(path, body=None, endpoint=base):
        with request(path, body, endpoint) as resp:
            assert resp.headers.get("X-Task-Id") == "gateway-verification"
            return json.load(resp)

    try:
        for _ in range(120):
            try:
                version = api("/api/version")
                assert version == api("/api/version", endpoint=compat)
                break
            except OSError:
                if server.poll() is not None:
                    raise RuntimeError("server exited; inspect server.log")
                time.sleep(1)
        else:
            raise RuntimeError("server startup timed out")
        models = ["qllama/bge-reranker-v2-m3:q8_0", "AuditAid/Qwen3_Reranker:0.6B_Q8", "qwen3:0.6b", "all-minilm", "qwen3.5:2b", "tev1:0.8b"]
        for model in models:
            print(f"pull {model}", flush=True)
            api("/api/pull", {"model": model, "stream": False})
        for model in models[:2]:
            body = {"model": model, "query": "What is the capital of France?",
                    "documents": ["Bananas are yellow fruit.", "Paris is the capital of France."],
                    "return_documents": True, "options": {"num_gpu": 0, "num_thread": 4}, "keep_alive": 0}
            for endpoint in (base, compat):
                for path in ("/v1/rerank", "/rerank", "/api/rerank"):
                    result = api(path, body, endpoint)
                    ranks = result["results"]
                    assert len(ranks) == 2 and ranks[0]["index"] == 1, result
                    assert 0 <= ranks[1]["relevance_score"] < ranks[0]["relevance_score"] <= 1, result
                    assert ranks[0]["document"]["text"] == body["documents"][1]
            top = api("/v1/rerank", dict(body, top_n=1, documents=[{"text": doc} for doc in body["documents"]]))
            assert len(top["results"]) == 1 and top["results"][0]["index"] == 1
            print(f"rerank passed: {model}", flush=True)
        chat = {"model": "qwen3:0.6b", "messages": [{"role": "user", "content": "Say hello."}],
                "think": False, "stream": False, "options": {"num_ctx": 2048, "num_predict": 16, "num_gpu": 0, "num_thread": 4}}
        assert api("/v1/chat/completions", chat)["choices"]
        assert api("/api/chat", chat, compat)["message"]
        assert api("/v1/completions", {"model": "qwen3:0.6b", "prompt": "Hello", "think": False,
                   "stream": False, "options": {"num_predict": 8, "num_gpu": 0, "num_thread": 4}}, compat)["choices"]
        assert api("/v1/embeddings", {"model": "all-minilm", "input": "hello"})["data"]
        # Model Hub's default QA model declares a minimum Ollama version.
        # Exercise it with the real dated release binary, not a 0.0.0 dev build.
        qa = dict(chat, model="qwen3.5:2b", keep_alive=0)
        assert api("/api/chat", qa)["message"]["content"]
        assert api("/v1/chat/completions", qa, compat)["choices"][0]["message"]["content"]

        decision_body = {"model": "tev1:0.8b", "state": "Hello, I was charged twice.", "keep_alive": 0,
                         "questions": {"team": {"type": "choice", "instructions": "Which team should handle this?",
                                                "criteria": {"billing": "Payments and refunds", "technical": "Software bugs"}},
                                       "greeting": {"type": "noul", "instructions": "Does the message contain a greeting?"},
                                       "urgency": {"type": "score", "instructions": "Rate urgency.", "criteria": ["Routine", "Urgent"]}}}
        assert "decision" in api("/api/show", {"model": "tev1:0.8b"})["capabilities"]
        for endpoint in (base, compat):
            answers = api("/v1/systemone", decision_body, endpoint)["answers"]
            assert answers["team"]["choice"] in ["billing", "technical"]
            assert 0 <= answers["team"]["confidence"] <= 1
            assert 0 <= answers["greeting"]["noul"] <= 1
            assert "score" in answers["urgency"]
        print("PASS: real local Tev1 decision capability and Choice/Noul/Score on both ports", flush=True)

        # Read one SSE chunk, then close the socket during a long generation.
        stream_body = dict(chat, stream=True, messages=[{"role": "user", "content": "Write a very long story."}],
                           options={"num_ctx": 2048, "num_predict": 2000, "num_gpu": 0, "num_thread": 4})
        with request("/v1/chat/completions", stream_body, compat) as resp:
            while True:
                line = resp.readline()
                if line.startswith(b"data:") and b"choices" in line:
                    break
                assert line, "empty SSE response"
            snapshot = api("/metrics")
            assert snapshot["active_slots"] >= 1 and snapshot["phase"] == "decode", snapshot
            assert snapshot["active_slots"] == api("/metrics", endpoint=compat)["active_slots"]
        for _ in range(100):
            if api("/metrics")["active_slots"] == 0:
                break
            time.sleep(0.1)
        else:
            raise AssertionError("cancelled request leaked active metrics slot")
        print("PASS: both ports, native/OpenAI APIs, BGE/Qwen rerank, Qwen3.5 QA, embedding, SSE, cancellation, metrics", flush=True)
    finally:
        server.terminate()
        try:
            server.wait(timeout=30)
        except subprocess.TimeoutExpired:
            server.kill()
            server.wait()
        log.close()


if __name__ == "__main__":
    main()
