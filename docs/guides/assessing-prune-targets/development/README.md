# Development Machines

If you're a developer looking at your own machine and feeling a bit unsure about what you can safely remove, you're in the right place.

This section exists for people who want to reclaim space but don't necessarily have deep confidence in every part of their stack. You might be worried about breaking a build, losing work, or accidentally deleting something important. That's completely normal.

## How These Guides Are Written

These guides try to be:

- **Practical** — focused on things that actually take up a lot of space for many developers.
- **Risk-aware** — we talk about when something is usually low-risk vs when it might be worth being more cautious.
- **Non-prescriptive** — we don't tell you what you "should" do. Your situation (client work, regulated environments, long rebuild times, limited bandwidth, etc.) is yours to judge.

The goal is to give you enough background and context so you can make decisions you're comfortable with, rather than following generic advice that might not apply to you.

## Language & Tool Specific Guidance

- [Rust: `target/` directories](./rust/target-directories.md)
- [Node.js / TypeScript: `node_modules/` directories](./node/node-modules.md)
- [Go: Module cache and build artifacts](./go/module-cache.md)

## Cross-Language Patterns

- [General build artifacts (`dist/`, `build/`, `.next/`, `.turbo/`, etc.)](./build-artifacts.md)

---

**Remember**: These are just patterns that many developers have found useful. They are not rules, and they may not be appropriate for your specific situation.
