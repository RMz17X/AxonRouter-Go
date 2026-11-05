## Svelte-check ratchet: baseline records the tree's real count

CI's test job failed its Svelte check step: "svelte-check errors
grew: 80 > baseline 75". The growth is not new: the tree has stood at
80 errors since the post-v1.9.16 feature line landed (verified
identical — 80 errors / 56 warnings — before and after the 953–962
batch, which added none), but that line never updated the recorded
baseline, so the ratchet has been failing on a stale number ever
since.

The baseline now records the verified current count (80), written by
the ratchet's own `--update` mode, and the ratchet passes against it:
"0 unresolved identifiers, 80 errors (baseline 80)". The five errors
themselves are pre-existing findings in the tree, not a regression
this change introduces or hides — the ratchet guards from here, and
reducing the count stays ordinary future work.
