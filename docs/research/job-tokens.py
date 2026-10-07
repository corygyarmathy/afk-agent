#!/usr/bin/env python3
"""Break an opencode session tree's tokens down by what produced them.

Read-only. Usage:

    job-tokens.py DB ROOT_SESSION_ID [--json] [--price IN,OUT,CACHE_READ]
    job-tokens.py DB --list            # root sessions, newest first

DB is opencode's SQLite store (opencode 1.18.x: <data>/opencode/opencode*.db;
for the afk-agent service, HOME=/var/lib/afk-agent, so
/var/lib/afk-agent/.local/share/opencode/). It is opened with mode=ro.

Method. Every step (one model call) ends in a step-finish part carrying that
call's tokens. A step's prompt is input + cache.read + cache.write: the whole
context, re-sent. Context grows between steps by what step k produced (its
text, reasoning and tool-call arguments) and what its tools returned. The
growth, prompt[k+1] - prompt[k], is split across step k's tool outputs in
proportion to their characters, after taking off step k's own output. A
chunk that enters before step j of N is paid for N - j + 1 times, so a
session's input is the sum over chunks of size x times re-sent, and every
chunk is labelled by what produced it.
"""
import json
import re
import sqlite3
import sys
from collections import defaultdict


# USD per token. opencode-go/deepseek-v4-pro list price (opencode 1.18.33's
# catalogue: 0.66 in, 1.98 out, 0.022 cache read, per million). Override with
# --price IN,OUT,CACHE_READ (per million).
PRICE = {"input": 0.66e-6, "output": 1.98e-6, "cache_read": 0.022e-6}


def open_db(path):
    return sqlite3.connect(f"file:{path}?mode=ro", uri=True)


def bash_kind(cmd):
    c = cmd.strip()
    # The first command word that is not cd/env.
    for seg in re.split(r"&&|\|\||;|\n", c):
        w = seg.strip().split()
        while w and (w[0] in ("cd", "env", "sudo", "time") or "=" in w[0]):
            w = w[2:] if w[0] == "cd" else w[1:]
        if not w:
            continue
        h = w[0]
        if h == "gh":
            return "bash: gh"
        if h == "git":
            sub = w[1] if len(w) > 1 else ""
            if sub in ("diff", "show", "log"):
                return "bash: git diff/show/log"
            return "bash: git (other)"
        if h in ("nix", "nixos-rebuild", "treefmt", "nixfmt", "statix", "deadnix"):
            return "bash: nix build/check/fmt"
        if h in ("go", "make", "pytest", "npm", "cargo") or "test" in h or h.startswith("scripts/"):
            return "bash: build/test"
        if h in ("cat", "sed", "head", "tail", "less", "nl", "awk", "wc"):
            return "bash: file read"
        if h in ("grep", "rg", "find", "ls", "fd", "tree"):
            return "bash: search/list"
        return "bash: other"
    return "bash: other"


def label(p):
    t = p.get("type")
    if t != "tool":
        return None
    tool = p.get("tool", "?")
    inp = (p.get("state") or {}).get("input") or {}
    if tool == "bash":
        return bash_kind(inp.get("command", ""))
    if tool == "skill":
        return "skill load"
    if tool == "task":
        return "sub-agent result"
    if tool in ("read",):
        return "read"
    if tool in ("grep", "glob", "list"):
        return "search/list"
    if tool in ("edit", "write", "patch", "multiedit", "apply_patch"):
        return "edit/write result"
    if tool in ("webfetch", "websearch"):
        return "web"
    return f"tool: {tool}"


def tool_output(p):
    st = p.get("state") or {}
    out = st.get("output")
    if out is None:
        out = st.get("error") or ""
    return out if isinstance(out, str) else json.dumps(out)


def session_parts(db, sid):
    rows = db.execute(
        "select p.data, m.data from part p join message m on m.id = p.message_id "
        "where p.session_id = ? order by m.time_created, m.id, p.id",
        (sid,),
    ).fetchall()
    return [(json.loads(a), json.loads(b)) for a, b in rows]


