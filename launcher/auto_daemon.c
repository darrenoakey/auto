// ##################################################################
// auto-daemon
// stable, code-signed launcher for the auto watch daemon.
//
// launchd runs this binary instead of running python directly. It spawns the
// python daemon as a child, forwards termination signals to it, and exits with
// the child's status. macOS TCC attributes every process in the tree
// (the python daemon and each service it starts) to this binary's code
// identity. Because the binary is signed with a persistent certificate and a
// fixed identifier, that identity survives upgrades of python and of auto
// itself, so Full Disk Access / Accessibility grants are never lost.
//
// usage: auto-daemon <command> [args...]
#include <errno.h>
#include <signal.h>
#include <spawn.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <unistd.h>

extern char **environ;

static volatile pid_t child_pid = 0;

// ##################################################################
// forward signal
// relays a termination or handover signal to the child process
static void forward_signal(int sig) {
    if (child_pid > 0) {
        kill(child_pid, sig);
    }
}

// ##################################################################
// main
// spawns the child, relays signals, and mirrors its exit status
int main(int argc, char **argv) {
    if (argc < 2) {
        fprintf(stderr, "usage: auto-daemon <command> [args...]\n");
        return 64;
    }

    const int forwarded[] = {SIGTERM, SIGINT, SIGHUP, SIGUSR1, SIGUSR2};
    const size_t forwarded_count = sizeof(forwarded) / sizeof(forwarded[0]);

    // block the forwarded signals until child_pid is known so a signal that
    // arrives during spawn is delivered to the child rather than dropped
    sigset_t block_set;
    sigset_t previous_set;
    sigemptyset(&block_set);
    for (size_t i = 0; i < forwarded_count; i++) {
        sigaddset(&block_set, forwarded[i]);
    }
    sigprocmask(SIG_BLOCK, &block_set, &previous_set);

    struct sigaction action;
    memset(&action, 0, sizeof(action));
    action.sa_handler = forward_signal;
    sigemptyset(&action.sa_mask);
    action.sa_flags = SA_RESTART;
    for (size_t i = 0; i < forwarded_count; i++) {
        sigaction(forwarded[i], &action, NULL);
    }

    // the child must not inherit the blocked mask
    posix_spawnattr_t attributes;
    posix_spawnattr_init(&attributes);
    posix_spawnattr_setsigmask(&attributes, &previous_set);
    posix_spawnattr_setflags(&attributes, POSIX_SPAWN_SETSIGMASK);

    pid_t pid = 0;
    int spawn_result = posix_spawnp(&pid, argv[1], NULL, &attributes, &argv[1], environ);
    posix_spawnattr_destroy(&attributes);
    if (spawn_result != 0) {
        fprintf(stderr, "auto-daemon: cannot start %s: %s\n", argv[1], strerror(spawn_result));
        return 127;
    }
    child_pid = pid;
    sigprocmask(SIG_SETMASK, &previous_set, NULL);

    int status = 0;
    while (waitpid(pid, &status, 0) < 0) {
        if (errno != EINTR) {
            fprintf(stderr, "auto-daemon: waitpid failed: %s\n", strerror(errno));
            return 70;
        }
    }

    if (WIFEXITED(status)) {
        return WEXITSTATUS(status);
    }
    if (WIFSIGNALED(status)) {
        return 128 + WTERMSIG(status);
    }
    return 70;
}
