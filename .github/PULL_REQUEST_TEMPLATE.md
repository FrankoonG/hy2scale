<!--
Thanks for opening a PR!

For anything non-trivial, please file an issue first so we can agree on the
direction before you spend effort on the implementation.

Questions or discussions belong in:
  https://github.com/FrankoonG/hy2scale/discussions
-->

## Summary

<!-- One or two sentences describing what this change does and why. -->

## Type

<!-- Delete rows that do not apply. -->

- [ ] Bug fix
- [ ] Feature / enhancement
- [ ] Refactor (no functional change)
- [ ] Documentation / wiki
- [ ] Build / CI / release tooling
- [ ] Translation (en / zh / ko)

## Related issue

<!-- "Closes #123" or "Refs #456", or "N/A" for trivial changes. -->

## Affected components

<!-- Tick everything touched. -->

- [ ] Mesh / relay plane (`internal/relay`)
- [ ] Nested discovery
- [ ] Hysteria 2 server
- [ ] SOCKS5 / HTTP / Shadowsocks
- [ ] L2TP/IPsec (strongSwan + xl2tpd)
- [ ] IKEv2/IPsec (strongSwan)
- [ ] WireGuard (wireguard-go + gvisor)
- [ ] Routing rules / TUN mode
- [ ] TLS / certificate store
- [ ] Users / sessions
- [ ] Web UI (`web/app`, `web/ui-framework`)
- [ ] i18n (EN / ZH / KO)
- [ ] iKuai v4 ipkg / compat mode
- [ ] API (`internal/api`)
- [ ] Docs / Wiki

## How was this tested?

<!--
Include enough detail for a reviewer to reproduce. Relevant for this project:
- Did you test against the demo cluster (`test/docker-compose.demo.yml`)?
- Did you try Playwright flows for UI changes in BOTH light mode AND Dark Reader (see docs/dark-reader-testing.md)?
- For protocol changes — which client OS did you verify with?
- For compat-mode changes — did you test on iKuai v4 or a stripped-kernel target?
-->

## Screenshots (UI changes)

<!-- Drag-and-drop before/after screenshots. Dual-mode (light + Dark Reader) is required for frontend changes. -->

## Breaking changes

<!-- Describe any config format / API change that requires user action, or write "None". -->

## Checklist

- [ ] The change is focused and self-contained; unrelated refactors are in separate commits / PRs.
- [ ] Commit messages are imperative and descriptive (see `git log --oneline` for the house style).
- [ ] All user-visible strings are i18n-ised in `web/app/src/i18n/{en,zh,ko}.json` (no hard-coded text in JSX).
- [ ] No secrets, private keys, or personal data in the diff.
- [ ] `docs/` and `test/` are not committed (they are gitignored — keep temp files there).
- [ ] I ran the relevant subset of tests / manual checks.
