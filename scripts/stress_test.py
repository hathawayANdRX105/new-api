#!/usr/bin/env python3
"""OpenAI-compatible LLM stress runner for local and k8s smoke/load tests.

No external deps beyond aiohttp. Measures non-streaming latency and streaming TTFT/ITL/TPS.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import math
import statistics
import time
from dataclasses import dataclass, field
from typing import Any

import aiohttp
from aiohttp import web


SHORT_PROMPT = "Reply with OK."
LONG_PROMPT = "Write a detailed but concise explanation of API gateway retry behavior, streaming behavior, and failure isolation."


@dataclass
class Metrics:
    name: str
    latencies: list[float] = field(default_factory=list)
    ttfts: list[float] = field(default_factory=list)
    itls: list[float] = field(default_factory=list)
    output_chars: int = 0
    chunks: int = 0
    status: dict[int, int] = field(default_factory=dict)
    errors: list[str] = field(default_factory=list)
    started: float = 0.0
    ended: float = 0.0

    def record_status(self, code: int) -> None:
        self.status[code] = self.status.get(code, 0) + 1

    def sample_error(self, msg: str) -> None:
        if len(self.errors) < 8:
            self.errors.append(msg[:200])

    @staticmethod
    def pct(values: list[float], p: float) -> float | None:
        if not values:
            return None
        vals = sorted(values)
        idx = max(0, min(len(vals) - 1, math.ceil(len(vals) * p / 100) - 1))
        return vals[idx]

    def summary(self) -> dict[str, Any]:
        total = sum(self.status.values())
        duration = max(self.ended - self.started, 0.001)
        return {
            "name": self.name,
            "requests": total,
            "success": self.status.get(200, 0),
            "success_rate": round(self.status.get(200, 0) * 100 / total, 2) if total else 0,
            "duration_sec": round(duration, 2),
            "rps": round(total / duration, 2),
            "status": self.status,
            "lat_p50": round(self.pct(self.latencies, 50), 3) if self.latencies else None,
            "lat_p90": round(self.pct(self.latencies, 90), 3) if self.latencies else None,
            "lat_p99": round(self.pct(self.latencies, 99), 3) if self.latencies else None,
            "ttft_p50": round(self.pct(self.ttfts, 50), 3) if self.ttfts else None,
            "ttft_p90": round(self.pct(self.ttfts, 90), 3) if self.ttfts else None,
            "ttft_p99": round(self.pct(self.ttfts, 99), 3) if self.ttfts else None,
            "itl_avg": round(statistics.mean(self.itls), 3) if self.itls else None,
            "chunks": self.chunks,
            "chars_per_sec": round(self.output_chars / duration, 2),
            "errors": self.errors,
        }


class MockUpstream:
    def __init__(self, port: int):
        self.port = port
        self.app = web.Application()
        self.app.router.add_get("/v1/models", self.models)
        self.app.router.add_post("/v1/chat/completions", self.chat)
        self.runner: web.AppRunner | None = None

    async def models(self, request: web.Request) -> web.Response:
        return web.json_response({"object": "list", "data": [{"id": "mock-fast"}, {"id": "mock-slow"}]})

    async def chat(self, request: web.Request) -> web.StreamResponse:
        body = await request.json()
        model = body.get("model", "mock-fast")
        stream = bool(body.get("stream"))
        max_tokens = int(body.get("max_tokens") or 16)
        if "bad" in model:
            return web.json_response({"error": {"message": "mock upstream failure"}}, status=500)
        if "slow" in model:
            await asyncio.sleep(0.8)
        if not stream:
            return web.json_response({
                "id": "mock",
                "object": "chat.completion",
                "created": int(time.time()),
                "model": model,
                "choices": [{"index": 0, "message": {"role": "assistant", "content": "OK " * max(1, min(max_tokens, 64))}, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 10, "completion_tokens": max_tokens, "total_tokens": max_tokens + 10},
            })
        resp = web.StreamResponse(status=200, headers={"Content-Type": "text/event-stream"})
        await resp.prepare(request)
        for i in range(max(1, min(max_tokens, 64))):
            chunk = {"choices": [{"delta": {"content": "x"}, "index": 0, "finish_reason": None}]}
            await resp.write(f"data: {json.dumps(chunk)}\n\n".encode())
            await asyncio.sleep(0.02)
        await resp.write(b"data: [DONE]\n\n")
        await resp.write_eof()
        return resp

    async def start(self) -> None:
        self.runner = web.AppRunner(self.app)
        await self.runner.setup()
        await web.TCPSite(self.runner, "127.0.0.1", self.port).start()

    async def stop(self) -> None:
        if self.runner:
            await self.runner.cleanup()


async def one_non_stream(session: aiohttp.ClientSession, args: argparse.Namespace, metrics: Metrics, prompt: str, max_tokens: int) -> None:
    start = time.perf_counter()
    try:
        async with session.post(
            f"{args.target_url.rstrip('/')}/chat/completions",
            headers={"Authorization": f"Bearer {args.api_key}", "Content-Type": "application/json", **host_header(args)},
            json={"model": args.model, "messages": [{"role": "user", "content": prompt}], "max_tokens": max_tokens, "stream": False},
            timeout=aiohttp.ClientTimeout(total=args.request_timeout),
        ) as resp:
            text = await resp.text()
            metrics.record_status(resp.status)
            latency = time.perf_counter() - start
            metrics.latencies.append(latency)
            if resp.status == 200:
                metrics.ttfts.append(latency)
                metrics.output_chars += len(text)
            else:
                metrics.sample_error(f"{resp.status}: {text}")
    except Exception as exc:
        metrics.record_status(599)
        metrics.latencies.append(time.perf_counter() - start)
        metrics.sample_error(type(exc).__name__ + ": " + str(exc))


async def one_stream(session: aiohttp.ClientSession, args: argparse.Namespace, metrics: Metrics, prompt: str, max_tokens: int) -> None:
    start = time.perf_counter()
    first = None
    last = None
    try:
        async with session.post(
            f"{args.target_url.rstrip('/')}/chat/completions",
            headers={"Authorization": f"Bearer {args.api_key}", "Content-Type": "application/json", **host_header(args)},
            json={"model": args.model, "messages": [{"role": "user", "content": prompt}], "max_tokens": max_tokens, "stream": True},
            timeout=aiohttp.ClientTimeout(total=args.request_timeout),
        ) as resp:
            metrics.record_status(resp.status)
            async for raw in resp.content:
                now = time.perf_counter()
                if first is None:
                    first = now
                    metrics.ttfts.append(first - start)
                if last is not None:
                    metrics.itls.append(now - last)
                last = now
                line = raw.decode(errors="ignore")
                metrics.output_chars += len(line)
                if line.startswith("data:") and "[DONE]" not in line:
                    metrics.chunks += 1
            metrics.latencies.append(time.perf_counter() - start)
            if resp.status != 200:
                metrics.sample_error(f"stream status {resp.status}")
    except Exception as exc:
        metrics.record_status(599)
        metrics.latencies.append(time.perf_counter() - start)
        metrics.sample_error(type(exc).__name__ + ": " + str(exc))


def host_header(args: argparse.Namespace) -> dict[str, str]:
    return {"Host": args.host_header} if args.host_header else {}


async def worker(session: aiohttp.ClientSession, args: argparse.Namespace, metrics: Metrics) -> None:
    end = time.perf_counter() + args.duration
    while time.perf_counter() < end:
        long_case = random_ratio(args.long_ratio)
        stream_case = random_ratio(args.stream_ratio)
        prompt = LONG_PROMPT if long_case else SHORT_PROMPT
        max_tokens = args.long_tokens if long_case else args.short_tokens
        if stream_case:
            await one_stream(session, args, metrics, prompt, max_tokens)
        else:
            await one_non_stream(session, args, metrics, prompt, max_tokens)
        await asyncio.sleep(args.pacing_ms / 1000)


def random_ratio(ratio: float) -> bool:
    # local import avoids global RNG surprises in forked runners; stdlib only.
    import random
    return random.random() < ratio


async def run(args: argparse.Namespace) -> list[dict[str, Any]]:
    mock = MockUpstream(args.mock_port) if args.use_mock else None
    if mock:
        await mock.start()
    try:
        results = []
        scenarios = [(args.scenario, args.concurrency, args.duration)]
        if args.scenario == "matrix":
            scenarios = [
                ("short_non_stream", max(1, args.concurrency), max(5, args.short_duration)),
                ("long_stream", max(1, args.stream_concurrency), max(5, args.long_duration)),
                ("mixed_stream_non_stream", max(1, args.concurrency), max(5, args.duration)),
            ]
        connector = aiohttp.TCPConnector(limit=max(args.concurrency, args.stream_concurrency) * 3)
        async with aiohttp.ClientSession(connector=connector) as session:
            for name, concurrency, duration in scenarios:
                local = argparse.Namespace(**vars(args))
                local.concurrency = concurrency
                local.duration = duration
                if name == "short_non_stream":
                    local.stream_ratio = 0.0
                    local.long_ratio = 0.0
                elif name == "long_stream":
                    local.stream_ratio = 1.0
                    local.long_ratio = 1.0
                metrics = Metrics(name=name)
                metrics.started = time.perf_counter()
                await asyncio.gather(*(worker(session, local, metrics) for _ in range(concurrency)))
                metrics.ended = time.perf_counter()
                results.append(metrics.summary())
        return results
    finally:
        if mock:
            await mock.stop()


def write_report(results: list[dict[str, Any]], args: argparse.Namespace) -> None:
    lines = [
        "# new-api K8s/Local LLM 压测报告",
        "",
        f"- target_url: `{args.target_url}`",
        f"- model: `{args.model}`",
        f"- stream_ratio: `{args.stream_ratio}` long_ratio: `{args.long_ratio}`",
        f"- short_tokens/long_tokens: `{args.short_tokens}/{args.long_tokens}`",
        "",
        "| 场景 | 请求 | 成功率 | RPS | Lat P50/P90/P99(s) | TTFT P50/P90/P99(s) | ITL avg(s) | chunks | chars/s | 状态码 |",
        "|---|---:|---:|---:|---|---|---:|---:|---:|---|",
    ]
    for r in results:
        lines.append(
            f"| {r['name']} | {r['requests']} | {r['success_rate']}% | {r['rps']} | "
            f"{r['lat_p50']}/{r['lat_p90']}/{r['lat_p99']} | "
            f"{r['ttft_p50']}/{r['ttft_p90']}/{r['ttft_p99']} | "
            f"{r['itl_avg']} | {r['chunks']} | {r['chars_per_sec']} | `{r['status']}` |"
        )
    errors = [e for r in results for e in r.get("errors", [])]
    if errors:
        lines += ["", "## Error samples", *[f"- `{e}`" for e in errors[:10]]]
    data = {"args": vars(args), "results": results}
    with open(args.report_file, "w", encoding="utf-8") as f:
        f.write("\n".join(lines) + "\n")
    with open(args.json_file, "w", encoding="utf-8") as f:
        json.dump(data, f, ensure_ascii=False, indent=2)


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser()
    p.add_argument("--scenario", default="matrix", choices=["matrix", "short_non_stream", "long_stream", "mixed_stream_non_stream"])
    p.add_argument("--target-url", default="http://127.0.0.1:3182/v1")
    p.add_argument("--host-header", default="")
    p.add_argument("--api-key", default="")
    p.add_argument("--model", default="kimi-k3")
    p.add_argument("--concurrency", type=int, default=30)
    p.add_argument("--stream-concurrency", type=int, default=10)
    p.add_argument("--duration", type=float, default=60)
    p.add_argument("--short-duration", type=float, default=20)
    p.add_argument("--long-duration", type=float, default=60)
    p.add_argument("--short-tokens", type=int, default=16)
    p.add_argument("--long-tokens", type=int, default=256)
    p.add_argument("--stream-ratio", type=float, default=0.5)
    p.add_argument("--long-ratio", type=float, default=0.5)
    p.add_argument("--pacing-ms", type=int, default=10)
    p.add_argument("--request-timeout", type=float, default=60)
    p.add_argument("--use-mock", action="store_true")
    p.add_argument("--mock-port", type=int, default=8099)
    p.add_argument("--report-file", default="report.md")
    p.add_argument("--json-file", default="metrics.json")
    return p.parse_args()


if __name__ == "__main__":
    args = parse_args()
    result = asyncio.run(run(args))
    write_report(result, args)
