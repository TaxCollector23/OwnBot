package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
)

type pairedDevice struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	RevokedAt string `json:"revoked_at"`
}

func runDaemonAccess(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: ownbot daemon pair|devices [flags]")
	}
	action := args[0]
	fs := flag.NewFlagSet("daemon "+action, flag.ContinueOnError)
	apiURL, dataDir := commonFlags(fs)
	revokeID := fs.String("revoke", "", "revoke a paired device by ID (devices only)")
	if err := parseFlags(fs, args[1:], stderr); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("daemon %s does not accept positional arguments", action)
	}
	client, err := clientFor(*apiURL, *dataDir)
	if err != nil {
		return err
	}
	switch action {
	case "pair":
		if *revokeID != "" {
			return errors.New("daemon pair does not accept --revoke")
		}
		var access struct {
			Enabled bool `json:"enabled"`
		}
		if err := client.Do(context.Background(), http.MethodGet, "/api/access", nil, &access); err != nil {
			return err
		}
		if !access.Enabled {
			return errors.New("device pairing requires LAN access; run `ownbot daemon start --lan` first")
		}
		var pairing struct {
			PairingURL  string `json:"pairing_url"`
			ExpiresAt   string `json:"expires_at"`
			Fingerprint string `json:"certificate_fingerprint"`
		}
		if err := client.Do(context.Background(), http.MethodPost, "/api/devices/pairing", map[string]any{}, &pairing); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "One-use pairing link (expires %s):\n%s\n", pairing.ExpiresAt, pairing.PairingURL)
		_, _ = fmt.Fprintln(stdout, "Open it only on the device you intend to pair. Anyone with the link can pair one device before it expires.")
		if pairing.Fingerprint != "" {
			_, _ = fmt.Fprintf(stdout, "Certificate fingerprint (verify this before trusting the host on the new device): %s\n", pairing.Fingerprint)
		}
		return nil
	case "devices":
		if *revokeID != "" {
			if err := client.Do(context.Background(), http.MethodDelete, "/api/devices/"+urlPath(*revokeID), map[string]any{}, nil); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(stdout, "Revoked paired device %s.\n", *revokeID)
			return nil
		}
		var devices []pairedDevice
		if err := client.Do(context.Background(), http.MethodGet, "/api/devices", nil, &devices); err != nil {
			return err
		}
		return writeJSON(stdout, devices)
	default:
		return fmt.Errorf("unknown daemon command %q; use `ownbot daemon start|stop|pair|devices`", action)
	}
}
