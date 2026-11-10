# LAN access

OwnBot's interactive setup starts the daemon with LAN access enabled after a model passes its capability probe and an agent is saved. It prints a one-use pairing QR code and URL. The host performs inference and tool execution; the remote device is only a dashboard. LAN access stays on the host's private Wi-Fi or Ethernet network. OwnBot does not use a relay, port-forward, or public service, and cellular/public access is unsupported.

## Start or repair LAN access manually

For a custom URL, an existing installation, or a daemon started before setup, run on the host:

```sh
ownbot daemon start --lan
```

If the daemon is already running without LAN access:

```sh
ownbot daemon stop
ownbot daemon start --lan
```

The local owner API defaults to `127.0.0.1:8765`; the separate LAN dashboard uses HTTPS on port `8766` and binds one detected private interface. It does not bind `0.0.0.0` or create a router port-forward. In a phone browser, `localhost` and `127.0.0.1` refer to the phone itself; open the private LAN URL that OwnBot prints for the host. If interface detection picks the wrong address, choose the host's private Wi-Fi/Ethernet IPv4 address:

```sh
ownbot daemon start --lan --lan-address 192.168.1.25:8766
```

The CLI opens the host's local dashboard with a one-use owner session. The link expires after 60 seconds; the browser redeems it for a local owner session that lasts 30 minutes. The host API token is never placed in that link. Over SSH, the CLI prints an SSH tunnel command for the local owner dashboard. The tunnel is for the host's owner session; it does not expose the LAN listener to the internet.

If the host reports a daemon is already running on a different LAN address, stop it and restart with the requested `--lan-address`. A LAN IP change creates a new local certificate and fingerprint.

## Trust the host certificate before pairing

OwnBot uses a self-signed, host-local root certificate for its LAN HTTPS listener. The daemon keeps the private key in the protected state directory and exports a public DER certificate and an iOS configuration profile:

- `~/.ownbot/lan.cert.cer` — public certificate for Android and desktop certificate stores.
- `~/.ownbot/lan.cert.mobileconfig` — profile containing only the public certificate for iPhone/iPad.
- `~/.ownbot/lan.key.pem` — private key. Never copy or share it.

The generated iOS `.mobileconfig` profile is **unsigned**. iOS may label it unverified before installation. Verify the certificate and fingerprint against the host before installing it; do not install it if you cannot independently verify the file. OwnBot has not yet validated this flow on a physical Apple device.

If you use `--data-dir`, the files are in that directory. Setup and `ownbot daemon start --lan` print the certificate SHA-256 fingerprint. Compare the certificate you transfer with that fingerprint before installing it. You can calculate the DER file's fingerprint on the host with:

```sh
openssl x509 -inform DER -in ~/.ownbot/lan.cert.cer -noout -fingerprint -sha256
```

The command output separates hex pairs with colons; ignore colons and letter case when comparing. Transfer the public certificate/profile from the host using a trusted method such as AirDrop or USB. If you cannot verify that the file matches the printed fingerprint, do not install it. Installing a root CA gives that certificate authority the ability to authenticate TLS sites on the device; keep the OwnBot host and private key protected and remove the certificate from the device when it is no longer needed.

### iPhone or iPad

1. Transfer `lan.cert.mobileconfig` to the iPhone/iPad from the host using AirDrop or another trusted local method.
2. Verify that the unsigned profile contains OwnBot's certificate and compare its SHA-256 fingerprint with the value printed on the host. If you cannot verify the certificate, stop without installing the profile.
3. Install the profile from **Settings → General → VPN & Device Management** (the downloaded profile may also appear in Settings).
4. Enable the certificate under **Settings → General → About → Certificate Trust Settings → Enable Full Trust for Root Certificates**.
5. Open the LAN URL again. If Safari still reports a certificate warning, stop; do not continue through it or enter the pairing link.

Apple notes that a manually installed certificate profile is not automatically trusted for SSL/TLS; the user must enable full trust in Certificate Trust Settings. The OwnBot profile and steps have not yet been tested on a physical iPhone or iPad. See [Apple's certificate trust instructions](https://support.apple.com/en-us/102390).

### Android phone or tablet

1. Transfer `lan.cert.cer` to the phone from the host through a trusted local method.
2. Verify its SHA-256 fingerprint against the host terminal before installing.
3. In Android settings, find **Security & privacy → More security settings → Encryption & credentials → Install a certificate → CA certificate**. Labels vary by manufacturer; Google's [Pixel certificate guide](https://support.google.com/pixelphone/answer/2844832) documents the certificate installation settings.
4. Reopen the OwnBot LAN URL. If the selected browser still shows a certificate warning, stop and do not enter the pairing link.

Android warns when a user CA is installed, and browser trust behavior can vary by Android version, device policy, and browser. OwnBot's Android certificate and browser flow has not been tested on a physical phone or tablet.

### Laptop or desktop

Transfer `lan.cert.cer` over a trusted local method, compare its fingerprint on the host, then install it as a local root certificate using that operating system's certificate manager. On macOS, import it into Keychain Access and scope trust to this OwnBot certificate. If the browser still reports a certificate warning, stop and do not pair.

## Pair and revoke a device

After the certificate is trusted on the phone, scan the QR printed by successful `ownbot setup`. It contains a short-lived, one-use pairing secret, never the permanent host API token. If you need another link later, run:

```sh
ownbot daemon pair
```

The command requires local owner authorization and LAN mode. Open the link only on the intended device; anyone who gets it first can pair one device before it expires. The dashboard asks for a device name and stores that device's token in the browser's `sessionStorage`. It survives page reloads in that tab and is removed when the tab session ends. The browser never receives the host API token.

List or revoke devices from the host:

```sh
ownbot daemon devices
ownbot daemon devices --revoke DEVICE_ID
```

Revocation makes the next API request from that device fail. **Forget this browser** removes its token from that tab; host-side revocation is required to invalidate the device credential.

## Dashboard behavior

The mobile dashboard can select among enabled agents, submit a prompt, show that the host is processing the task, display the response and run history, and review supported tool approvals. Task submission is currently a synchronous request: the progress message stays visible while the host's model works, but OwnBot does not stream tokens or per-step progress to the browser. The host records the completed run and its steps for review.

## Troubleshooting

- Keep the host and phone on the same trusted Wi-Fi/Ethernet network. Guest Wi-Fi, client isolation, VLAN separation, VPN routing, or a host firewall can block device-to-device traffic.
- Allow inbound TCP port `8766` on the host's private network profile. Do not expose it through a router port-forward.
- If no private interface is found, connect the host to Wi-Fi/Ethernet or pass `--lan-address PRIVATE_IP:8766`.
- If the host IP changes, transfer and verify the new certificate fingerprint before pairing again.
- If a paired browser is rejected, create a fresh pairing link on the host. The owner token is never entered on the paired device.

## Verification status

Automated Go and isolated LAN tests cover HTTPS setup, short-lived pairing, one-use token rejection, device credentials, revocation, and restart persistence. The certificate file/profile structure is tested and the certificate is verified with a local root pool. These checks do not establish phone operating-system trust or browser behavior. Real second-laptop, iPhone, and Android tests on the same Wi-Fi are still required. See [STATUS.md](../STATUS.md) and [TODO.md](../TODO.md).
