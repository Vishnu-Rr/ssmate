# Releasing

A release is cut by pushing a version tag:

```bash
git tag v0.1.0
git push origin v0.1.0
```

The `release` workflow then builds every target with GoReleaser, publishes the
archives, `.deb` and `.rpm` packages and checksums to the GitHub release,
attests their provenance, and updates the cask in
[Vishnu-Rr/homebrew-tap](https://github.com/Vishnu-Rr/homebrew-tap).

## One-time setup

The workflow's own token can write only to this repository, so the cask is
pushed to the tap over SSH with a deploy key, which can write to the tap and
nothing else:

1. Generate a key with no passphrase: `ssh-keygen -t ed25519 -N "" -f tapkey`.
2. Add `tapkey.pub` to `Vishnu-Rr/homebrew-tap` as a deploy key with write
   access: `gh repo deploy-key add tapkey.pub --repo Vishnu-Rr/homebrew-tap --allow-write`.
3. Save the private key as this repository's Actions secret
   `HOMEBREW_TAP_DEPLOY_KEY`: `gh secret set HOMEBREW_TAP_DEPLOY_KEY < tapkey`.
4. Delete both files.

## Before tagging

```bash
go test -race ./...
goreleaser check
HOMEBREW_TAP_DEPLOY_KEY=x goreleaser release --snapshot --clean --skip=publish   # builds everything, publishes nothing
```