def analyse(db, sid):
    """One session: its steps, and its input broken down by origin."""
    parts = session_parts(db, sid)
    steps = []  # each: tokens, tool parts that ran in it
    cur = None
    user_chars_before = 0
    for p, m in parts:
        t = p.get("type")
        if m.get("role") == "user":
            if t == "text":
                if cur is None and not steps:
                    user_chars_before += len(p.get("text", ""))
                else:
                    # A later user message: interactive turns, or a resume.
                    (steps[-1] if steps else {}).setdefault("user_after", 0)
                    if steps:
                        steps[-1]["user_after"] += len(p.get("text", ""))
            continue
        if t == "step-start":
            cur = {"tools": [], "user_after": 0}
        elif t == "tool" and cur is not None:
            cur["tools"].append(p)
        elif t == "step-finish" and cur is not None:
            tk = p.get("tokens") or {}
            c = tk.get("cache") or {}
            cur["prompt"] = tk.get("input", 0) + c.get("read", 0) + c.get("write", 0)
            cur["output"] = tk.get("output", 0)
            cur["reasoning"] = tk.get("reasoning", 0)
            cur["cache_read"] = c.get("read", 0)
            steps.append(cur)
            cur = None
    n = len(steps)
    if n == 0:
        return None

    chunks = []  # (label, tokens, enters_before_step_index, detail)
    chunks.append(("baseline: system prompt, tools, AGENTS.md, first message", steps[0]["prompt"], 0, ""))
    for k in range(n - 1):
        s = steps[k]
        delta = steps[k + 1]["prompt"] - s["prompt"]
        if delta < 0:
            # Compaction or a pruned context: nothing to attribute.
            chunks.append(("(context shrank: compaction/pruning)", delta, k + 1, ""))
            continue
        # The step's own text, tool-call arguments and reasoning. Reasoning is
        # re-sent: on steps with no tool output the growth is output +
        # reasoning to within a few tokens (opencode 1.18.33, deepseek-v4-*).
        own_out = min(delta, s["output"])
        own_reason = min(delta - own_out, s["reasoning"])
        rest = delta - own_out - own_reason
        outs = [(label(p), len(tool_output(p)), p) for p in s["tools"]]
        total_chars = sum(c for _, c, _ in outs) + s.get("user_after", 0)
        chunks.append(("model's own output (text, tool-call args), re-sent", own_out, k + 1, ""))
        chunks.append(("model's reasoning, re-sent", own_reason, k + 1, ""))
        if total_chars == 0:
            if rest:
                chunks.append(("unattributed growth (reasoning re-sent, framing)", rest, k + 1, ""))
            continue
        for lab, c, p in outs:
            inp = (p.get("state") or {}).get("input") or {}
            detail = inp.get("filePath") or inp.get("command") or inp.get("pattern") or inp.get("name") or inp.get("description") or ""
            chunks.append((lab, rest * c / total_chars, k + 1, str(detail)[:120]))
        if s.get("user_after"):
            chunks.append(("later user message", rest * s["user_after"] / total_chars, k + 1, ""))

    by = defaultdict(lambda: {"added": 0.0, "paid": 0.0, "calls": 0, "usd": 0.0})
    for lab, tok, j, _ in chunks:
        by[lab]["added"] += tok
        by[lab]["paid"] += tok * (n - j)
        by[lab]["calls"] += 1
        # Tool output and the baseline are fresh input once, then cache
        # reads. The model's own output and reasoning come back as cache
        # reads from the first re-send (observed: a session's uncached input
        # matches its baseline plus tool output, not its whole growth);
        # producing them is billed as output, below.
        if lab.startswith("model's"):
            by[lab]["usd"] += tok * PRICE["cache_read"] * (n - j)
        else:
            by[lab]["usd"] += tok * (PRICE["input"] + PRICE["cache_read"] * max(0, n - j - 1))

    # Re-reads: the same file read more than once in this session.
    reads = defaultdict(list)
    for lab, tok, j, d in chunks:
        if lab in ("read", "bash: file read") and d:
            key = re.sub(r"\s+", " ", d)
            reads[key].append((tok, n - j))
    reread_paid = sum(t * r for v in reads.values() if len(v) > 1 for t, r in v[1:])

    total_prompt = sum(s["prompt"] for s in steps)
    return {
        "steps": n,
        "input_total": total_prompt,
        "cache_read": sum(s["cache_read"] for s in steps),
        "output": sum(s["output"] for s in steps),
        "reasoning": sum(s["reasoning"] for s in steps),
        "first_prompt": steps[0]["prompt"],
        "last_prompt": steps[-1]["prompt"],
        "by": dict(by),
        "reread_paid": reread_paid,
        "files": {k: sum(t for t, _ in v) / len(v) for k, v in reads.items()},
    }


def tree(db, root):
    out = [root]
    i = 0
    while i < len(out):
        out += [r[0] for r in db.execute("select id from session where parent_id = ? order by time_created", (out[i],))]
        i += 1
    return out


