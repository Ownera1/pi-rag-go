#!/bin/sh
# A/B retrieval evaluation: baseline git ref vs the working tree, same
# workspace, same labels. Each side rebuilds the index (two full re-embeds).
#   scripts/ab-eval.sh WORKSPACE questions.jsonl [BASE_REF] [TOP_K]
set -eu
ws=$(cd "$1" && pwd)
dataset=$(cd "$(dirname "$2")" && pwd)/$(basename "$2")
base=${3:-HEAD}
k=${4:-5}
root=$(cd "$(dirname "$0")/.." && pwd)
out=$root/evaluation/runs/ab-$(date +%Y%m%d-%H%M%S)
tmp=$(mktemp -d)
trap 'git -C "$root" worktree remove --force "$tmp/base" 2>/dev/null; rm -rf "$tmp"' EXIT
mkdir -p "$out"
export CGO_CFLAGS="$("$root/scripts/cgo-flags.sh")"

git -C "$root" worktree add --detach "$tmp/base" "$base" >/dev/null
(cd "$tmp/base" && go build -tags sqlite_fts5 -o "$tmp/rag-base" ./cmd/rag)
(cd "$root" && go build -tags sqlite_fts5 -o "$tmp/rag-new" ./cmd/rag)

for side in base new; do
	echo "== $side: rebuild" >&2
	"$tmp/rag-$side" rebuild --workspace "$ws" >/dev/null
	"$tmp/rag-$side" eval --workspace "$ws" --dataset "$dataset" --top-k "$k" \
		--modes bm25,vector,hybrid --output "$out/$side.json" >/dev/null
done

python3 - "$out/base.json" "$out/new.json" <<'EOF'
import json, sys
a, b = (json.load(open(p)) for p in sys.argv[1:])
print(f"{'mode':8} {'recall base':>12} {'recall new':>11} {'MRR base':>9} {'MRR new':>8}")
for x, y in zip(a["summaries"], b["summaries"]):
    print(f"{x['mode']:8} {x['recallAtK']:12.3f} {y['recallAtK']:11.3f} {x['mrr']:9.3f} {y['mrr']:8.3f}")
ra = {m["mode"]: {r["id"]: r["reciprocalRank"] for r in m["results"]} for m in a["summaries"]}
for m in b["summaries"]:
    for r in m["results"]:
        old = ra[m["mode"]].get(r["id"], 0)
        if abs(r["reciprocalRank"] - old) > 1e-9:
            print(f"  {m['mode']:6} {r['id']}: RR {old:.2f} -> {r['reciprocalRank']:.2f}  {r['query']}")
EOF
echo "reports: $out" >&2
