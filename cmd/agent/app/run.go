// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	_ "expvar" // Blank import used because this isn't directly used in this file

	_ "net/http/pprof" // Blank import used because this isn't directly used in this file

	"github.com/DataDog/datadog-agent/cmd/agent/api"
	"github.com/DataDog/datadog-agent/cmd/agent/clcrunnerapi"
	"github.com/DataDog/datadog-agent/cmd/agent/common"
	"github.com/DataDog/datadog-agent/cmd/agent/common/misconfig"
	"github.com/DataDog/datadog-agent/cmd/agent/common/signals"
	"github.com/DataDog/datadog-agent/cmd/agent/gui"
	"github.com/DataDog/datadog-agent/cmd/manager"
	"github.com/DataDog/datadog-agent/pkg/aggregator"
	"github.com/DataDog/datadog-agent/pkg/api/healthprobe"
	"github.com/DataDog/datadog-agent/pkg/autodiscovery"
	"github.com/DataDog/datadog-agent/pkg/collector/corechecks/embed/jmx"
	"github.com/DataDog/datadog-agent/pkg/config"
	remoteconfig "github.com/DataDog/datadog-agent/pkg/config/remote/service"
	"github.com/DataDog/datadog-agent/pkg/config/settings"
	"github.com/DataDog/datadog-agent/pkg/dogstatsd"
	"github.com/DataDog/datadog-agent/pkg/epforwarder"
	"github.com/DataDog/datadog-agent/pkg/forwarder"
	"github.com/DataDog/datadog-agent/pkg/logs"
	"github.com/DataDog/datadog-agent/pkg/metadata"
	"github.com/DataDog/datadog-agent/pkg/metadata/host"
	orchcfg "github.com/DataDog/datadog-agent/pkg/orchestrator/config"
	"github.com/DataDog/datadog-agent/pkg/otlp"
	"github.com/DataDog/datadog-agent/pkg/pidfile"
	"github.com/DataDog/datadog-agent/pkg/serializer"
	"github.com/DataDog/datadog-agent/pkg/snmp/traps"
	"github.com/DataDog/datadog-agent/pkg/status/health"
	"github.com/DataDog/datadog-agent/pkg/telemetry"
	"github.com/DataDog/datadog-agent/pkg/util"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"github.com/DataDog/datadog-agent/pkg/version"
	"github.com/spf13/cobra"
	"gopkg.in/DataDog/dd-trace-go.v1/profiler"

	// runtime init routines
	ddruntime "github.com/DataDog/datadog-agent/pkg/runtime"

	// register core checks
	_ "github.com/DataDog/datadog-agent/pkg/collector/corechecks/cluster/ksm"
	_ "github.com/DataDog/datadog-agent/pkg/collector/corechecks/cluster/kubernetesapiserver"
	_ "github.com/DataDog/datadog-agent/pkg/collector/corechecks/cluster/orchestrator"
	_ "github.com/DataDog/datadog-agent/pkg/collector/corechecks/containers/containerd"
	_ "github.com/DataDog/datadog-agent/pkg/collector/corechecks/containers/cri"
	_ "github.com/DataDog/datadog-agent/pkg/collector/corechecks/containers/docker"
	_ "github.com/DataDog/datadog-agent/pkg/collector/corechecks/containers/generic"
	_ "github.com/DataDog/datadog-agent/pkg/collector/corechecks/ebpf"
	_ "github.com/DataDog/datadog-agent/pkg/collector/corechecks/embed"
	_ "github.com/DataDog/datadog-agent/pkg/collector/corechecks/net"
	_ "github.com/DataDog/datadog-agent/pkg/collector/corechecks/nvidia/jetson"
	_ "github.com/DataDog/datadog-agent/pkg/collector/corechecks/snmp"
	_ "github.com/DataDog/datadog-agent/pkg/collector/corechecks/system/cpu"
	_ "github.com/DataDog/datadog-agent/pkg/collector/corechecks/system/disk"
	_ "github.com/DataDog/datadog-agent/pkg/collector/corechecks/system/filehandles"
	_ "github.com/DataDog/datadog-agent/pkg/collector/corechecks/system/memory"
	_ "github.com/DataDog/datadog-agent/pkg/collector/corechecks/system/uptime"
	_ "github.com/DataDog/datadog-agent/pkg/collector/corechecks/system/winproc"
	_ "github.com/DataDog/datadog-agent/pkg/collector/corechecks/systemd"

	// register metadata providers
	_ "github.com/DataDog/datadog-agent/pkg/collector/metadata"
	_ "github.com/DataDog/datadog-agent/pkg/metadata"
)

