// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package generic

import (
	yaml "gopkg.in/yaml.v2"

	"github.com/DataDog/datadog-agent/pkg/aggregator"
	"github.com/DataDog/datadog-agent/pkg/autodiscovery/integration"
	"github.com/DataDog/datadog-agent/pkg/collector/check"
	core "github.com/DataDog/datadog-agent/pkg/collector/corechecks"
	"github.com/DataDog/datadog-agent/pkg/util/containers"
	"github.com/DataDog/datadog-agent/pkg/util/containers/v2/metrics"
)

const (
	genericContainerCheckName = "container"
)

// ContainerConfig holds the check configuration
type ContainerConfig struct{}

// Parse parses the container check config and set default values
func (c *ContainerConfig) Parse(data []byte) error {
	return yaml.Unmarshal(data, c)
}

// ContainerCheck generates metrics for all containers
type ContainerCheck struct {
	core.CheckBase
	instance  *ContainerConfig
	processor Processor
}

func init() {
	core.RegisterCheck("container", ContainerCheckFactory)
}

// ContainerCheckFactory is exported for integration testing
func ContainerCheckFactory() check.Check {
	return &ContainerCheck{
		CheckBase: core.NewCheckBase(genericContainerCheckName),
		instance:  &ContainerConfig{},
	}
}

// Configure parses the check configuration and init the check
func (c *ContainerCheck) Configure(config, initConfig integration.Data, source string) error {
	err := c.CommonConfigure(config, source)
	if err != nil {
		return err
	}

	filter, err := containers.GetSharedMetricFilter()
	if err != nil {
		return err
	}

	c.processor = NewProcessor(metrics.GetProvider(), MetadataContainerLister{}, GenericMetricsAdapter{}, filter)
	return c.instance.Parse(config)
}

// Run executes the check
func (c *ContainerCheck) Run() error {
	sender, err := aggregator.GetSender(c.ID())
	if err != nil {
		return err
	}

	return c.processor.Run(sender, c.Interval()/2)
}
