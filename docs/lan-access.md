# LAN access

LAN access exposes the dashboard to devices on the host's private Wi-Fi/Ethernet network over HTTPS. The daemon binds one detected private IP on port `8766`; it does not bind a public interface or create a router port-forward. Cellular and public internet access are not supported.

## Start LAN access

On the host computer, run:

```sh
ownyourbot daemon start --lan
```

If the daemon is already running without LAN access, restart it:

```sh
ownyourbot daemon stop
ownyourbot daemon start --lan
```

The start command prints an HTTPS address and the SHA-256 fingerprint for its self-signed certificate. If automatic interface selection picks the wrong private address or none is found, choose the host's Wi-Fi/Ethernet IPv4 address explicitly:

```sh
ownyourbot daemon start --lan --lan-address 192.168.1.25:8766
```

The local API/dashboard listener defaults to `127.0.0.1:8765`. If another service uses that port, choose a different loopback address; the HTTPS LAN listener remains on port `8766`:

```sh
ownyourbot daemon start --lan --listen 127.0.0.1:18765
```

The start command opens the local dashboard with a one-time loopback-only bootstrap link. The link is kept in the URL fragment and expires after 60 seconds; the browser redeems it for a local owner session that lasts 30 minutes. The host API token is not placed in the link. To reopen a dashboard manually, run the same `ownyourbot daemon start` command on the host; it opens a fresh owner session for an already-running daemon. A manually entered owner API token remains a fallback. Stop this background daemon with `ownyourbot daemon stop --url http://127.0.0.1:18765` before restarting it. If you also changed `--data-dir`, pass that same directory to `daemon stop`. `ownyourbot daemon start` launches a background process and returns; Ctrl-C stops only a daemon started directly in the foreground, such as `./bin/ownbotd`.

Use the address printed by the command. A different LAN IP can result in a new certificate and fingerprint. By default, the certificate and private key are kept in `~/.ownbot/lan.cert.pem` and `~/.ownbot/lan.key.pem`; if you set `--data-dir`, they are stored in that directory instead. Never share the private key.

## Check the certificate

The LAN HTTPS certificate is self-signed. Before trusting it, compare the SHA-256 fingerprint printed by the host CLI with the value shown in the host dashboard's **LAN access** section. You can independently calculate the certificate file's fingerprint on the host:

```sh
openssl x509 -in ~/.ownbot/lan.cert.pem -noout -fingerprint -sha256
```

If you chose a custom data directory, replace `~/.ownbot` with that directory.

The OpenSSL output separates hex pairs with colons; ignore colons and letter case when comparing. Transfer only `lan.cert.pem` to another device, using a method you trust. Do not transfer `lan.key.pem`. A browser warning by itself is not proof that the certificate is the host's: compare the fingerprint through a separate channel first. If the browser does not let you inspect or trust the matching certificate, stop and do not enter a pairing link.

### macOS laptop

1. Transfer `~/.ownbot/lan.cert.pem` to the laptop over a trusted local method such as AirDrop or USB.
2. Open **Keychain Access**, import the certificate into the login keychain, then open the certificate details and verify its SHA-256 fingerprint against the host.
3. In the certificate's **Trust** section, set **Secure Sockets Layer (SSL)** to **Always Trust** for this OwnYourBot certificate. Keep the trust scoped to this certificate; do not disable browser certificate checks globally.
4. Open the HTTPS address printed by the host and confirm the browser reports a secure connection.

Apple describes certificate trust controls in [Keychain Access](https://support.apple.com/guide/keychain-access/kyca11871/mac).

### iPhone or iPad

The HTTPS server currently uses a self-signed server certificate. Apple documents manually enabling trust for installed root certificates, but OwnYourBot does not yet provide a verified iOS certificate installation flow, and a physical iPhone/iPad check has not been done. Compare the host's SHA-256 fingerprint out of band before trusting any certificate. If Safari or another browser displays a certificate warning and you cannot verify and trust that exact certificate through iOS settings, stop; do not bypass the warning or enter the pairing link. Use the dashboard on the host until the iOS trust flow is verified. See [Apple's certificate trust instructions](https://support.apple.com/en-us/102390) for the platform's documented requirements.

### Android phone or tablet

Android certificate installation options vary by device and OS version. Google's Pixel guide documents **Settings → Security & privacy → More security settings → Encryption & credentials → Install a certificate → Wi-Fi certificate**. That procedure has not been verified as a way to establish browser trust for OwnYourBot's self-signed server certificate, and no physical Android check has been done. Compare the SHA-256 fingerprint with the host out of band. If the browser still shows a warning or the exact fingerprint cannot be verified, stop; do not bypass the warning or enter the pairing link. Use the dashboard on the host until Android trust is verified.

## Pair and revoke

1. On the host, run `ownyourbot daemon start --lan` to open the local dashboard with an owner session. If the daemon is already running, the command opens a session for it if the LAN address matches the running configuration.
2. In **LAN access**, create a pairing link and send it only to the intended device. The link contains a one-use credential and expires; anyone who gets it first can pair.
3. Open the HTTPS link on the trusted device and submit a device name. The browser stores its device token in that browser tab's session storage; it never receives the host's API token.
4. Revoke a device in the host dashboard's **Paired devices** list. Its next API request is rejected. Pair it again from the host if access is needed later.

Paired devices receive dashboard access, but cannot view LAN settings, create links, or manage other devices. **Forget this browser** removes its saved paired token from that browser tab; it does not revoke the device at the host. Use **Revoke access** on the host to invalidate it.

## Troubleshooting

- The host and device must be on the same trusted Wi-Fi/Ethernet network. Guest Wi-Fi, client/AP isolation, VLAN separation, VPN routing, or a host firewall can block device-to-device traffic.
- Allow inbound TCP port `8766` on the host's private network profile. Do not add a router port-forward or expose this port to the public internet.
- If the host reports no private LAN interface, connect it to Wi-Fi/Ethernet or pass `--lan-address PRIVATE_IP:8766` using its private IPv4 address.
- If the host reports that a daemon is already running without LAN access, run `ownyourbot daemon stop`, then `ownyourbot daemon start --lan`.
- If the certificate warning returns after the host IP changes, verify and trust the new fingerprint before pairing again.
- If the local dashboard is locked, rerun the same `ownyourbot daemon start` command on the host to open a fresh owner session. If a paired device is rejected, create and use a fresh link on the host. The owner token is never entered on a paired device.

## Not verified yet

The [isolated LAN lab workflow](https://github.com/TaxCollector23/OwnBot/actions/runs/40557519399) passed on commit `93e3221`. It uses two Linux Docker bridge containers to exercise the real HTTPS listener, pairing, replay rejection, separate device credentials, revocation, and restart persistence. This does not verify physical Wi-Fi, browser certificate trust, mobile devices, or firewall/router behavior. A real second-laptop, iPhone, and Android test is still required. Cellular access through a tunnel or relay remains future work.
