package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strings"
	"time"

	"github.com/DataDog/agent-payload/process"
	cmdconfig "github.com/DataDog/datadog-agent/cmd/agent/common/commands/config"
	"github.com/DataDog/datadog-agent/cmd/manager"
	"github.com/DataDog/datadog-agent/cmd/process-agent/api"
	apiutil "github.com/DataDog/datadog-agent/pkg/api/util"
	ddconfig "github.com/DataDog/datadog-agent/pkg/config"
	"github.com/DataDog/datadog-agent/pkg/config/settings"
	settingshttp "github.com/DataDog/datadog-agent/pkg/config/settings/http"
	"github.com/DataDog/datadog-agent/pkg/metadata/host"
	"github.com/DataDog/datadog-agent/pkg/pidfile"
	"github.com/DataDog/datadog-agent/pkg/process/checks"
	"github.com/DataDog/datadog-agent/pkg/process/config"
	"github.com/DataDog/datadog-agent/pkg/process/statsd"
	"github.com/DataDog/datadog-agent/pkg/process/util"
	"github.com/DataDog/datadog-agent/pkg/tagger"
	"github.com/DataDog/datadog-agent/pkg/tagger/collectors"
	"github.com/DataDog/datadog-agent/pkg/tagger/local"
	"github.com/DataDog/datadog-agent/pkg/tagger/remote"
	"github.com/DataDog/datadog-agent/pkg/telemetry"
	ddutil "github.com/DataDog/datadog-agent/pkg/util"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"github.com/DataDog/datadog-agent/pkg/util/profiling"
	"github.com/DataDog/datadog-agent/pkg/workloadmeta"

	// register all workloadmeta collectors
	_ "github.com/DataDog/datadog-agent/pkg/workloadmeta/collectors"

	"github.com/spf13/cobra"
)

const loggerName ddconfig.LoggerName = "PROCESS"

var opts struct {
	configPath         string
	sysProbeConfigPath string
	pidfilePath        string
	debug              bool
	version            bool
	check              string
	info               bool
}

// version info sourced from build flags
var (
	Version   string
	GitCommit string
	GitBranch string
	BuildDate string
	GoVersion string
)

var (
	rootCmd = &cobra.Command{
		Run:          rootCmdRun,
		SilenceUsage: true,
	}

	configCommand = cmdconfig.Config(getSettingsClient)
)

func getSettingsClient() (settings.Client, error) {
	// Set up the config so we can get the port later
	// We set this up differently from the main process-agent because this way is quieter
	cfg := config.NewDefaultAgentConfig(false)
	if opts.configPath != "" {
		if err := config.LoadConfigIfExists(opts.configPath); err != nil {
			return nil, err
		}
	}
	err := cfg.LoadProcessYamlConfig(opts.configPath)
	if err != nil {
		return nil, err
	}

	httpClient := apiutil.GetClient(false)
	ipcAddress, err := ddconfig.GetIPCAddress()
	ipcAddressWithPort := fmt.Sprintf("http://%s:%d/config", ipcAddress, ddconfig.Datadog.GetInt("process_config.cmd_port"))
	if err != nil {
		return nil, err
	}
	settingsClient := settingshttp.NewClient(httpClient, ipcAddressWithPort, "process-agent")
	return settingsClient, nil
}

func init() {
	rootCmd.AddCommand(configCommand)
}

// fixDeprecatedFlags modifies os.Args so that non-posix flags are converted to posix flags
// it also displays a warning when a non-posix flag is found
func fixDeprecatedFlags() {
	deprecatedFlags := []string{
		// Global flags
		"-config", "-ddconfig", "-sysprobe-config", "-pid", "-info", "-version", "-check",
		// Windows flags
		"-install-service", "-uninstall-service", "-start-service", "-stop-service", "-foreground",
	}

	for i, arg := range os.Args {
		for _, f := range deprecatedFlags {
			if !strings.HasPrefix(arg, f) {
				continue
			}
			fmt.Printf("WARNING: `%s` argument is deprecated and will be removed in a future version. Please use `-%[1]s` instead.\n", f)
			os.Args[i] = "-" + os.Args[i]
		}
	}
}

