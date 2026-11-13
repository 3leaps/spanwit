#!/bin/sh
# Stage a locally built executable next to its destination, then replace the
# destination with one rename. The caller owns creating the destination
# directory and choosing its policy; this script never starts a service.
set -eu

case $(uname -s) in
    Darwin | Linux) ;;
    *)
        printf '%s\n' "staged installation supports macOS/Linux only" >&2
        exit 1
        ;;
esac

die() {
    printf '%s\n' "error: $*" >&2
    exit 1
}

if [ "$#" -ne 2 ]; then
    die "usage: $0 SOURCE DESTINATION"
fi

source=$1
destination=$2

[ -f "$source" ] && [ ! -L "$source" ] || die "source is not a regular file: $source"

destination_dir=$(dirname "$destination")
[ -d "$destination_dir" ] && [ ! -L "$destination_dir" ] || die "destination directory is not a directory: $destination_dir"
# The caller supplies a user-controlled destination directory. This helper
# does not claim adversarial same-user rename or TOCTOU containment.

# Installing through a destination symlink makes platform mv behavior harder
# to reason about, especially when it names a directory. Refuse it outright.
if [ -L "$destination" ]; then
    die "destination is a symlink: $destination"
fi
if [ -d "$destination" ]; then
    die "destination is a directory: $destination"
fi

umask 077
staged=$(mktemp "${destination}.tmp.XXXXXX") || die "could not create staging file beside destination"
cleanup() {
    rm -f "$staged"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

cp "$source" "$staged"
chmod 755 "$staged"

# The staging file and destination share a parent, so mv is a same-filesystem
# rename. Destination symlinks were refused before staging.
mv -f "$staged" "$destination"
trap - EXIT HUP INT TERM

printf 'installed: %s\n' "$destination"
