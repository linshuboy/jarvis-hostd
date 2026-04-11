package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"jarvisai/runtime/hostd/internal/appctl"
	"jarvisai/runtime/hostd/internal/buildinfo"
	"jarvisai/runtime/hostd/internal/config"
	"jarvisai/runtime/hostd/internal/host"
	"jarvisai/runtime/hostd/internal/hostdrun"
	"jarvisai/runtime/hostd/internal/state"
	"jarvisai/runtime/hostd/internal/winsvc"
	"jarvisai/runtime/hostd/internal/wsclient"
)

type Runner interface {
	Run(context.Context) error
}

type RunnerFactory func(config.Config, *state.Store, *log.Logger) (Runner, error)

type Dependencies struct {
	Stdout        io.Writer
	Stderr        io.Writer
	RunnerFactory RunnerFactory
}

func Execute(ctx context.Context, args []string, deps Dependencies) error {
	stdout := deps.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := deps.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	if deps.RunnerFactory == nil {
		deps.RunnerFactory = defaultRunnerFactory
	}
	if len(args) == 0 {
		return runCommand(ctx, []string{"run"}, deps)
	}
	switch args[0] {
	case "run":
		return runCommand(ctx, args, deps)
	case "app":
		return appCommand(args[1:], stdout, stderr)
	case "config":
		return configCommand(args[1:], stdout, stderr)
	case "service":
		return serviceCommand(ctx, args[1:], deps)
	case "version":
		return versionCommand(stdout)
	case "pair":
		return pairCommand(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	default:
		return runCommand(ctx, append([]string{"run"}, args...), deps)
	}
}

func appCommand(args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("app subcommand is required")
	}
	switch args[0] {
	case "snapshot":
		return appSnapshot(args[1:], stdout, stderr)
	case "set-token":
		return appSetToken(args[1:], stdout, stderr)
	case "clear-token":
		return appClearToken(args[1:], stdout, stderr)
	case "reconnect":
		return appReconnect(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unsupported app subcommand: %s", args[0])
	}
}

func runCommand(ctx context.Context, args []string, deps Dependencies) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(deps.Stderr)
	options, err := parseRunOptions(flags, args[1:])
	if err != nil {
		return err
	}
	loaded, err := config.Load(options)
	if err != nil {
		return err
	}
	if err := config.ValidateForRun(loaded.Config); err != nil {
		return err
	}
	logger := log.New(deps.Stderr, "hostd ", log.LstdFlags|log.Lmsgprefix)
	runner, err := deps.RunnerFactory(loaded.Config, state.NewStore(loaded.StatePath), logger)
	if err != nil {
		return err
	}
	return runner.Run(ctx)
}

func configCommand(args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("config subcommand is required")
	}
	switch args[0] {
	case "validate":
		return configValidate(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unsupported config subcommand: %s", args[0])
	}
}

