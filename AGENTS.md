<!-- >>> greenline >>> -->
## Greenline gate — how merges work here

This repo is gated by **greenline**. Read `docs/greenline.md` and `docs/DOCTRINE.md`
before writing code or tests.

**Invariants (never violate):**
- `main` == what prod runs == green, always.
- The canonical checkout is pristine — never edit it by hand.
- All work happens in worktrees branched from last-green.
- Every merge goes through the serialized gate: full `check` + real `deploy`.

**Your workflow:**
1. `greenline worktree <name>` — get a worktree at `/Volumes/Gumby/worktrees/greenline/auto/<name>` on branch `gl/<name>`.
2. Do your work there. Co-design tests + code per docs/DOCTRINE.md (parallel-safe, namespaced, no global-state assertions, OS-assigned ports; never mock other services — make real calls fast with a content-addressed record/replay cache).
3. Commit in your worktree. Then `greenline submit` (from that worktree).
4. The gate squash-merges, runs `./run check`, fast-forwards `main`, runs `./run deploy`, and publishes. It rolls back prod automatically if deploy fails.
5. On success: `greenline done` to remove your worktree + branch.

**Never** commit or push on `main` — hooks hard-lock it (reference-transaction
cannot be bypassed with `--no-verify`; pre-commit/pre-push refuse too). Never edit the
canonical checkout. If the gate reports a conflict, rebase your worktree on
`main` and resubmit. If commits somehow reached `main` outside the
gate (legacy workflow, hotfix), run `greenline adopt` to gate them in place —
greenline never discards commits on `main`.

Diagnose with `greenline status` and `greenline doctor` (`--fix` to reconcile).
<!-- <<< greenline <<< -->

## Watcher CPU regression guard

- Idle `WatchTick` must write the registry only once, for its heartbeat. Check
  request presence through the read cache before entering mutation transactions,
  and return `false` from `withState` when nothing changed. `mutateProcess`
  always saves even when its callback does nothing.
- Strict-overlap cleanup once rewrote the whole state and backup for every
  service each second, invalidating the read cache and causing quadratic JSON
  work. The real 150-entry benchmark fell from 334ms/65.8MB to 6.6ms/0.74MB per
  tick after skipping absent requests and saving only actual expiry changes.
  Run `./run test ./pkg/manager -run '^$' -bench BenchmarkWatchTickIdleRegistry`
  for a disk-backed scale check; tests also guard write count and allocations.
- Symbolize stripped Go frames in a macOS `sample` report with
  `go tool addr2line output/Auto.app/Contents/MacOS/auto`: replace the ASLR load
  address with the Mach-O `__TEXT` vmaddr from `otool -l`, retaining each frame's
  offset. Use the exact sampled binary before deploying its replacement.

## Shutdown safety

- Read `docs/shutdown.md` before changing shutdown or spawn paths. Never write
  `kern.willshutdown` or simulate global shutdown notifications in a test.
- Every actual launch must pass the kernel shutdown gate, including retries,
  overlap candidates, isolate bootstrap/kickstart, and auto-shim exec. Keep spawn
  ownership recording inside the start barrier so teardown cannot miss a child.
- Inhibit starts before teardown, but preserve operator explicit-stop flags.
  Unload isolated KeepAlive jobs instead of just signalling their process groups.
- Kernel-confirmed shutdown parks the watcher until launchd terminates it;
  SIGTERM alone also occurs during ordinary bootout and is not machine proof.
