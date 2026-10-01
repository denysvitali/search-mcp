#!/usr/bin/env python3
"""Opt-in live MCP audit using Python 3.9+ and its standard library.

Build the binary first. Results and server logs are written outside the repo:
    go build -o /tmp/search-mcp .
    python3 scripts/audit_mcp.py --binary /tmp/search-mcp --output /tmp/mcp-audit
Pass server flags after --. The audit uses live public sites and may take minutes.
"""

import argparse
import json
import pathlib
import queue
import re
import subprocess
import threading
import time
from urllib.parse import urlparse


class Probe:
    def __init__(self, binary, output, flags):
        self.output = pathlib.Path(output)
        self.output.mkdir(parents=True, exist_ok=True)
        self.log = (self.output / "stderr.log").open("w")
        self.process = subprocess.Popen(
            [str(pathlib.Path(binary).resolve()), "serve", *flags],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=self.log,
            text=True,
        )
        self.messages = queue.Queue()
        self.records = []
        self.request_id = 0
        threading.Thread(target=self.pump, daemon=True).start()

    def pump(self):
        for line in self.process.stdout:
            try:
                self.messages.put(json.loads(line))
            except ValueError:
                self.messages.put({"protocol_error": line[:200]})
        self.messages.put({"protocol_error": "server stdout closed"})

    def send(self, message):
        self.process.stdin.write(json.dumps(message) + "\n")
        self.process.stdin.flush()

    def rpc(self, method, params):
        self.request_id += 1
        started = time.monotonic()
        self.send({
            "jsonrpc": "2.0", "id": self.request_id,
            "method": method, "params": params,
        })
        deadline = started + 70
        while True:
            response = self.messages.get(timeout=max(0, deadline - time.monotonic()))
            if "protocol_error" in response:
                raise RuntimeError(response["protocol_error"])
            if response.get("id") == self.request_id:
                break
        record = {
            "method": method, "params": params,
            "seconds": round(time.monotonic() - started, 2), "response": response,
        }
        self.records.append(record)
        (self.output / "results.json").write_text(json.dumps(self.records, indent=2))
        if "error" in response:
            raise RuntimeError(response["error"])
        return response["result"]

    def call(self, name, arguments):
        result = self.rpc("tools/call", {"name": name, "arguments": arguments})
        structured = result.get("structuredContent", {})
        if not structured:
            for block in result.get("content", []):
                try:
                    structured = json.loads(block.get("text", ""))
                    break
                except ValueError:
                    continue
        print(json.dumps({
            "tool": name, "args": arguments,
            "seconds": self.records[-1]["seconds"],
            "error": result.get("isError", False),
            "preview": str(structured or result)[:350],
        }), flush=True)
        return structured

    def close(self):
        self.process.stdin.close()
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.terminate()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait()
        self.log.close()
        self.process.stdout.close()


def run(probe):
    probe.rpc("initialize", {
        "protocolVersion": "2025-06-18", "capabilities": {},
        "clientInfo": {"name": "live-audit", "version": "1"},
    })
    probe.send({"jsonrpc": "2.0", "method": "notifications/initialized"})
    tools = probe.rpc("tools/list", {})
    print("TOOLS", [tool["name"] for tool in tools.get("tools", [])], flush=True)
    batch = probe.call("search_batch", {
        "queries": [
            "Archimedes palimpsest multispectral imaging",
            "Herculaneum scrolls virtual unwrapping",
            "Codex Sinaiticus digitisation manuscripts",
        ], "count": 5, "max_per_host": 2,
    })
    for item in batch.get("items", []):
        for result in item.get("response", {}).get("results", [])[:2]:
            probe.call("web_read", {"url": result["url"], "max_length": 1500})
    probe.call("provider_status", {})
    manuscript = "https://www.archimedespalimpsest.org/digital/"
    paper = (
        "https://www.imaging.org/common/uploaded%20files/pdfs/"
        "Papers/2001/PICS-0-251/4623.pdf"
    )
    for url in [manuscript, (
        "https://hmml.org/stories/"
        "seeing-invisible-multispectral-imaging-ancient-medieval-manuscripts/"
    )]:
        probe.call("web_read", {"url": url, "max_length": 1800})
        links = probe.call("web_read", {
            "url": url, "links": True, "max_length": 3000,
        })
        internal = [
            link for link in re.findall(r"\]\((https?://[^)]+)\)", links.get("content", ""))
            if urlparse(link).hostname == urlparse(url).hostname and link != url
        ]
        if internal:
            probe.call("web_read", {"url": internal[0], "max_length": 1200})
    for arguments in [
        {"url": paper},
        {"url": paper, "pages": "1"},
        {"url": paper, "query": "ink", "max_results": 2},
        {"url": paper, "query": "zzzx-no-match", "max_results": 2},
        {"url": "https://arxiv.org/pdf/2304.10914", "pages": "1"},
    ]:
        probe.call("read_pdf", arguments)
    for arguments in [
        {"url": "https://arxiv.org/pdf/2304.10914", "max_length": 1000},
        {"url": "https://arxiv.org/abs/2304.10914", "max_length": 1000},
        {"url": manuscript, "start_index": 2000, "max_length": 9223372036854774784},
        {"url": manuscript, "query": "images", "context": 0},
        {"url": manuscript, "query": "images", "max_length": 100},
    ]:
        probe.call("web_read", arguments)
    probe.call("search_batch", {
        "queries": ["", " ", "Archimedes", "Archimedes"], "count": 1,
    })
    # Invalid options must fail before performing network I/O.
    probe.call("web_read", {"url": "https://example.com", "links": True, "start_index": -1})
    probe.call("web_read", {"url": "https://example.com", "links": True, "context": 11})
    probe.call("search", {"query": "Archimedes", "include_domains": ["https://bad-domain"]})
    probe.call("provider_status", {})
    print(f"Saved {len(probe.records)} protocol exchanges to {probe.output / 'results.json'}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--output", default="/tmp/search-mcp-audit-results")
    parser.add_argument("server_flags", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    flags = args.server_flags[1:] if args.server_flags[:1] == ["--"] else args.server_flags
    probe = Probe(args.binary, args.output, flags)
    try:
        run(probe)
    finally:
        probe.close()


if __name__ == "__main__":
    main()
