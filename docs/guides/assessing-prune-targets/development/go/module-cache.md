# Go: Module Cache and Build Artifacts

If you work with Go, you may have noticed that your machine can accumulate a surprising amount of space in certain folders related to dependencies and builds. This guide is written for people who are looking at these directories and feeling unsure whether it’s safe to clean them up.

## Common Large Directories in Go Projects

Two areas tend to take up the most space for Go developers:

- The **Go module cache** (usually located at `$GOPATH/pkg/mod` or `$(go env GOMODCACHE)`)
- Build output directories inside individual projects (often called `bin/`, or sometimes left in the project root depending on how the project is set up)

The module cache in particular can grow quite large over time because Go downloads and stores source code + compiled artifacts for every module version you’ve ever depended on (directly or indirectly).

## Why People Consider Cleaning These

The module cache is **not** your source code. It is a local copy of modules downloaded from the internet (primarily proxy.golang.org). Go will re-download anything it needs the next time you build or run `go mod tidy` / `go get`.

Build output directories are generated when you compile your programs. In most cases they can be recreated by running `go build` or whatever build command the project uses.

Because of this, many Go developers periodically clean these areas when they’re trying to free up disk space.

## Things That Can Make People Nervous

It’s reasonable to feel cautious. Here are some common concerns:

- **“Will this break my builds or slow me down a lot?”**  
  The main downside is that the next time you build something that needs those modules, Go will have to download them again. On a fast connection this is usually quick, but it can feel slow if you have a large dependency graph or limited bandwidth.

- **“What if I’m working offline?”**  
  If you don’t have internet access, Go won’t be able to re-download modules you’ve deleted from the cache. This is one of the more common reasons people choose to leave the cache alone.

- **“I have replace directives or local forks.”**  
  These usually live in your project’s `go.mod`, not in the module cache itself, so they are typically safe. Still, it’s worth being aware of any unusual setup in your projects.

- **“My company has strict policies about what can be deleted.”**  
  Some environments prefer that developers not clean dependency caches without approval.

## Risk Profile

| Situation                                      | Typical Risk Level | Notes                                                |
| ---------------------------------------------- | ------------------ | ---------------------------------------------------- |
| Personal projects with good internet           | Usually low        | Re-downloading is fast and low-friction              |
| Work projects with slow or restricted networks | Medium–Higher      | Re-download time or policy concerns can matter       |
| Air-gapped or offline-heavy workflows          | Higher             | You may genuinely need the cached modules            |
| Very large dependency graphs                   | Medium             | First rebuild after cleanup can take noticeable time |

## Ways to Be More Cautious

If you’d like to reduce risk while still reclaiming space, some developers use approaches like:

- Only remove very old entries from the module cache (some people write small scripts for this).
- Clean build output directories inside projects more aggressively than the global module cache.
- Test the impact by cleaning one project’s build output first before doing anything broader.

## References for Further Reading

- Official Go documentation: [Module cache](https://go.dev/ref/mod#module-cache)
- `go clean` command documentation
- Various discussions on the Go blog and Reddit (r/golang) about managing module cache size

---

**Related reading**

- [Rust: `target/` directories](../rust/target-directories.md)
- [General build artifacts](../build-artifacts.md)
