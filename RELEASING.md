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

The workflow's own token can write only to this repository, so updating the
tap needs a token of its own:

1. Create a fine-grained personal access token with access to the
   `Vishnu-Rr/homebrew-tap` repository only, and the permission
   **Contents: Read and write**.
2. Save it in this repository as the Actions secret
   `HOMEBREW_TAP_GITHUB_TOKEN`.

## Before tagging

```bash
go test -race ./...
goreleaser check
goreleaser release --snapshot --clean --skip=publish   # builds everything locally, publishes nothing
```
