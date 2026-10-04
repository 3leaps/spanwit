# Sanitized diskutil plist fixtures

These fixtures model the public, machine-readable command shapes used by the
collector:

- `/usr/sbin/diskutil info -plist <mount>`
- `/usr/sbin/diskutil apfs list -plist`
- `/usr/sbin/diskutil apfs listSnapshots -plist <volume>`

They are sanitized synthetic fixtures based on macOS 26.x output structure.
Device identifiers, UUIDs, names, sizes, and mount points are non-host values.
No user name, host name, or local path is retained.