var (
	// flags variables
	pidfilePath string

	orchestratorForwarder  *forwarder.DefaultForwarder
	eventPlatformForwarder epforwarder.EventPlatformForwarder
	configService          *remoteconfig.Service

	runCmd = &cobra.Command{
		Use:   "run",
		Short: "Run the Agent",
		Long:  `Runs the agent in the foreground`,
		RunE:  run,
	}
)

func init() {
	// attach the command to the root
	AgentCmd.AddCommand(runCmd)

	// local flags
	runCmd.Flags().StringVarP(&pidfilePath, "pidfile", "p", "", "path to the pidfile")
}

// Start the main loop
func run(cmd *cobra.Command, args []string) error {
	defer func() {
		StopAgent()
	}()

	// prepare go runtime
	ddruntime.SetMaxProcs()

	// Setup a channel to catch OS signals
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)

	// Make a channel to exit the function
	stopCh := make(chan error)

	go func() {
		// Set up the signals async so we can Start the agent
		select {
		case <-signals.Stopper:
			log.Info("Received stop command, shutting down...")
			stopCh <- nil
		case <-signals.ErrorStopper:
			log.Critical("The Agent has encountered an error, shutting down...")
			stopCh <- fmt.Errorf("shutting down because of an error")
		case sig := <-signalCh:
			log.Infof("Received signal '%s', shutting down...", sig)
			stopCh <- nil
		}
	}()

	// By default systemd redirects the stdout to journald. When journald is stopped or crashes we receive a SIGPIPE signal.
	// Go ignores SIGPIPE signals unless it is when stdout or stdout is closed, in this case the agent is stopped.
	// We never want the agent to stop upon receiving SIGPIPE, so we intercept the SIGPIPE signals and just discard them.
	sigpipeCh := make(chan os.Signal, 1)
	signal.Notify(sigpipeCh, syscall.SIGPIPE)
	go func() {
		for range sigpipeCh {
			// do nothing
		}
	}()

	if err := StartAgent(); err != nil {
		return err
	}

	select {
	case err := <-stopCh:
		return err
	}
}