def fmt(x):
    x = float(x)
    return f"{x/1e6:.2f}M" if abs(x) >= 1e6 else f"{x/1e3:.1f}k" if abs(x) >= 1e3 else f"{x:.0f}"


def report(db, root, as_json=False):
    ids = tree(db, root)
    res = {}
    for sid in ids:
        title, model, parent, cost = db.execute("select title, model, parent_id, cost from session where id = ?", (sid,)).fetchone()
        a = analyse(db, sid)
        if a:
            a["title"] = title
            a["model"] = json.loads(model).get("id") if model else None
            a["parent"] = parent
            a["cost"] = cost
            res[sid] = a
    if as_json:
        print(json.dumps(res, indent=1, default=str))
        return
    grand_in = sum(a["input_total"] for a in res.values())
    grand_out = sum(a["output"] for a in res.values())
    grand_reason = sum(a["reasoning"] for a in res.values())
    for sid, a in res.items():
        print(f"\n## {a['title']}  [{sid}] {a['model']}")
        print(f"steps {a['steps']} | input {fmt(a['input_total'])} (cache read {fmt(a['cache_read'])}) | "
              f"output {fmt(a['output'])} (reasoning {fmt(a['reasoning'])}) | "
              f"first prompt {fmt(a['first_prompt'])}, last {fmt(a['last_prompt'])}")
        out_usd = (a["output"] + a["reasoning"]) * PRICE["output"]
        in_usd = sum(v["usd"] for v in a["by"].values())
        a["usd_est"] = in_usd + out_usd
        print(f"est. cost ${a['usd_est']:.4f} (recorded ${a['cost']:.4f}): input side ${in_usd:.4f}, "
              f"output ${a['output']*PRICE['output']:.4f}, reasoning ${a['reasoning']*PRICE['output']:.4f}")
        print(f"{'origin':58} {'calls':>5} {'added':>8} {'paid':>8} {'share':>6} {'usd':>8}")
        for lab, v in sorted(a["by"].items(), key=lambda kv: -kv[1]["paid"]):
            print(f"{lab:58} {v['calls']:>5} {fmt(v['added']):>8} {fmt(v['paid']):>8} {100*v['paid']/a['input_total']:>5.1f}% {v['usd']:>8.4f}")
        print(f"re-reads of an already-read file, paid: {fmt(a['reread_paid'])} "
              f"({100*a['reread_paid']/a['input_total']:.1f}%)")
    # Repetition across the tree: baselines, and files read in more than one session.
    print(f"\n## tree: {len(res)} sessions | input {fmt(grand_in)} | output {fmt(grand_out)} (reasoning {fmt(grand_reason)})")
    base = sum(a["first_prompt"] for s, a in res.items() if s != root)
    base_paid = sum(a["by"].get("baseline: system prompt, tools, AGENTS.md, first message", {}).get("paid", 0) for s, a in res.items() if s != root)
    print(f"sub-agent baselines: {fmt(base)} added, {fmt(base_paid)} paid ({100*base_paid/grand_in:.1f}% of tree input)")
    seen = defaultdict(list)
    for sid, a in res.items():
        for f, t in a["files"].items():
            seen[f].append((sid, t))
    shared = {f: v for f, v in seen.items() if len(v) > 1}
    shared_added = sum(t for v in shared.values() for _, t in v[1:])
    print(f"files read in more than one session: {len(shared)}, {fmt(shared_added)} tokens added again beyond the first")
    for f, v in sorted(shared.items(), key=lambda kv: -sum(t for _, t in kv[1]))[:10]:
        print(f"  {len(v)}x {fmt(v[0][1]):>7}  {f}")


def main():
    if len(sys.argv) < 3:
        print(__doc__)
        sys.exit(2)
    db = open_db(sys.argv[1])
    if sys.argv[2] == "--list":
        for r in db.execute(
            "select id, datetime(time_created/1000,'unixepoch'), json_extract(model,'$.id'), "
            "tokens_input + tokens_cache_read, tokens_output, round(cost,4), substr(title,1,60), directory "
            "from session where parent_id is null order by time_created desc limit 200"
        ):
            print("\t".join(str(x) for x in r))
        return
    if "--price" in sys.argv:
        i, o, c = (float(x) * 1e-6 for x in sys.argv[sys.argv.index("--price") + 1].split(","))
        PRICE.update(input=i, output=o, cache_read=c)
    report(db, sys.argv[2], "--json" in sys.argv)


if __name__ == "__main__":
    main()
