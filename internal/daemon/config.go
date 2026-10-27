package daemon

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const defaultListenAddr = "127.0.0.1:8765"

type Config struct {
	DataDir                string `json:"data_dir"`
	ListenAddr             string `json:"listen_addr,omitempty"`
	SocketPath             string `json:"socket_path,omitempty"`
	LAN                    bool   `json:"lan,omitempty"`
	LANAddr                string `json:"lan_addr,omitempty"`
	ShutdownTimeoutSeconds int    `json:"shutdown_timeout_seconds,omitempty"`
}

func Defaults() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	return Config{DataDir: filepath.Join(home, ".ownbot"), ListenAddr: defaultListenAddr, ShutdownTimeoutSeconds: 10}, nil
}

// Load resolves defaults < JSON config < environment < explicitly supplied flags.
// Existing OWNBOT_DATA_DIR and OWNBOT_ADDR variables remain supported.
func Load(args []string, getenv func(string) string, stderr io.Writer) (Config, error) {
	defaults, err := Defaults()
	if err != nil {
		return Config{}, err
	}
	fs := flag.NewFlagSet("ownbotd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var configPath, dataDir, listenAddr, socketPath, shutdown, lanAddr string
	var lan bool
	fs.StringVar(&configPath, "config", "", "JSON config file")
	fs.StringVar(&dataDir, "data-dir", "", "state directory (default ~/.ownbot)")
	fs.StringVar(&listenAddr, "listen", "", "loopback TCP address (default 127.0.0.1:8765)")
	fs.StringVar(&socketPath, "socket", "", "Unix socket path (Unix only; mutually exclusive with --listen)")
	fs.BoolVar(&lan, "lan", false, "enable paired-device HTTPS access on the local network")
	fs.StringVar(&lanAddr, "lan-address", "", "private interface IP:port for HTTPS (default detected LAN IP:8766)")
	fs.StringVar(&shutdown, "shutdown-timeout", "", "graceful shutdown timeout (for example 10s)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: ownbotd [options]\n\nRun OwnBot on loopback TCP or a Unix socket.\n\nOptions:")
		fs.PrintDefaults()
	}
	if err = fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	seenDataDir, seenListen, seenSocket, seenShutdown := false, false, false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "data-dir":
			seenDataDir = true
		case "listen":
			seenListen = true
		case "socket":
			seenSocket = true
		case "shutdown-timeout":
			seenShutdown = true
		}
	})
	if seenListen && seenSocket {
		return Config{}, errors.New("--listen and --socket are mutually exclusive")
	}
	if configPath == "" {
		configPath = getenv("OWNBOT_CONFIG")
	}

	cfg := defaults
	if configPath != "" {
		b, e := os.ReadFile(configPath)
		if e != nil {
			return Config{}, fmt.Errorf("read config file: %w", e)
		}
		var keys map[string]json.RawMessage
		if e = json.Unmarshal(b, &keys); e != nil {
			return Config{}, fmt.Errorf("parse config file: %w", e)
		}
		if keys == nil {
			return Config{}, errors.New("config file must contain a JSON object")
		}
		for key := range keys {
			switch key {
			case "data_dir", "listen_addr", "socket_path", "shutdown_timeout_seconds", "lan", "lan_addr":
			default:
				return Config{}, fmt.Errorf("unknown config field %q", key)
			}
		}
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields()
		if e = dec.Decode(&cfg); e != nil {
			return Config{}, fmt.Errorf("parse config file: %w", e)
		}
		var trailing any
		if e = dec.Decode(&trailing); e != io.EOF {
			return Config{}, errors.New("config file must contain exactly one JSON object")
		}
		_, socketSpecified := keys["socket_path"]
		_, listenSpecified := keys["listen_addr"]
		if socketSpecified && !listenSpecified {
			cfg.ListenAddr = ""
		}
	}
	if v := getenv("OWNBOT_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := getenv("OWNBOT_ADDR"); v != "" {
		cfg.ListenAddr = v
		cfg.SocketPath = ""
	}
	if v := getenv("OWNBOT_SOCKET"); v != "" {
		cfg.SocketPath = v
		if getenv("OWNBOT_ADDR") == "" {
			cfg.ListenAddr = ""
		}
	}
	if v := getenv("OWNBOT_SHUTDOWN_TIMEOUT"); v != "" {
		d, e := parseDurationSeconds(v)
		if e != nil {
			return Config{}, fmt.Errorf("OWNBOT_SHUTDOWN_TIMEOUT: %w", e)
		}
		cfg.ShutdownTimeoutSeconds = d
	}
	if seenDataDir {
		cfg.DataDir = dataDir
	}
	if seenListen {
		cfg.ListenAddr = listenAddr
		cfg.SocketPath = ""
	}
	if seenSocket {
		cfg.SocketPath = socketPath
		cfg.ListenAddr = ""
	}
	if seenShutdown {
		d, e := parseDurationSeconds(shutdown)
		if e != nil {
			return Config{}, fmt.Errorf("--shutdown-timeout: %w", e)
		}
		cfg.ShutdownTimeoutSeconds = d
	}
	if v := getenv("OWNBOT_LAN"); v != "" {
		cfg.LAN, err = strconv.ParseBool(v)
		if err != nil {
			return Config{}, errors.New("OWNBOT_LAN must be true or false")
		}
	}
	if v := getenv("OWNBOT_LAN_ADDR"); v != "" {
		cfg.LANAddr = v
		cfg.LAN = true
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "lan-address" {
			cfg.LANAddr = lanAddr
			cfg.LAN = true
		}
	})
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "lan" {
			cfg.LAN = lan
			if !lan {
				cfg.LANAddr = ""
			}
		}
	})
	if cfg.LANAddr != "" {
		cfg.LAN = true
	}
	if cfg.LAN && cfg.LANAddr == "" {
		cfg.LANAddr, err = DefaultLANAddress()
		if err != nil {
			return Config{}, err
		}
	}
	if err = validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func parseDurationSeconds(raw string) (int, error) {
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, errors.New("must be a positive duration")
	}
	seconds := int(d.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	if seconds > 300 {
		return 0, errors.New("must not exceed 5m")
	}
	return seconds, nil
}

func validate(c Config) error {
	if strings.TrimSpace(c.DataDir) == "" {
		return errors.New("data directory is required")
	}
	if c.ShutdownTimeoutSeconds < 1 || c.ShutdownTimeoutSeconds > 300 {
		return errors.New("shutdown timeout must be between 1 and 300 seconds")
	}
	if c.SocketPath != "" && c.ListenAddr != "" {
		return errors.New("choose exactly one of loopback TCP or Unix socket")
	}
	if c.SocketPath == "" && c.ListenAddr == "" {
		return errors.New("either listen_addr or socket_path is required")
	}
	if c.SocketPath != "" && !filepath.IsAbs(c.SocketPath) {
		return errors.New("Unix socket path must be absolute")
	}
	if c.ListenAddr != "" {
		host, port, err := net.SplitHostPort(c.ListenAddr)
		if err != nil {
			return fmt.Errorf("invalid listen address: %w", err)
		}
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if ip == nil || !ip.IsLoopback() {
			return errors.New("listen address must use a loopback IP address")
		}
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return errors.New("listen port must be between 1 and 65535")
		}
	}
	if c.LAN {
		if c.SocketPath != "" {
			return errors.New("LAN access requires a loopback TCP listener, not a Unix socket")
		}
		if err := validateLANAddress(c.LANAddr); err != nil {
			return err
		}
	} else if c.LANAddr != "" {
		return errors.New("LAN address requires LAN access to be enabled")
	}
	return nil
}
