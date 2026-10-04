# General Build Artifacts

Many projects create folders like `dist/`, `build/`, `.next/`, or similar that contain compiled or processed output. These directories can grow surprisingly large over time. This guide is meant for people who are looking at these folders and feeling unsure whether it’s safe to remove them.

## Why These Folders Get Big

When you build or compile a project, the tools take your source code and turn it into something runnable or deployable. That output usually ends up in one of these folders:

- `dist/` or `build/` (very common in web projects, Java projects, etc.)
- `.next/` (used by Next.js)
- `.turbo/` (used by Turborepo)
- `out/` (used by some static site generators and other tools)

These folders can easily reach hundreds of megabytes or several gigabytes, especially in larger projects or monorepos. They often contain thousands of files that are generated from your source code.

## Why Many People Consider Removing Them

In most cases, these folders only contain **derived** files — things that can be recreated by running the project’s build command again (e.g. `npm run build`, `./gradlew build`, `cargo build`, etc.).

Because the source code lives outside these folders, deleting the contents usually does not mean losing your actual work. The next time you (or your team/CI) run the build, the folder gets regenerated.

This is why many developers periodically clean these directories when they’re trying to free up space.

## Common Worries

It’s normal to feel cautious about deleting build output. Here are some of the concerns that come up frequently:

- **“My next build will take forever.”**  
  This is often the biggest practical downside. Some projects (especially large monorepos or those with native compilation) can take a long time to build from scratch.

- **“What if something important lives in there?”**  
  In most standard setups, nothing you wrote by hand lives in these folders. However, some unusual workflows or older projects occasionally put generated-but-important files inside `dist/` or `build/`.

- **“I’m on a slow or metered connection.”**  
  Rebuilding sometimes requires re-downloading dependencies or other assets, which can use bandwidth.

- **“These are the files I actually ship or deploy.”**  
  In some projects, the contents of `dist/` or `build/` are exactly what gets deployed. Deleting them before you’ve made a release or backup can be a problem.

## Risk Profile

| Situation                                                   | Typical Risk Level | Things to Consider                                                |
| ----------------------------------------------------------- | ------------------ | ----------------------------------------------------------------- |
| Small or medium personal projects                           | Usually low        | Rebuild time is often acceptable                                  |
| Large or complex projects with slow builds                  | Medium             | Deleting can cost significant time                                |
| Projects where you directly deploy from `dist/` or `build/` | Higher             | Make sure you have the artifacts elsewhere first                  |
| Projects involving native code or heavy compilation         | Medium–Higher      | Rebuilds can be expensive                                         |
| CI or shared build environments                             | Varies             | Often better handled through CI caching rather than local cleanup |

## Ways to Reduce Risk If You’re Nervous

Many people use these approaches when they want to be more careful:

- Only delete folders in projects you’re not actively working on right now.
- Use age-based rules (for example, only remove build folders that haven’t been modified in the last 14–30 days).
- Test the impact on one smaller project first before doing a broader cleanup.
- Keep a recent backup or stash of a particularly important `dist/` or `build/` folder the first time you try removing it.

## References for Further Reading

- Documentation for your specific build tool (Next.js, Turborepo, Gradle, etc.) usually explains what lives in these folders.
- Discussions on Reddit (r/webdev, r/javascript, r/typescript) about cleaning build directories.
- General advice in “12 Factor App” style guides around build artifacts.

---

**Related reading**

- [Rust: `target/` directories](./rust/target-directories.md)
- [Node.js / TypeScript: `node_modules/` directories](./node/node-modules.md)
