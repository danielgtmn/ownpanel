# Contributing to ownpanel

Thanks for helping. ownpanel is a small project, so a short heads-up before larger work saves everyone time.

## Before you start

- **Questions and ideas:** [Discussions](https://github.com/danielgtmn/ownpanel/discussions).
- **Bugs and concrete features:** [issues](https://github.com/danielgtmn/ownpanel/issues/new/choose). For anything bigger than a small fix, comment on the issue (or open one) first, so we can agree on the approach.
- **Security problems:** never in public, see [SECURITY.md](SECURITY.md).
- What is planned and in which order: the [roadmap board](https://github.com/users/danielgtmn/projects/5).

## Setting up

You need Go (the version in `go.mod`) and a current Node.js with npm. `scripts/dev.sh` installs Caddy into `bin/` if it is missing. Details in [Development](https://github.com/danielgtmn/ownpanel/wiki/Development).

```sh
scripts/dev.sh                    # Caddy + ownpaneld locally
cd web && npm ci && npm run dev   # the web interface with hot reload, talking to dev.sh
```

## Making a change

1. Fork, then branch from `main`.
2. Read [AGENTS.md](AGENTS.md): layout, the rules that are easy to break (permissions only in `internal/panel`, secrets never in logs or responses, root stays out of the daemon, new migrations only), and the commands. When you touch `web/`, read [web/AGENTS.md](web/AGENTS.md) too.
3. Add tests with the change, and update the documentation in `wiki/` (and the README, where it covers the same thing) in the same pull request.
4. Run what CI runs for what you touched:

   ```sh
   make test && go test -race ./...
   cd web && npm ci && npm run lint && npm test -- --run && npm run build
   ```

5. **Try it for real.** For changes to the interface, run the app and click through the flow. For install and update changes, test in a systemd container. If something could not be tested end to end, say so in the pull request.

## Pull requests

- The title follows [Conventional Commits](https://www.conventionalcommits.org): `feat: …`, `fix: …`, `docs: …`, `chore: …`, with an optional scope like `feat(dns): …`. The same applies to every commit. The `commits` check enforces it, and release versions are computed from it.
- Reference the issue (`Closes #12`) and fill in the template.
- Pull requests are merged with a merge commit, so keep the commits readable. There is no need to squash.

## Text in the interface

Labels, hints and messages describe what happens for the person, never the technology behind it: "Simplify names", not "Slugify". No library, protocol or internal names.

## License

ownpanel is licensed under the [AGPL-3.0](LICENSE). By contributing, you agree that your contribution is licensed under the same terms.

Everyone taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md).
