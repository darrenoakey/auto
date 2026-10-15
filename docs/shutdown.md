# Machine shutdown handling

Auto reads `kern.willshutdown` through the native sysctl API. A nonzero value
means XNU has committed to machine shutdown or restart. Auto never writes this
sysctl. **Do not write it to test this feature**: setting it to one is irreversible
for the current boot and changes kernel power-management behavior.

## Behavior

- The watcher checks before its first supervision pass, every subsequent pass,
  and each service. Direct launches check again immediately before fork, including
  retries and overlap replacements. CLI and isolated-launch paths use the same
  kernel gate; the launchd-owned isolated shim checks again before exec.
- On confirmed machine shutdown, the watcher inhibits all launches, drains
  ownership recording for in-flight children, sends SIGTERM to owned process
  groups in parallel, and escalates surviving group members after five seconds.
  A child that ignores SIGTERM is still killed if its group leader exits first.
- Isolated LaunchAgent jobs are unloaded, including registered jobs between
  launches. Signalling their PIDs alone would allow launchd KeepAlive to restart
  them. Their plists remain for the next login. Shutdown-only launchctl clients
  have an eight-second deadline, concurrent with direct cleanup, so a stuck
  isolate bootout cannot consume the watcher's own launchd termination budget.
  Launchd owns the unloaded job's final TERM/KILL sequence.
- The watcher parks after cleanup until launchd terminates it. It does not exit
  into KeepAlive while launchd could still relaunch it. A new watcher or CLI in
  the same boot sees the same kernel flag and refuses launches.
- SIGTERM also inhibits launches immediately, even during a busy watch tick,
  before the watch loop handles teardown. SIGTERM is not labelled machine
  shutdown: ordinary `launchctl bootout` sends it too. SIGINT continues to leave
  managed services running while stopping supervision.
- A failed kernel read pauses supervision and refuses new launches. It does not
  falsely declare shutdown or destructively stop running services.
- Shutdown does not change `explicitly_stopped` preferences. The kernel flag
  resets with a new boot, so eligible services resume at the next GUI login.
  Sleep, logout and cancellable shutdown dialogs are not machine-shutdown flags.

## Why this detector

Sources were fetched and read on 2026-10-15:

1. [XNU IOPMrootDomain.cpp](https://github.com/apple-oss-distributions/xnu/blob/main/iokit/Kernel/IOPMrootDomain.cpp)
   implements `sysctl_willshutdown`, backed by `gWillShutdown`, and the one-way
   zero-to-one compare-and-swap in `IOPMRootDomainWillShutdown`.
2. Read-only inspection of this Mac's launchd, macOS 27.2 build 26B5091g, showed
   its "committing to system shutdown" routine writing `kern.willshutdown=1`
   before its COMMITTED/BEGIN_SHUTDOWN state transitions and later service
   bootout states. This is current implementation evidence, not a public API
   ordering guarantee. The installed binary SHA-256 was
   `d891c766c468ac84628ca737470b48a0ba41874cebf5b137b16d54ffa9869878`.
3. [Apple's lifecycle guide](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/Lifecycle.html)
   describes SIGTERM/SIGKILL daemon teardown and earlier loginwindow application
   termination. SIGTERM alone cannot distinguish shutdown from unloading a job.
4. [IOPMLib.h](https://github.com/apple-oss-distributions/IOKitUser/blob/main/pwr_mgt.subproj/IOPMLib.h)
   explicitly states that `IORegisterForSystemPower` does not provide shutdown or
   restart notifications. [Stack Overflow 1321219](https://stackoverflow.com/questions/1321219)
   documents the same pitfall.
5. [NSWorkspace documentation](https://developer.apple.com/documentation/appkit/nsworkspace/willpoweroffnotification)
   includes logout. [Stack Overflow 22079025](https://stackoverflow.com/questions/22079025)
   and [46085718](https://stackoverflow.com/questions/46085718) also show the
   ambiguity and background-delivery problems.
6. [PowerManagement's loginwindow callback](https://github.com/apple-oss-distributions/PowerManagement/blob/main/pmconfigd/pmconfigd.m)
   says shutdown/restart initiation can mean only a confirmation dialog;
   `logoutNoReturn` includes logout and arrives after applications have quit.
   These are not safe unconditional machine-shutdown triggers.
7. [Libnotify notify.h](https://github.com/apple-oss-distributions/Libnotify/blob/main/notify.h)
   says the first `notify_check` always reports true. Registration or an initial
   true check is not proof that an actual shutdown event occurred.

Google/DuckDuckGo and Reddit requests were attempted, but returned challenges,
403s or unrelated results. No unread Reddit post is cited as evidence.

## Verification and limits

Regression tests use real kernel reads, real processes and process groups,
real SIGTERM delivery, and a uniquely named temporary launchd KeepAlive job.
They cover inhibition, draining an in-flight spawn, preserving restart
preferences, next-supervisor recovery, child escalation and isolated-job unload.
The host is never rebooted, and kernel shutdown state is never modified.

This is not a guarantee against power loss, panic, forced reboot, or services
that deliberately escape their owned process groups. The kernel check and fork
cannot be atomic with an unrelated OS shutdown request: a child already crossing
that boundary is accounted for and drained. No observed source establishes that
this flag precedes every loginwindow/AppKit application death. Userspace-only
reboots have a different kernel flag. For strict ordering on a shutdown you
initiate yourself, run `auto -q shutdown` before the OS shutdown command.
A real VM or spare-machine shutdown trace is still required to verify complete
shutdown timing for each macOS release.
