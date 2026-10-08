#!/usr/bin/env bash
# Every workflow action is pinned by full commit SHA with a version comment, no
# action pin is on the Node 20 deny-list, and hosted runners use an explicit
# image instead of a moving label.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd -P)"
shopt -s nullglob
workflows=("$root"/.github/workflows/*.yml "$root"/.github/workflows/*.yaml)
[[ ${#workflows[@]} -gt 0 ]] || {
    echo 'error: no workflow files found' >&2
    exit 1
}
python3 - "${workflows[@]}" << 'PY'
import re
import sys

# Pins known to target Node 20 (GitHub forces them onto Node 24 with a
# deprecation warning). Extend when a pinned action is found to run on node20.
NODE20 = {
    'actions/checkout@11d5960a326750d5838078e36cf38b85af677262',
    'actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02',
}
errors = []
for path in sys.argv[1:]:
    name = path.rsplit('/', 1)[-1]
    for number, line in enumerate(open(path, encoding='utf-8'), 1):
        uses = re.match(r'^\s*(?:-\s+)?uses:\s*(\S+)(.*)$', line)
        if uses:
            ref, rest = uses.groups()
            if ref.startswith(('./', 'docker://')):
                continue
            if not re.fullmatch(r'[\w.-]+/[\w./-]+@[0-9a-f]{40}', ref):
                errors.append(f'{name}:{number}: action not pinned by full SHA: {ref}')
            elif not re.search(r'#\s*v\d', rest):
                errors.append(f'{name}:{number}: pinned action lacks a version comment: {ref}')
            if ref in NODE20:
                errors.append(f'{name}:{number}: action pin targets Node 20: {ref}')
        if re.search(r'\b[\w-]+-latest\b', line) and not line.lstrip().startswith('#'):
            errors.append(f'{name}:{number}: moving runner label: {line.strip()}')
        runs_on = re.match(r'^\s*runs-on:\s*(.+?)\s*$', line)
        if runs_on and '${{' in runs_on.group(1):
            errors.append(f'{name}:{number}: expression-valued runs-on cannot be checked: {runs_on.group(1)}')
if errors:
    raise SystemExit('error: ' + '\n'.join(errors))
PY
echo '[ok] workflow actions are SHA-pinned, off Node 20, on explicit runners'