// StartAgent Initializes the agent process
func StartAgent() error {
	var (
		err            error
		configSetupErr error
		loggerSetupErr error
	)

	// Main context passed to components
	common.MainCtx, common.MainCtxCancel = context.WithCancel(context.Background())

	// Global Agent configuration
	configSetupErr = common.SetupConfig(confFilePath)

	// Setup logger
	if runtime.GOOS != "android" {
		syslogURI := config.GetSyslogURI()
		logFile := config.Datadog.GetString("log_file")
		if logFile == "" {
			logFile = common.DefaultLogFile
		}

		jmxLogFile := config.Datadog.GetString("jmx_log_file")
		if jmxLogFile == "" {
			jmxLogFile = common.DefaultJmxLogFile
		}

		if config.Datadog.GetBool("disable_file_logging") {
			// this will prevent any logging on file
			logFile = ""
			jmxLogFile = ""
		}

		loggerSetupErr = config.SetupLogger(
			loggerName,
			config.Datadog.GetString("log_level"),
			logFile,
			syslogURI,
			config.Datadog.GetBool("syslog_rfc"),
			config.Datadog.GetBool("log_to_console"),
			config.Datadog.GetBool("log_format_json"),
		)

		// Setup JMX logger
		if loggerSetupErr == nil {
			loggerSetupErr = config.SetupJMXLogger(
				jmxLoggerName,
				config.Datadog.GetString("log_level"),
				jmxLogFile,
				syslogURI,
				config.Datadog.GetBool("syslog_rfc"),
				config.Datadog.GetBool("log_to_console"),
				config.Datadog.GetBool("log_format_json"),
			)
		}

	} else {
		loggerSetupErr = config.SetupLogger(
			loggerName,
			config.Datadog.GetString("log_level"),
			"", // no log file on android
			"", // no syslog on android,
			false,
			true,  // always log to console
			false, // not in json
		)

		// Setup JMX logger
		if loggerSetupErr == nil {
			loggerSetupErr = config.SetupJMXLogger(
				jmxLoggerName,
				config.Datadog.GetString("log_level"),
				"", // no log file on android
				"", // no syslog on android,
				false,
				true,  // always log to console
				false, // not in json
			)
		}
	}

	if configSetupErr != nil {
		log.Errorf("Failed to setup config %v", configSetupErr)
		return fmt.Errorf("unable to set up global agent configuration: %v", configSetupErr)
	}

	if loggerSetupErr != nil {
		return fmt.Errorf("Error while setting up logging, exiting: %v", loggerSetupErr)
	}

	log.Infof("Starting Datadog Agent v%v", version.AgentVersion)

	if err := util.SetupCoreDump(); err != nil {
		log.Warnf("Can't setup core dumps: %v, core dumps might not be available after a crash", err)
	}

	// init settings that can be changed at runtime
	if err := initRuntimeSettings(); err != nil {
		log.Warnf("Can't initiliaze the runtime settings: %v", err)
	}

	// Setup Internal Profiling
	if v := config.Datadog.GetInt("internal_profiling.block_profile_rate"); v > 0 {
		if err := settings.SetRuntimeSetting("runtime_block_profile_rate", v); err != nil {
			log.Errorf("Error setting block profile rate: %v", err)
		}
	}
	if v := config.Datadog.GetInt("internal_profiling.mutex_profile_fraction"); v > 0 {
		if err := settings.SetRuntimeSetting("runtime_mutex_profile_fraction", v); err != nil {
			log.Errorf("Error mutex profile fraction: %v", err)
		}
	}
	if config.Datadog.GetBool("internal_profiling.enabled") {
		err := settings.SetRuntimeSetting("internal_profiling", true)
		if err != nil {
			log.Errorf("Error starting profiler: %v", err)
		}
	}

	// Setup expvar server
	telemetryHandler := telemetry.Handler()
	expvarPort := config.Datadog.GetString("expvar_port")
	if config.Datadog.GetBool("telemetry.enabled") {
		http.Handle("/telemetry", telemetryHandler)
	}
	go func() {
		err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%s", expvarPort), http.DefaultServeMux)
		if err != nil && err != http.ErrServerClosed {
			log.Errorf("Error creating expvar server on port %v: %v", expvarPort, err)
		}
	}()

	// Setup healthcheck port
	healthPort := config.Datadog.GetInt("health_port")
	if healthPort > 0 {
		err := healthprobe.Serve(common.MainCtx, healthPort)
		if err != nil {
			return log.Errorf("Error starting health port, exiting: %v", err)
		}
		log.Debugf("Health check listening on port %d", healthPort)
	}

	if pidfilePath != "" {
		err = pidfile.WritePID(pidfilePath)
		if err != nil {
			return log.Errorf("Error while writing PID file, exiting: %v", err)
		}
		log.Infof("pid '%d' written to pid file '%s'", os.Getpid(), pidfilePath)
	}

	err = manager.ConfigureAutoExit(common.MainCtx)
	if err != nil {
		return log.Errorf("Unable to configure auto-exit, err: %w", err)
	}

	hostname, err := util.GetHostname(context.TODO())
	if err != nil {
		return log.Errorf("Error while getting hostname, exiting: %v", err)
	}
	log.Infof("Hostname is: %s", hostname)

	// HACK: init host metadata module (CPU) early to avoid any
	//       COM threading model conflict with the python checks
	err = host.InitHostMetadata()
	if err != nil {
		log.Errorf("Unable to initialize host metadata: %v", err)
	}

	// start remote configuration management
	if config.Datadog.GetBool("remote_configuration.enabled") {
		opts := remoteconfig.Opts{}
		configService, err = remoteconfig.NewService(opts)
		if err != nil {
			log.Errorf("Failed to initialize config management service: %s", err)
		} else if err := configService.Start(context.Background()); err != nil {
			log.Errorf("Failed to start config management service: %s", err)
		}
	}

	// start the cmd HTTP server
	if runtime.GOOS != "android" {
		if err = api.StartServer(configService); err != nil {
			return log.Errorf("Error while starting api server, exiting: %v", err)
		}
	}

	// start clc runner server
	// only start when the cluster agent is enabled and a cluster check runner host is enabled
	if config.Datadog.GetBool("cluster_agent.enabled") && config.Datadog.GetBool("clc_runner_enabled") {
		if err = clcrunnerapi.StartCLCRunnerServer(map[string]http.Handler{
			"/telemetry": telemetryHandler,
		}); err != nil {
			return log.Errorf("Error while starting clc runner api server, exiting: %v", err)
		}
	}

	// start the GUI server
	guiPort := config.Datadog.GetString("GUI_port")
	if guiPort == "-1" {
		log.Infof("GUI server port -1 specified: not starting the GUI.")
	} else if err = gui.StartGUIServer(guiPort); err != nil {
		log.Errorf("Error while starting GUI: %v", err)
	}

	// setup the forwarder
	keysPerDomain, err := config.GetMultipleEndpoints()
	if err != nil {
		log.Error("Misconfiguration of agent endpoints: ", err)
	}

	// Enable core agent specific features like persistence-to-disk
	options := forwarder.NewOptions(keysPerDomain)
	options.EnabledFeatures = forwarder.SetFeature(options.EnabledFeatures, forwarder.CoreFeatures)

	common.Forwarder = forwarder.NewDefaultForwarder(options)
	log.Debugf("Starting forwarder")
	common.Forwarder.Start() //nolint:errcheck
	log.Debugf("Forwarder started")

	// setup the orchestrator forwarder (only on cluster check runners)
	orchestratorForwarder = orchcfg.NewOrchestratorForwarder()
	if orchestratorForwarder != nil {
		orchestratorForwarder.Start() //nolint:errcheck
	}

	eventPlatformForwarder = epforwarder.NewEventPlatformForwarder()
	eventPlatformForwarder.Start()

	// setup the aggregator
	s := serializer.NewSerializer(common.Forwarder, orchestratorForwarder)
	agg := aggregator.InitAggregator(s, eventPlatformForwarder, hostname)
	agg.AddAgentStartupTelemetry(version.AgentVersion)

	// start dogstatsd
	if config.Datadog.GetBool("use_dogstatsd") {
		var err error
		common.DSD, err = dogstatsd.NewServer(agg, nil)
		if err != nil {
			log.Errorf("Could not start dogstatsd: %s", err)
		}
	}
	log.Debugf("statsd started")

	// Start OTLP intake
	if otlp.IsEnabled(config.Datadog) {
		var err error
		common.OTLP, err = otlp.BuildAndStart(common.MainCtx, config.Datadog, s)
		if err != nil {
			log.Errorf("Could not start OTLP: %s", err)
		}
	}
	log.Debug("OTLP pipeline started")

	// Start SNMP trap server
	if traps.IsEnabled() {
		if config.Datadog.GetBool("logs_enabled") {
			err = traps.StartServer()
			if err != nil {
				log.Errorf("Failed to start snmp-traps server: %s", err)
			}
		} else {
			log.Warn(
				"snmp-traps server did not start, as log collection is disabled. " +
					"Please enable log collection to collect and forward traps.",
			)
		}
	}

	// start logs-agent
	if config.Datadog.GetBool("logs_enabled") || config.Datadog.GetBool("log_enabled") {
		if config.Datadog.GetBool("log_enabled") {
			log.Warn(`"log_enabled" is deprecated, use "logs_enabled" instead`)
		}
		if err := logs.Start(func() *autodiscovery.AutoConfig { return common.AC }); err != nil {
			log.Error("Could not start logs-agent: ", err)
		}
	} else {
		log.Info("logs-agent disabled")
	}

	if err = common.SetupSystemProbeConfig(sysProbeConfFilePath); err != nil {
		log.Infof("System probe config not found, disabling pulling system probe info in the status page: %v", err)
	}

	// Detect Cloud Provider
	go util.DetectCloudProvider(context.Background())

	// Append version and timestamp to version history log file if this Agent is different than the last run version
	util.LogVersionHistory()

	// create and setup the Autoconfig instance
	common.LoadComponents(config.Datadog.GetString("confd_path"))
	// start the autoconfig, this will immediately run any configured check
	common.StartAutoConfig()

	// check for common misconfigurations and report them to log
	misconfig.ToLog()

	// setup the metadata collector
	common.MetadataScheduler = metadata.NewScheduler(s)
	if err := metadata.SetupMetadataCollection(common.MetadataScheduler, metadata.AllDefaultCollectors); err != nil {
		return err
	}

	if config.Datadog.GetBool("inventories_enabled") {
		if err := metadata.SetupInventories(common.MetadataScheduler, common.AC, common.Coll); err != nil {
			return err
		}
	}

	// start dependent services
	go startDependentServices()

	return nil
}

