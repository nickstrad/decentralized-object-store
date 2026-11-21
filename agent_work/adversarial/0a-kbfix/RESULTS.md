# 0a-kbfix adversarial results

No adversarial pass was run for this change: it adds one apt package (the SQLite header)
to the dev image and one image-build call to `kb-sync.sh`. It touches no gate logic and no
product code. The Chunk 0a harness (`agent_work/adversarial/0a-foundation/`) is unaffected.
Checked instead: `kb-sync.sh index` and `search` work; gate G0–G6 green on the new image.
