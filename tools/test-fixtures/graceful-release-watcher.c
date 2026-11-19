// A real launchd child. Never spawn descendants or ignore cleanup SIGTERM.
#include <signal.h>
#include <stdio.h>
#include <unistd.h>

#ifndef IGNORE_INT
#define IGNORE_INT 0
#endif

int main(int argc, char **argv) {
    char path[4096];
    sigset_t signals;
    sigemptyset(&signals);
    sigaddset(&signals, SIGINT);
    sigaddset(&signals, SIGTERM);
    if (argc != 3 || sigprocmask(SIG_BLOCK, &signals, NULL) != 0)
        return 1;
    int len = snprintf(path, sizeof(path), "%s/%ld.events", argv[2], (long)getpid());
    if (len < 0 || (size_t)len >= sizeof(path))
        return 1;
    FILE *log = fopen(path, "w");
    if (!log || fputs("ready\n", log) == EOF || fflush(log) != 0)
        return 1;

    for (;;) {
        int sig;
        if (sigwait(&signals, &sig) != 0)
            return 1;
        if (fputs(sig == SIGINT ? "SIGINT\n" : "SIGTERM\n", log) == EOF || fflush(log) != 0)
            return 1;
        if (sig == SIGTERM || !IGNORE_INT)
            return fclose(log) != 0;
    }
}