func configValidate(args []string, stdout io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("config validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options, err := parseRunOptions(flags, args)
	if err != nil {
		return err
	}
	loaded, err := config.Load(options)
	if err != nil {
		return err
	}
	if err := config.ValidateForRun(loaded.Config); err != nil {
		return err
	}
	return printJSON(stdout, map[string]any{
		"valid":       true,
		"config_path": loaded.ConfigPath,
		"state_path":  loaded.StatePath,
		"config":      loaded.Config,
	})
}

func versionCommand(stdout io.Writer) error {
	return printJSON(stdout, buildinfo.Info())
}

func serviceCommand(ctx context.Context, args []string, deps Dependencies) error {
	if len(args) == 0 {
		return fmt.Errorf("service subcommand is required")
	}
	switch args[0] {
	case "run":
		return serviceRunCommand(ctx, args[1:], deps)
	default:
		return fmt.Errorf("unsupported service subcommand: %s", args[0])
	}
}

func serviceRunCommand(ctx context.Context, args []string, deps Dependencies) error {
	flags := flag.NewFlagSet("service run", flag.ContinueOnError)
	flags.SetOutput(deps.Stderr)
	options, err := parseRunOptions(flags, args)
	if err != nil {
		return err
	}
	loaded, err := config.Load(options)
	if err != nil {
		return err
	}
	if err := config.ValidateForRun(loaded.Config); err != nil {
		return err
	}
	logger := log.New(deps.Stderr, "hostd ", log.LstdFlags|log.Lmsgprefix)
	runner, err := deps.RunnerFactory(loaded.Config, state.NewStore(loaded.StatePath), logger)
	if err != nil {
		return err
	}
	return winsvc.Run(ctx, "hostd", logger, runner.Run)
}

func pairCommand(args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("pair subcommand is required")
	}
	switch args[0] {
	case "status":
		return pairStatus(args[1:], stdout, stderr)
	case "set-token":
		return pairSetToken(args[1:], stdout, stderr)
	case "clear-token":
		return pairClearToken(args[1:], stdout, stderr)
	case "revoke":
		return pairRevoke(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unsupported pair subcommand: %s", args[0])
	}
}

func pairStatus(args []string, stdout io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("pair status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options, err := parsePathOptions(flags, args)
	if err != nil {
		return err
	}
	configPath, statePath, err := config.ResolvePaths(options)
	if err != nil {
		return err
	}
	store := state.NewStore(statePath)
	current, err := store.Load()
	if err != nil {
		return err
	}
	return printJSON(stdout, map[string]any{
		"config_path":       configPath,
		"state_path":        statePath,
		"runtime_id":        current.RuntimeID,
		"pairing_state":     current.PairingState,
		"has_runtime_token": strings.TrimSpace(current.RuntimeToken) != "",
		"last_gateway_url":  current.LastGatewayURL,
		"last_connected_at": current.LastConnectedAt,
		"last_error":        current.LastError,
	})
}

func pairSetToken(args []string, stdout io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("pair set-token", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options, token, err := parseTokenMutationOptions(flags, args)
	if err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("token is required")
	}
	configPath, statePath, err := config.ResolvePaths(options)
	if err != nil {
		return err
	}
	current, err := persistRuntimeToken(statePath, token)
	if err != nil {
		return err
	}
	return printJSON(stdout, map[string]any{
		"config_path":       configPath,
		"state_path":        statePath,
		"runtime_id":        current.RuntimeID,
		"pairing_state":     current.PairingState,
		"has_runtime_token": true,
	})
}

func pairClearToken(args []string, stdout io.Writer, stderr io.Writer) error {
	return updatePairingState(args, stdout, stderr, state.PairingStateUnpaired, false)
}

func pairRevoke(args []string, stdout io.Writer, stderr io.Writer) error {
	return updatePairingState(args, stdout, stderr, state.PairingStateRevoked, false)
}

func updatePairingState(args []string, stdout io.Writer, stderr io.Writer, pairingState string, keepToken bool) error {
	flags := flag.NewFlagSet("pair state", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options, err := parsePathOptions(flags, args)
	if err != nil {
		return err
	}
	configPath, statePath, err := config.ResolvePaths(options)
	if err != nil {
		return err
	}
	current, err := updatePersistedPairingState(statePath, pairingState, keepToken)
	if err != nil {
		return err
	}
	return printJSON(stdout, map[string]any{
		"config_path":       configPath,
		"state_path":        statePath,
		"runtime_id":        current.RuntimeID,
		"pairing_state":     current.PairingState,
		"has_runtime_token": strings.TrimSpace(current.RuntimeToken) != "",
	})
}

func parseRunOptions(flags *flag.FlagSet, args []string) (config.Options, error) {
	var (
		configPath        string
		statePath         string
		controlSocketPath string
	)
	var (
		gatewayWSURL          stringFlag
		gatewayTLSMode        stringFlag
		gatewayTLSFingerprint stringFlag
		displayName           stringFlag
		loggingLevel          stringFlag
		heartbeatSeconds      intFlag
		hostEnabled           boolFlag
	)
	flags.StringVar(&configPath, "config", "", "path to hostd config.json")
	flags.StringVar(&statePath, "state", "", "path to hostd state.json")
	flags.StringVar(&controlSocketPath, "control-socket", "", "path to hostd app control socket")
	flags.Var(&gatewayWSURL, "gateway-ws-url", "gateway websocket url")
	flags.Var(&gatewayTLSMode, "gateway-tls-mode", "gateway tls mode: off|system|pinned")
	flags.Var(&gatewayTLSFingerprint, "gateway-tls-fingerprint", "gateway tls fingerprint")
	flags.Var(&displayName, "display-name", "runtime display name")
	flags.Var(&loggingLevel, "log-level", "log level")
	flags.Var(&heartbeatSeconds, "heartbeat-seconds", "heartbeat seconds")
	flags.Var(&hostEnabled, "host-enabled", "whether host.main is enabled")
	if err := flags.Parse(args); err != nil {
		return config.Options{}, err
	}
	options := config.Options{
		ConfigPath:        configPath,
		StatePath:         statePath,
		ControlSocketPath: controlSocketPath,
	}
	if gatewayWSURL.set {
		options.GatewayWSURL = &gatewayWSURL.value
	}
	if gatewayTLSMode.set {
		options.GatewayTLSMode = &gatewayTLSMode.value
	}
	if gatewayTLSFingerprint.set {
		options.GatewayTLSFingerprint = &gatewayTLSFingerprint.value
	}
	if displayName.set {
		options.DisplayName = &displayName.value
	}
	if loggingLevel.set {
		options.LoggingLevel = &loggingLevel.value
	}
	if heartbeatSeconds.set {
		options.HeartbeatSeconds = &heartbeatSeconds.value
	}
	if hostEnabled.set {
		options.HostEnabled = &hostEnabled.value
	}
	return options, nil
}

func parsePathOptions(flags *flag.FlagSet, args []string) (config.Options, error) {
	var (
		configPath        string
		statePath         string
		controlSocketPath string
	)
	flags.StringVar(&configPath, "config", "", "path to hostd config.json")
	flags.StringVar(&statePath, "state", "", "path to hostd state.json")
	flags.StringVar(&controlSocketPath, "control-socket", "", "path to hostd app control socket")
	if err := flags.Parse(args); err != nil {
		return config.Options{}, err
	}
	return config.Options{
		ConfigPath:        configPath,
		StatePath:         statePath,
		ControlSocketPath: controlSocketPath,
	}, nil
}

func parseTokenMutationOptions(flags *flag.FlagSet, args []string) (config.Options, string, error) {
	var token string
	options, err := parsePathOptionsWithToken(flags, args, &token)
	return options, token, err
}

func parsePathOptionsWithToken(flags *flag.FlagSet, args []string, token *string) (config.Options, error) {
	var (
		configPath        string
		statePath         string
		controlSocketPath string
	)
	flags.StringVar(&configPath, "config", "", "path to hostd config.json")
	flags.StringVar(&statePath, "state", "", "path to hostd state.json")
	flags.StringVar(&controlSocketPath, "control-socket", "", "path to hostd app control socket")
	flags.StringVar(token, "token", "", "runtime token to persist into state")
	if err := flags.Parse(args); err != nil {
		return config.Options{}, err
	}
	return config.Options{
		ConfigPath:        configPath,
		StatePath:         statePath,
		ControlSocketPath: controlSocketPath,
	}, nil
}

func defaultRunnerFactory(cfg config.Config, store *state.Store, logger *log.Logger) (Runner, error) {
	hostComponent, err := host.NewComponent(host.Options{
		ComponentID:        host.ComponentID,
		RuntimeVersion:     buildinfo.RuntimeVersion(),
		MaxReadBytes:       host.DefaultMaxReadBytes,
		MaxOutputBytes:     host.DefaultMaxOutputBytes,
		DefaultExecTimeout: host.DefaultExecTimeout,
	})
	if err != nil {
		return nil, err
	}
	return hostdrun.New(hostdrun.Options{
		Config:        cfg,
		StateStore:    store,
		HostComponent: hostComponent,
		Dialer:        wsclient.DefaultDialer{},
		Logger:        logger,
	}), nil
}

func printUsage(output io.Writer) {
	_, _ = fmt.Fprintln(output, "hostd run [flags]")
	_, _ = fmt.Fprintln(output, "hostd app snapshot [--config PATH] [--state PATH] [--control-socket PATH]")
	_, _ = fmt.Fprintln(output, "hostd app set-token --token TOKEN [--config PATH] [--state PATH] [--control-socket PATH]")
	_, _ = fmt.Fprintln(output, "hostd app clear-token [--config PATH] [--state PATH] [--control-socket PATH]")
	_, _ = fmt.Fprintln(output, "hostd app reconnect [--config PATH] [--state PATH] [--control-socket PATH]")
	_, _ = fmt.Fprintln(output, "hostd config validate [flags]")
	_, _ = fmt.Fprintln(output, "hostd service run [flags]")
	_, _ = fmt.Fprintln(output, "hostd version")
	_, _ = fmt.Fprintln(output, "hostd pair status [--config PATH] [--state PATH]")
	_, _ = fmt.Fprintln(output, "hostd pair set-token --token TOKEN [--config PATH] [--state PATH]")
	_, _ = fmt.Fprintln(output, "hostd pair clear-token [--config PATH] [--state PATH]")
	_, _ = fmt.Fprintln(output, "hostd pair revoke [--config PATH] [--state PATH]")
}

func printJSON(output io.Writer, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "%s\n", encoded)
	return err
}

type stringFlag struct {
	value string
	set   bool
}

func (f *stringFlag) String() string {
	return f.value
}

func (f *stringFlag) Set(value string) error {
	f.value = value
	f.set = true
	return nil
}

type intFlag struct {
	value int
	set   bool
}

func (f *intFlag) String() string {
	return ""
}

func (f *intFlag) Set(value string) error {
	parsed, err := config.ParseInt(value)
	if err != nil {
		return err
	}
	f.value = parsed
	f.set = true
	return nil
}

type boolFlag struct {
	value bool
	set   bool
}

func (f *boolFlag) String() string {
	if f.value {
		return "true"
	}
	return "false"
}

func (f *boolFlag) Set(value string) error {
	parsed, err := config.ParseBool(value)
	if err != nil {
		return err
	}
	f.value = parsed
	f.set = true
	return nil
}

var ErrUsage = errors.New("usage")

func appSnapshot(args []string, stdout io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("app snapshot", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options, err := parsePathOptions(flags, args)
	if err != nil {
		return err
	}
	configPath, statePath, controlSocketPath, err := resolveAppPaths(options)
	if err != nil {
		return err
	}
	snapshot, err := appctl.SnapshotStatus(controlSocketPath)
	if err != nil {
		if !errors.Is(err, appctl.ErrUnavailable) {
			return err
		}
		snapshot, err = fallbackAppSnapshot(statePath)
		if err != nil {
			return err
		}
		return printJSON(stdout, buildAppSnapshotPayload("state-fallback", false, configPath, statePath, controlSocketPath, snapshot))
	}
	return printJSON(stdout, buildAppSnapshotPayload("control-socket", true, configPath, statePath, controlSocketPath, snapshot))
}

func appSetToken(args []string, stdout io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("app set-token", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options, token, err := parseTokenMutationOptions(flags, args)
	if err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("token is required")
	}
	configPath, statePath, controlSocketPath, err := resolveAppPaths(options)
	if err != nil {
		return err
	}
	snapshot, err := appctl.SetRuntimeToken(controlSocketPath, token)
	if err != nil {
		if !errors.Is(err, appctl.ErrUnavailable) {
			return err
		}
		if _, persistErr := persistRuntimeToken(statePath, token); persistErr != nil {
			return persistErr
		}
		snapshot, err = fallbackAppSnapshot(statePath)
		if err != nil {
			return err
		}
		return printJSON(stdout, buildAppSnapshotPayload("state-fallback", false, configPath, statePath, controlSocketPath, snapshot))
	}
	return printJSON(stdout, buildAppSnapshotPayload("control-socket", true, configPath, statePath, controlSocketPath, snapshot))
}

func appClearToken(args []string, stdout io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("app clear-token", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options, err := parsePathOptions(flags, args)
	if err != nil {
		return err
	}
	configPath, statePath, controlSocketPath, err := resolveAppPaths(options)
	if err != nil {
		return err
	}
	snapshot, err := appctl.ClearRuntimeToken(controlSocketPath)
	if err != nil {
		if !errors.Is(err, appctl.ErrUnavailable) {
			return err
		}
		if _, clearErr := updatePersistedPairingState(statePath, state.PairingStateUnpaired, false); clearErr != nil {
			return clearErr
		}
		snapshot, err = fallbackAppSnapshot(statePath)
		if err != nil {
			return err
		}
		return printJSON(stdout, buildAppSnapshotPayload("state-fallback", false, configPath, statePath, controlSocketPath, snapshot))
	}
	return printJSON(stdout, buildAppSnapshotPayload("control-socket", true, configPath, statePath, controlSocketPath, snapshot))
}

func appReconnect(args []string, stdout io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("app reconnect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options, err := parsePathOptions(flags, args)
	if err != nil {
		return err
	}
	configPath, statePath, controlSocketPath, err := resolveAppPaths(options)
	if err != nil {
		return err
	}
	snapshot, err := appctl.RequestReconnect(controlSocketPath)
	if err != nil {
		if !errors.Is(err, appctl.ErrUnavailable) {
			return err
		}
		snapshot, err = fallbackAppSnapshot(statePath)
		if err != nil {
			return err
		}
		return printJSON(stdout, buildAppSnapshotPayload("state-fallback", false, configPath, statePath, controlSocketPath, snapshot))
	}
	return printJSON(stdout, buildAppSnapshotPayload("control-socket", true, configPath, statePath, controlSocketPath, snapshot))
}

func resolveAppPaths(options config.Options) (string, string, string, error) {
	configPath, statePath, err := config.ResolvePaths(options)
	if err != nil {
		return "", "", "", err
	}
	controlSocketPath, err := config.ResolveControlSocketPath(options)
	if err != nil {
		return "", "", "", err
	}
	return configPath, statePath, controlSocketPath, nil
}

func persistRuntimeToken(statePath string, token string) (state.State, error) {
	store := state.NewStore(statePath)
	return store.Update(func(value *state.State) error {
		if _, ensureErr := state.EnsureRuntimeID(value); ensureErr != nil {
			return ensureErr
		}
		value.RuntimeToken = strings.TrimSpace(token)
		value.PairingState = state.PairingStatePaired
		value.LastError = ""
		return nil
	})
}

func updatePersistedPairingState(statePath string, pairingState string, keepToken bool) (state.State, error) {
	store := state.NewStore(statePath)
	return store.Update(func(value *state.State) error {
		if _, ensureErr := state.EnsureRuntimeID(value); ensureErr != nil {
			return ensureErr
		}
		value.PairingState = pairingState
		if !keepToken {
			value.RuntimeToken = ""
		}
		return nil
	})
}

func fallbackAppSnapshot(statePath string) (appctl.Snapshot, error) {
	store := state.NewStore(statePath)
	current, err := store.Load()
	if err != nil {
		return appctl.Snapshot{}, err
	}
	return appctl.Snapshot{
		RuntimeID:       current.RuntimeID,
		PairingState:    current.PairingState,
		HasRuntimeToken: strings.TrimSpace(current.RuntimeToken) != "",
		LastGatewayURL:  current.LastGatewayURL,
		LastConnectedAt: current.LastConnectedAt,
		LastError:       current.LastError,
		Online:          false,
		ConnectionState: "offline",
		HelperPID:       0,
	}, nil
}

func buildAppSnapshotPayload(
	bridgeMode string,
	helperAvailable bool,
	configPath string,
	statePath string,
	controlSocketPath string,
	snapshot appctl.Snapshot,
) map[string]any {
	return map[string]any{
		"bridge_mode":         bridgeMode,
		"helper_available":    helperAvailable,
		"config_path":         configPath,
		"state_path":          statePath,
		"control_socket_path": controlSocketPath,
		"runtime_id":          snapshot.RuntimeID,
		"pairing_state":       snapshot.PairingState,
		"has_runtime_token":   snapshot.HasRuntimeToken,
		"last_gateway_url":    snapshot.LastGatewayURL,
		"last_connected_at":   snapshot.LastConnectedAt,
		"last_error":          snapshot.LastError,
		"online":              snapshot.Online,
		"connection_state":    snapshot.ConnectionState,
		"helper_pid":          snapshot.HelperPID,
	}
}
