# Node.js / TypeScript: `node_modules/` Directories

If you work with JavaScript or TypeScript projects, you've likely seen folders called `node_modules/` that can become surprisingly large. This guide is intended to give you some context if you're considering removing them and feel uncertain about the risks.

## Why These Folders Get So Big

When you run `npm install`, `yarn install`, or `pnpm install`, your package manager downloads all the packages your project depends on (and all of _their_ dependencies) into a `node_modules/` folder.

A few factors make these directories grow large:

- Modern JavaScript projects often have hundreds of dependencies.
- Each dependency can bring in many transitive dependencies.
- Some packages include platform-specific binaries or pre-built assets.
- In monorepos or projects with multiple packages, you can end up with multiple large `node_modules/` folders.

It's common for a single `node_modules/` folder to be several hundred megabytes to a few gigabytes. Developers who work across many JavaScript or TypeScript projects can easily accumulate 50+ GB just from these folders.

## Why Many People Consider Deleting Them

The contents of `node_modules/` are **not source code**. They are downloaded and/or generated based on the dependencies listed in your `package.json` and lockfile (`package-lock.json`, `yarn.lock`, or `pnpm-lock.yaml`).

In most cases, you can safely delete a `node_modules/` folder and recreate it later by running your package manager's install command again. The source code for your actual project lives outside of `node_modules/`.

Because of this, many developers regularly delete these folders when looking to free up space.

## Common Concerns

It's normal to feel hesitant. Here are some of the common worries people have:

- **"Will this break my project?"**  
  Usually not permanently. The main downside is that you'll need to reinstall dependencies, which takes time and (if you're on a slow connection) bandwidth.

- **"Some packages are hard to install."**  
  This is a real consideration. Some packages involve native compilation, require specific system libraries, or have complicated post-install scripts. Reinstalling them can sometimes be painful or fail in certain environments.

- **"I'm working offline or have limited data."**  
  Reinstalling large dependency trees can use significant bandwidth and may not be practical without a good connection.

- **"I have local changes or patches in node_modules."**  
  This is uncommon but does happen (especially with `patch-package` or manual edits). In those cases, deleting the folder would lose those changes.

## Risk Profile

| Situation                                       | Typical Risk Level | Notes                                                      |
| ----------------------------------------------- | ------------------ | ---------------------------------------------------------- |
| Small to medium personal or side projects       | Usually low        | Reinstall time is often acceptable                         |
| Large monorepos or projects with native modules | Medium             | Reinstall time or complexity can be significant            |
| Work projects with strict compliance needs      | Higher             | Some organizations prefer not to delete dependency folders |
| Air-gapped machines or very slow connections    | Higher             | Re-downloading dependencies may be difficult or expensive  |
| Projects where you've applied patches           | Higher             | Manual patches can be lost                                 |

## Ways People Reduce Risk

If you're unsure about deleting `node_modules/` folders, here are approaches that some developers use to feel more comfortable:

- Delete `node_modules/` in one project first as a test before doing it more broadly.
- Use age-based rules (for example, only remove folders that haven't been modified in the last 30–60 days).
- Keep a recent backup of particularly important or complex `node_modules/` folders the first time you try cleaning them.
- Use `pnpm` instead of npm or yarn in new projects — it generally uses significantly less disk space due to its content-addressable store.

## References for Further Reading

- Official npm documentation on node_modules
- Yarn documentation on Plug'n'Play (an alternative that avoids large node_modules folders)
- pnpm documentation on its storage model
- Various discussions on Reddit (r/node, r/typescript) about cleaning node_modules

---

**Related reading**

- [Rust: `target/` directories](../rust/target-directories.md)
- [General build artifacts](../build-artifacts.md)
