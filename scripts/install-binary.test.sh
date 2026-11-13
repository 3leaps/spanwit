#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
installer="$root/scripts/install-binary.sh"
scratch="$(mktemp -d "${TMPDIR:-/tmp}/spanwit-install-test.XXXXXX")"
old_pid=""
native_pid=""

cleanup() {
    for pid in "$old_pid" "$native_pid"; do
        [[ -n "$pid" ]] || continue
        kill "$pid" 2> /dev/null || true
        wait "$pid" 2> /dev/null || true
    done
    rm -rf "$scratch"
}
trap cleanup EXIT

fail() {
    echo "error: $*" >&2
    exit 1
}

expect_fail() {
    if "$@" > /dev/null 2>&1; then
        fail "expected failure: $*"
    fi
}

inode() {
    case $(uname -s) in
        Darwin) stat -f '%i' "$1" ;;
        Linux) stat -c '%i' "$1" ;;
        *) fail "unsupported fixture host" ;;
    esac
}

no_staging_debris() {
    ! find "$destination_dir" -maxdepth 1 -name 'spanwit.tmp.*' -print -quit | grep -q .
}

source="$scratch/source binary"
destination_dir="$scratch/bin dir"
destination="$destination_dir/spanwit"
mkdir -p "$destination_dir"
printf '#!/bin/sh\nprintf "new\\n"\n' > "$source"
chmod 600 "$source"
printf '#!/bin/sh\nprintf "old\\n"\nsleep 2\n' > "$destination"
chmod 755 "$destination"
old_inode=$(inode "$destination")

"$destination" > "$scratch/running-old" &
old_pid=$!
for _ in {1..50}; do
    [[ -s "$scratch/running-old" ]] && break
    sleep 0.02
done
test -s "$scratch/running-old" || fail "old executable did not start"
"$installer" "$source" "$destination" > /dev/null
kill -0 "$old_pid" || fail "old executable stopped during replacement"
test "$(cat "$scratch/running-old")" = old || fail "old executable did not run"
test "$("$destination")" = new || fail "replacement executable did not run"
test -x "$destination" || fail "replacement is not executable"
test "$(inode "$destination")" != "$old_inode" || fail "replacement did not use a fresh inode"
wait "$old_pid"
old_pid=""

# Shell scripts exercise rename behavior, but macOS also needs a mapped native
# executable to survive replacement. sleep is deliberately bounded; true is a
# trusted, small replacement program present on both supported Unix platforms.
native_old="$scratch/native-old"
native_new="$scratch/native-new"
native_destination="$destination_dir/spanwit-native"
native_true=/usr/bin/true
[[ -x "$native_true" ]] || native_true=/bin/true
[[ -x /bin/sleep && -x "$native_true" ]] || fail "native test tools are unavailable"
cp /bin/sleep "$native_old"
cp "$native_true" "$native_new"
# A copied Apple platform binary needs an ad-hoc signature before this host
# permits local execution. The installer itself never signs deployment input.
if [[ "$(uname -s)" == Darwin ]]; then
    codesign --force --sign - "$native_old" "$native_new" > /dev/null 2>&1
fi
cp "$native_old" "$native_destination"
native_inode=$(inode "$native_destination")
"$native_destination" 2 &
native_pid=$!
sleep 0.1
kill -0 "$native_pid" || fail "native executable did not start"
"$installer" "$native_new" "$native_destination" > /dev/null
kill -0 "$native_pid" || fail "running native executable stopped during replacement"
test "$(inode "$native_destination")" != "$native_inode" || fail "native replacement did not use a fresh inode"
"$native_destination" || fail "replacement native executable did not run"
wait "$native_pid"
native_pid=""

printf 'keep\n' > "$destination"
expect_fail "$installer" "$scratch/missing" "$destination"
test "$(cat "$destination")" = keep || fail "missing source changed destination"

mock_bin="$scratch/mock-bin"
mkdir "$mock_bin"
printf '#!/bin/sh\nexit 7\n' > "$mock_bin/cp"
chmod 755 "$mock_bin/cp"
expect_fail env PATH="$mock_bin:/bin:/usr/bin" "$installer" "$source" "$destination"
test "$(cat "$destination")" = keep || fail "staging failure changed destination"

victim="$scratch/victim"
printf 'victim\n' > "$victim"
rm -f "$destination"
ln -s "$victim" "$destination"
expect_fail "$installer" "$source" "$destination"
test "$(cat "$victim")" = victim || fail "symlink target was modified"
test -L "$destination" || fail "file symlink was modified"
no_staging_debris || fail "file symlink refusal left staging debris"

directory_target="$scratch/directory-target"
mkdir "$directory_target"
rm -f "$destination"
ln -s "$directory_target" "$destination"
expect_fail "$installer" "$source" "$destination"
test -L "$destination" || fail "directory symlink was modified"
no_staging_debris || fail "directory symlink refusal left staging debris"

rm -f "$destination"
ln -s "$scratch/missing-target" "$destination"
expect_fail "$installer" "$source" "$destination"
test -L "$destination" || fail "dangling symlink was modified"
no_staging_debris || fail "dangling symlink refusal left staging debris"

directory_destination="$scratch/directory"
mkdir "$directory_destination"
expect_fail "$installer" "$source" "$directory_destination"
test -d "$directory_destination" || fail "directory destination changed"

echo '[ok] staged binary install tests passed'
