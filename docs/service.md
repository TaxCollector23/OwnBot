# User service

`ownbot daemon install` registers the daemon for the current account and starts it now. Setup uses the same per-user service on macOS and Linux when it starts a new daemon. It does not request administrator access.

```sh
ownbot daemon status
ownbot daemon install --listen 127.0.0.1:18765 --data-dir "$HOME/.ownbot"
ownbot daemon uninstall --listen 127.0.0.1:18765 --data-dir "$HOME/.ownbot"
```

Pass the same `--config`, `--listen`, `--data-dir`, `--lan`, and `--lan-address` options used to start the daemon. A configured loopback port and an explicit LAN access opt-in are recorded in the service definition. User services currently require a loopback TCP listener; Unix socket service control is not implemented yet.

macOS uses `~/Library/LaunchAgents/com.ownbot.daemon.plist`. Linux uses `systemd --user` and `~/.config/systemd/user/ownbot.service` (or `$XDG_CONFIG_HOME/systemd/user/ownbot.service`). These start at login for the current account. On other operating systems the commands report that user services are unsupported; `daemon start` remains a current-session background start.

Uninstall stops and removes the service definition, but keeps the state directory, API token, models, agents, and audit data. Use the same listener and data directory used at install time when checking or removing a service. If the daemon is running with different settings, inspect its existing command/configuration before replacing the service.