// versionString returns the version information filled in at build time
func versionString(sep string) string {
	var buf bytes.Buffer

	if Version != "" {
		fmt.Fprintf(&buf, "Version: %s%s", Version, sep)
	}
	if GitCommit != "" {
		fmt.Fprintf(&buf, "Git hash: %s%s", GitCommit, sep)
	}
	if GitBranch != "" {
		fmt.Fprintf(&buf, "Git branch: %s%s", GitBranch, sep)
	}
	if BuildDate != "" {
		fmt.Fprintf(&buf, "Build date: %s%s", BuildDate, sep)
	}
	if GoVersion != "" {
		fmt.Fprintf(&buf, "Go Version: %s%s", GoVersion, sep)
	}

	return buf.String()
}

const (
	agent6DisabledMessage = `process-agent not enabled.
Set env var DD_PROCESS_AGENT_ENABLED=true or add
process_config:
  enabled: "true"
to your datadog.yaml file.
Exiting.`
)

func runAgent(exit chan struct{}) {
	if opts.version {
		fmt.Print(versionString("\n"))
		cleanupAndExit(0)
	}

	if err := ddutil.SetupCoreDump(); err != nil {
		log.Warnf("Can't setup core dumps: %v, core dumps might not be available after a crash", err)
	}

	if opts.check == "" && !opts.info && opts.pidfilePath != "" {
		err := pidfile.WritePID(opts.pidfilePath)
		if err != nil {
			log.Errorf("Error while writing PID file, exiting: %v", err)
			cleanupAndExit(1)
		}

		log.Infof("pid '%d' written to pid file '%s'", os.Getpid(), opts.pidfilePath)
		defer func() {
			// remove pidfile if set
			os.Remove(opts.pidfilePath)
		}()
	}

	cfg, err := config.NewAgentConfig(loggerName, opts.configPath, opts.sysProbeConfigPath)
	if err != nil {
		log.Criticalf("Error parsing config: %s", err)
		cleanupAndExit(1)
	}

	mainCtx, mainCancel := context.WithCancel(context.Background())
	defer mainCancel()
	err = manager.ConfigureAutoExit(mainCtx)
	if err != nil {
		log.Criticalf("Unable to configure auto-exit, err: %w", err)
		cleanupAndExit(1)
	}

	// Now that the logger is configured log host info
	hostInfo := host.GetStatusInformation()
	log.Infof("running on platform: %s", hostInfo.Platform)
	log.Infof("running version: %s", versionString(", "))

	// Tagger must be initialized after agent config has been setup
	var t tagger.Tagger
	if ddconfig.Datadog.GetBool("process_config.remote_tagger") {
		t = remote.NewTagger()
	} else {
		// Start workload metadata store before tagger
		workloadmeta.GetGlobalStore().Start(context.Background())

		t = local.NewTagger(collectors.DefaultCatalog)
	}
	tagger.SetDefaultTagger(t)
	tagger.Init()
	defer tagger.Stop() //nolint:errcheck

	err = initInfo(cfg)
	if err != nil {
		log.Criticalf("Error initializing info: %s", err)
		cleanupAndExit(1)
	}
	if err := statsd.Configure(cfg.StatsdHost, cfg.StatsdPort); err != nil {
		log.Criticalf("Error configuring statsd: %s", err)
		cleanupAndExit(1)
	}

	// Exit if agent is not enabled and we're not debugging a check.
	if !cfg.Enabled && opts.check == "" {
		log.Infof(agent6DisabledMessage)

		// a sleep is necessary to ensure that supervisor registers this process as "STARTED"
		// If the exit is "too quick", we enter a BACKOFF->FATAL loop even though this is an expected exit
		// http://supervisord.org/subprocess.html#process-states
		time.Sleep(5 * time.Second)
		return
	}

	// update docker socket path in info
	dockerSock, err := util.GetDockerSocketPath()
	if err != nil {
		log.Debugf("Docker is not available on this host")
	}
	// we shouldn't quit because docker is not required. If no docker docket is available,
	// we just pass down empty string
	updateDockerSocket(dockerSock)

	if cfg.ProfilingSettings != nil {
		if err := profiling.Start(*cfg.ProfilingSettings); err != nil {
			log.Warnf("failed to enable profiling: %s", err)
		} else {
			log.Info("start profiling process-agent")
		}
		defer profiling.Stop()
	}

	log.Debug("Running process-agent with DEBUG logging enabled")
	if opts.check != "" {
		err := debugCheckResults(cfg, opts.check)
		if err != nil {
			fmt.Println(err)
			cleanupAndExit(1)
		} else {
			cleanupAndExit(0)
		}
		return
	}

	if opts.info {
		// using the debug port to get info to work
		url := fmt.Sprintf("http://localhost:%d/debug/vars", cfg.ProcessExpVarPort)
		if err := Info(os.Stdout, cfg, url); err != nil {
			cleanupAndExit(1)
		}
		return
	}

	// Run a profile & telemetry server.
	go func() {
		if ddconfig.Datadog.GetBool("telemetry.enabled") {
			http.Handle("/telemetry", telemetry.Handler())
		}
		err := http.ListenAndServe(fmt.Sprintf("localhost:%d", cfg.ProcessExpVarPort), nil)
		if err != nil && err != http.ErrServerClosed {
			log.Errorf("Error creating expvar server on port %v: %v", cfg.ProcessExpVarPort, err)
		}
	}()

	// Run API server
	err = api.StartServer()
	if err != nil {
		_ = log.Error(err)
	}

	cl, err := NewCollector(cfg)
	if err != nil {
		log.Criticalf("Error creating collector: %s", err)
		cleanupAndExit(1)
		return
	}
	if err := cl.run(exit); err != nil {
		log.Criticalf("Error starting collector: %s", err)
		os.Exit(1)
		return
	}

	for range exit {
	}
}

