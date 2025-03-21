// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2021-present Datadog, Inc.

package otlp

import (
	"fmt"

	"github.com/DataDog/datadog-agent/pkg/config"
	colConfig "go.opentelemetry.io/collector/config"
	"go.uber.org/multierr"
)

// getReceiverHost gets the receiver host for the OTLP endpoint in a given config.
func getReceiverHost(cfg config.Config) (receiverHost string) {
	// The default value for the trace Agent
	receiverHost = "localhost"

	// This is taken from pkg/trace/config.AgentConfig.applyDatadogConfig
	if cfg.IsSet("bind_host") || cfg.IsSet("apm_config.apm_non_local_traffic") {
		if cfg.IsSet("bind_host") {
			receiverHost = cfg.GetString("bind_host")
		}

		if cfg.IsSet("apm_config.apm_non_local_traffic") && cfg.GetBool("apm_config.apm_non_local_traffic") {
			receiverHost = "0.0.0.0"
		}
	} else if config.IsContainerized() {
		receiverHost = "0.0.0.0"
	}
	return
}

// isSetExperimentalPort checks if the experimental port config is set.
func isSetExperimentalPort(cfg config.Config) bool {
	return cfg.IsSet(config.ExperimentalOTLPHTTPPort) || cfg.IsSet(config.ExperimentalOTLPgRPCPort)
}

func isSetExperimental(cfg config.Config) bool {
	return isSetExperimentalPort(cfg)
}

func portToUint(v int) (port uint, err error) {
	if v < 0 || v > 65535 {
		err = fmt.Errorf("%d is out of [0, 65535] range", v)
	}
	port = uint(v)
	return
}

func fromExperimentalPortReceiverConfig(cfg config.Config, otlpConfig *colConfig.Map) error {
	var errs []error

	httpPort, err := portToUint(cfg.GetInt(config.ExperimentalOTLPHTTPPort))
	if err != nil {
		errs = append(errs, fmt.Errorf("HTTP port is invalid: %w", err))
	}

	gRPCPort, err := portToUint(cfg.GetInt(config.ExperimentalOTLPgRPCPort))
	if err != nil {
		errs = append(errs, fmt.Errorf("gRPC port is invalid: %w", err))
	}

	bindHost := getReceiverHost(cfg)

	if gRPCPort > 0 {
		otlpConfig.Set(
			buildKey("protocols", "grpc", "endpoint"),
			fmt.Sprintf("%s:%d", bindHost, gRPCPort),
		)
	}

	if httpPort > 0 {
		otlpConfig.Set(
			buildKey("protocols", "http", "endpoint"),
			fmt.Sprintf("%s:%d", bindHost, httpPort),
		)
	}

	return multierr.Combine(errs...)
}

// fromExperimentalConfig builds a PipelineConfig from the experimental configuration.
func fromExperimentalConfig(cfg config.Config) (PipelineConfig, error) {
	var errs []error
	otlpConfig := colConfig.NewMap()
	if isSetExperimentalPort(cfg) {
		err := fromExperimentalPortReceiverConfig(cfg, otlpConfig)
		if err != nil {
			errs = append(errs, fmt.Errorf("OTLP receiver port-based configuration is invalid: %w", err))
		}
	}

	tracePort, err := portToUint(cfg.GetInt(config.ExperimentalOTLPTracePort))
	if err != nil {
		errs = append(errs, fmt.Errorf("internal trace port is invalid: %w", err))
	}

	metricsEnabled := cfg.GetBool(config.ExperimentalOTLPMetricsEnabled)
	tracesEnabled := cfg.GetBool(config.ExperimentalOTLPTracesEnabled)
	if !metricsEnabled && !tracesEnabled {
		errs = append(errs, fmt.Errorf("at least one OTLP signal needs to be enabled"))
	}

	return PipelineConfig{
		OTLPReceiverConfig: otlpConfig.ToStringMap(),
		TracePort:          tracePort,
		MetricsEnabled:     metricsEnabled,
		TracesEnabled:      tracesEnabled,
	}, multierr.Combine(errs...)
}

// IsEnabled checks if OTLP pipeline is enabled in a given config.
func IsEnabled(cfg config.Config) bool {
	// TODO (AP-1267): Check stable config too
	return isSetExperimental(cfg)
}

// FromAgentConfig builds a pipeline configuration from an Agent configuration.
func FromAgentConfig(cfg config.Config) (PipelineConfig, error) {
	// TODO (AP-1267): Check stable config too
	return fromExperimentalConfig(cfg)
}
