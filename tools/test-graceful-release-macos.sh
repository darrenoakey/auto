#!/bin/bash
# Real integration test; requires an unprivileged macOS GUI launchd session.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
release_driver="$here/graceful-release"
# shellcheck source=tools/graceful-release
source "$release_driver"

[[ $(uname -s) == Darwin && $EUID -ne 0 ]] || fail 'test requires unprivileged macOS'
domain="gui/$EUID"
/bin/launchctl print "$domain" >/dev/null

wait_event() {
    local pid=$1 event=$2 i
    for ((i=0; i<50; i++)); do
        if grep -qsx "$event" "$events/$pid.events"; then return 0; fi
        sleep 0.1
    done
    fail "fixture PID $pid did not record $event"
}

assert_one_int() {
    [[ $(<"$events/$1.events") == $'ready\nSIGINT' ]] ||
        fail "fixture PID $1 did not record exactly one SIGINT and no other signal"
}

cleanup() {
    local rc=$?
    trap - EXIT
    if [[ -n $owned_target ]]; then
        # Only the UUID label we bootstrapped. The fixture handles bootout's TERM.
        /bin/launchctl bootout "$owned_target" || rc=1
        if /bin/launchctl print "$owned_target" >/dev/null 2>&1; then
            fail "cleanup left $owned_target registered; fixture preserved at $scratch" || exit 1
        fi
        if (( rc == 0 )); then wait_event "$restored_pid" SIGTERM || rc=1; fi
    fi
    if (( rc != 0 )); then
        echo "Fixture diagnostics for $target:" >&2
        local log
        for log in "$events"/*.events; do
            if [[ -f $log ]]; then printf '%s: %s\n' "$log" "$(<"$log")" >&2; fi
        done
    fi
    rm -rf -- "$scratch"
    exit "$rc"
}

run_case() (
    local scenario=$1 target owned_target='' label scratch events state bundle exe expected
    local prior candidate prior_pid token result candidate_pid candidate_token restored_pid started
    local ignore=0
    [[ $scenario != timeout ]] || ignore=1
    label="org.auto.graceful-release-test.$(/usr/bin/uuidgen)"
    target="$domain/$label"
    scratch=$(/usr/bin/mktemp -d /private/tmp/auto-graceful-release-test.XXXXXXXX)
    events="$scratch/events"
    trap cleanup EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    cd "$scratch"
    state="$scratch/state"
    bundle="$scratch/Auto.app"
    exe="$bundle/Contents/MacOS/auto"
    expected="$exe watch $events"
    mkdir -p "$events" "$state" "$(dirname "$exe")" "$state/candidate.app/Contents/MacOS"
    /usr/bin/xcrun clang -Wall -Wextra -Werror -DIGNORE_INT="$ignore" \
        "$here/test-fixtures/graceful-release-watcher.c" -o "$exe"
    /usr/bin/xcrun clang -Wall -Wextra -Werror \
        "$here/test-fixtures/graceful-release-watcher.c" -o "$state/candidate.app/Contents/MacOS/auto"
    prior=$(file_inode "$exe")
    candidate=$(file_inode "$state/candidate.app/Contents/MacOS/auto")
    [[ $prior != "$candidate" ]] || fail 'fixture images must have distinct inodes'
    printf '%s\n' "$prior" > "$state/prior-inode"
    printf '%s\n' "$candidate" > "$state/candidate-inode"

    # restore_bundle invokes ./run health. This fixture health probe observes
    # the real disposable job and verifies the identity of its running image.
    cat > run <<'HEALTH'
#!/bin/bash
set -euo pipefail
[[ $# == 1 && $1 == health ]]
source "$release_driver"
pid=$(watcher_pid "$target")
watcher_identity "$pid" "$expected" >/dev/null
watcher_has_image "$pid" "$(file_inode "$exe")"
HEALTH
    chmod +x run
    export release_driver target expected exe

    cat > "$label.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$label</string>
  <key>ProgramArguments</key><array><string>$exe</string><string>watch</string><string>$events</string></array>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>1</integer>
  <key>ExitTimeOut</key><integer>5</integer>
</dict></plist>
PLIST
    if /bin/launchctl print "$target" >/dev/null 2>&1; then fail "label already exists: $target"; fi
    /bin/launchctl bootstrap "$domain" "$scratch/$label.plist"
    owned_target=$target
    prior_pid=$(await_image "$target" "$expected" "$prior" 100)
    wait_event "$prior_pid" ready
    token=$(watcher_identity "$prior_pid" "$expected")
    mv "$bundle" "$state/prior.app"
    mv "$state/candidate.app" "$bundle"
    interrupt_watcher "$target" "$expected" "$prior_pid" "$token" "$state/deploy-int"
    wait_event "$prior_pid" SIGINT
    [[ $(<"$state/deploy-int") == "$token" ]] || fail 'wrong deploy identity recorded'

    if [[ $scenario == timeout ]]; then
        started=$SECONDS
        if result=$(await_image "$target" "$expected" "$candidate" 10 2>&1); then
            fail "unexpectedly loaded candidate image: $result"
        fi
        [[ $result == *'within the grace window'* && $((SECONDS-started)) -lt 10 ]] ||
            fail "did not report a bounded image timeout: $result"
        [[ $(watcher_pid "$target") == "$prior_pid" && $(watcher_identity "$prior_pid" "$expected") == "$token" ]] ||
            fail 'timeout did not retain the original watcher identity'
        if result=$(interrupt_watcher "$target" "$expected" "$prior_pid" "$token" "$state/deploy-int" 2>&1); then
            fail 'duplicate retirement request succeeded'
        fi
        [[ $result == *'not retrying'* ]] || fail "duplicate rejected for unexpected reason: $result"
        # Actual rollback restores the on-disk image without interrupting the
        # already-running original process a second time.
        restored_pid=$(restore_bundle)
        [[ $restored_pid == "$prior_pid" && $(watcher_identity "$restored_pid" "$expected") == "$token" &&
            ! -e "$state/rollback-int" ]] || fail 'rollback unnecessarily retired the original watcher'
        sleep 0.5 # allow an erroneous second SIGINT to reach the still-running child
        assert_one_int "$prior_pid"
        printf 'PASS: bounded timeout retained PID %s; duplicate/rollback sent no second signal (%s)\n' "$prior_pid" "$target"
    else
        candidate_pid=$(await_image "$target" "$expected" "$candidate" 100)
        [[ $candidate_pid != "$prior_pid" ]] || fail 'candidate did not replace the prior process'
        wait_event "$candidate_pid" ready
        candidate_token=$(watcher_identity "$candidate_pid" "$expected")
        assert_one_int "$prior_pid"
        # A running PID alone is insufficient: an incorrect candidate inode
        # must not send a signal or create rollback evidence.
        if result=$(retire_rejected_watcher "$target" "$expected" "$prior" "$prior" "$state/rollback-int" 2>&1); then
            fail 'rollback accepted the wrong candidate inode'
        fi
        [[ $result == *'no signal sent'* && ! -e "$state/rollback-int" &&
            $(<"$events/$candidate_pid.events") == ready ]] || fail 'wrong-inode rollback did not fail closed'
        restored_pid=$(restore_bundle)
        [[ $restored_pid != "$candidate_pid" && $restored_pid != "$prior_pid" &&
            $(<"$state/rollback-int") == "$candidate_token" ]] || fail 'rollback did not replace the verified candidate'
        wait_event "$restored_pid" ready
        assert_one_int "$candidate_pid"
        printf 'PASS: launchctl SIGINT replaced PID %s with candidate %s, then rollback restored image %s in PID %s (%s)\n' \
            "$prior_pid" "$candidate_pid" "$prior" "$restored_pid" "$target"
    fi
    [[ $(file_inode "$exe") == "$prior" && $(file_inode "$state/failed.app/Contents/MacOS/auto") == "$candidate" ]] ||
        fail 'rollback did not restore and preserve the expected images'
)

run_case timeout
run_case cooperative