func debugCheckResults(cfg *config.AgentConfig, check string) error {
	sysInfo, err := checks.CollectSystemInfo(cfg)
	if err != nil {
		return err
	}

	// Connections check requires process-check to have occurred first (for process creation ts),
	if check == checks.Connections.Name() {
		checks.Process.Init(cfg, sysInfo)
		checks.Process.Run(cfg, 0) //nolint:errcheck
	}

	names := make([]string, 0, len(checks.All))
	for _, ch := range checks.All {
		names = append(names, ch.Name())

		if ch.Name() == check {
			ch.Init(cfg, sysInfo)
			return runCheck(cfg, ch)
		}

		withRealTime, ok := ch.(checks.CheckWithRealTime)
		if ok && withRealTime.RealTimeName() == check {
			withRealTime.Init(cfg, sysInfo)
			return runCheckAsRealTime(cfg, withRealTime)
		}
	}
	return fmt.Errorf("invalid check '%s', choose from: %v", check, names)
}

func runCheck(cfg *config.AgentConfig, ch checks.Check) error {
	// Run the check once to prime the cache.
	if _, err := ch.Run(cfg, 0); err != nil {
		return fmt.Errorf("collection error: %s", err)
	}

	time.Sleep(1 * time.Second)

	printResultsBanner(ch.Name())

	msgs, err := ch.Run(cfg, 1)
	if err != nil {
		return fmt.Errorf("collection error: %s", err)
	}
	return printResults(msgs)
}

func runCheckAsRealTime(cfg *config.AgentConfig, ch checks.CheckWithRealTime) error {
	options := checks.RunOptions{
		RunStandard: true,
		RunRealTime: true,
	}
	var (
		groupID     int32
		nextGroupID = func() int32 {
			groupID++
			return groupID
		}
	)

	// We need to run the check twice in order to initialize the stats
	// Rate calculations rely on having two datapoints
	if _, err := ch.RunWithOptions(cfg, nextGroupID, options); err != nil {
		return fmt.Errorf("collection error: %s", err)
	}

	time.Sleep(1 * time.Second)

	printResultsBanner(ch.RealTimeName())

	run, err := ch.RunWithOptions(cfg, nextGroupID, options)
	if err != nil {
		return fmt.Errorf("collection error: %s", err)
	}

	return printResults(run.RealTime)
}

func printResultsBanner(name string) {
	fmt.Printf("-----------------------------\n\n")
	fmt.Printf("\nResults for check %s\n", name)
	fmt.Printf("-----------------------------\n\n")
}

func printResults(msgs []process.MessageBody) error {
	for _, m := range msgs {
		b, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal error: %s", err)
		}
		fmt.Println(string(b))
	}
	return nil
}

// cleanupAndExit cleans all resources allocated by the agent before calling
// os.Exit
func cleanupAndExit(status int) {
	// remove pidfile if set
	if opts.pidfilePath != "" {
		if _, err := os.Stat(opts.pidfilePath); err == nil {
			os.Remove(opts.pidfilePath)
		}
	}

	os.Exit(status)
}