// StopAgent Tears down the agent process
func StopAgent() {
	// retrieve the agent health before stopping the components
	// GetReadyNonBlocking has a 100ms timeout to avoid blocking
	health, err := health.GetReadyNonBlocking()
	if err != nil {
		log.Warnf("Agent health unknown: %s", err)
	} else if len(health.Unhealthy) > 0 {
		log.Warnf("Some components were unhealthy: %v", health.Unhealthy)
	}

	// gracefully shut down any component
	common.MainCtxCancel()

	if common.DSD != nil {
		common.DSD.Stop()
	}
	if common.OTLP != nil {
		common.OTLP.Stop()
	}
	if common.AC != nil {
		common.AC.Stop()
	}
	if common.MetadataScheduler != nil {
		common.MetadataScheduler.Stop()
	}
	traps.StopServer()
	api.StopServer()
	clcrunnerapi.StopCLCRunnerServer()
	jmx.StopJmxfetch()
	aggregator.StopDefaultAggregator()
	if common.Forwarder != nil {
		common.Forwarder.Stop()
	}
	if orchestratorForwarder != nil {
		orchestratorForwarder.Stop()
	}
	if eventPlatformForwarder != nil {
		eventPlatformForwarder.Stop()
	}
	logs.Stop()
	gui.StopGUIServer()
	profiler.Stop()

	os.Remove(pidfilePath)
	log.Info("See ya!")
	log.Flush()
}
