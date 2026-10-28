# Homebrew

Homebrew builds both `ownbot` and `ownbotd` from the latest `main` source. Go is installed as a build dependency; no prebuilt bottle is provided.

```sh
brew tap TaxCollector23/ownbot https://github.com/TaxCollector23/OwnBot
brew install --HEAD taxcollector23/ownbot/ownbot
```

This is a moving `--HEAD` install, not a stable release. To fetch and rebuild from the latest `main`, run `brew upgrade --fetch-HEAD taxcollector23/ownbot/ownbot`.

The formula supports macOS and Linux. Installing it does not start or register a service. `ownbot daemon install` uses a per-user LaunchAgent on macOS and `systemd --user` on Linux. Native service registration is unsupported on other operating systems. FreeBSD is not a supported Homebrew target; its npm/source path can be tried with Go 1.24+ and Node.js 18+ for npm installs, but is not verified.
