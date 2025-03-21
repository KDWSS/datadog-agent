// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package generic

import (
	"fmt"
	"time"

	"github.com/DataDog/datadog-agent/pkg/aggregator"
	"github.com/DataDog/datadog-agent/pkg/security/log"
	"github.com/DataDog/datadog-agent/pkg/tagger"
	"github.com/DataDog/datadog-agent/pkg/tagger/collectors"
	"github.com/DataDog/datadog-agent/pkg/util"
	"github.com/DataDog/datadog-agent/pkg/util/containers"
	"github.com/DataDog/datadog-agent/pkg/util/containers/v2/metrics"
	"github.com/DataDog/datadog-agent/pkg/workloadmeta"
)

// Processor contains the core logic of the generic check, allowing reusability
type Processor struct {
	metricsProvider metrics.Provider
	ctrLister       ContainerLister
	metricsAdapter  MetricsAdapter
	ctrFilter       *containers.Filter
}

// NewProcessor creates a new processor
func NewProcessor(provider metrics.Provider, lister ContainerLister, adapter MetricsAdapter, filter *containers.Filter) Processor {
	return Processor{
		metricsProvider: provider,
		ctrLister:       lister,
		metricsAdapter:  adapter,
		ctrFilter:       filter,
	}
}

// Run executes the check
func (p *Processor) Run(sender aggregator.Sender, cacheValidity time.Duration) error {
	allContainers, err := p.ctrLister.List()
	if err != nil {
		return fmt.Errorf("cannot list containers from metadata store, container metrics will be missing, err: %w", err)
	}

	collectorsCache := make(map[workloadmeta.ContainerRuntime]metrics.Collector)
	getCollector := func(runtime workloadmeta.ContainerRuntime) metrics.Collector {
		if collector, found := collectorsCache[runtime]; found {
			return collector
		}

		collector := p.metricsProvider.GetCollector(string(runtime))
		if collector != nil {
			collectorsCache[runtime] = collector
		}
		return collector
	}

	for _, container := range allContainers {
		// We surely won't get stats for not running containers
		if !container.State.Running {
			continue
		}

		if p.ctrFilter.IsExcluded(container.Name, container.Image.Name, container.Labels["io.kubernetes.pod.namespace"]) {
			log.Tracef("Container excluded due to filter, name: %s - image: %s - namespace: %s", container.Name, container.Image.Name, container.Labels["io.kubernetes.pod.namespace"])
			continue
		}

		entityID := containers.BuildTaggerEntityName(container.ID)
		tags, err := tagger.Tag(entityID, collectors.HighCardinality)
		if err != nil {
			log.Errorf("Could not collect tags for container %s: %s", container.ID[:12], err)
			continue
		}
		tags = p.metricsAdapter.AdaptTags(tags, container)

		collector := getCollector(container.Runtime)
		if collector == nil {
			log.Warnf("Collector not found for container: %v, metrics will ne missing", container)
			continue
		}

		containerStats, err := collector.GetContainerStats(container.ID, cacheValidity)
		if err != nil {
			log.Debugf("Container stats for: %v not available through collector: %s", container, collector.ID())
			continue
		}

		if err := p.processContainer(sender, tags, container, containerStats); err != nil {
			log.Debugf("Generating metrics for container: %v failed, metrics may be missing, err: %w", container, err)
			continue
		}

		// TODO: Implement container stats. We currently don't have enough information from Metadata service to do it.
	}

	sender.Commit()
	return nil
}

func (p *Processor) processContainer(sender aggregator.Sender, tags []string, container *workloadmeta.Container, containerStats *metrics.ContainerStats) error {
	if uptime := time.Since(container.State.StartedAt); uptime > 0 {
		p.sendMetric(sender.Gauge, "container.uptime", util.Float64Ptr(uptime.Seconds()), tags)
	}

	if containerStats.CPU != nil {
		p.sendMetric(sender.Rate, "container.cpu.usage", containerStats.CPU.Total, tags)
		p.sendMetric(sender.Rate, "container.cpu.user", containerStats.CPU.User, tags)
		p.sendMetric(sender.Rate, "container.cpu.system", containerStats.CPU.System, tags)
		p.sendMetric(sender.Rate, "container.cpu.throttled.time", containerStats.CPU.ThrottledTime, tags)
		p.sendMetric(sender.Rate, "container.cpu.throttled.periods", containerStats.CPU.ThrottledPeriods, tags)
		p.sendMetric(sender.Gauge, "container.cpu.shares", containerStats.CPU.Shares, tags)
		// Convert CPU Limit to nanoseconds to allow easy percentage computation in the App.
		if containerStats.CPU.Limit != nil {
			p.sendMetric(sender.Gauge, "container.cpu.limit", util.Float64Ptr(*containerStats.CPU.Limit*float64(time.Second/100)), tags)
		}
	}

	if containerStats.Memory != nil {
		p.sendMetric(sender.Gauge, "container.memory.usage", containerStats.Memory.UsageTotal, tags)
		p.sendMetric(sender.Gauge, "container.memory.kernel", containerStats.Memory.KernelMemory, tags)
		p.sendMetric(sender.Gauge, "container.memory.limit", containerStats.Memory.Limit, tags)
		p.sendMetric(sender.Gauge, "container.memory.soft_limit", containerStats.Memory.Softlimit, tags)
		p.sendMetric(sender.Gauge, "container.memory.rss", containerStats.Memory.RSS, tags)
		p.sendMetric(sender.Gauge, "container.memory.cache", containerStats.Memory.Cache, tags)
		p.sendMetric(sender.Gauge, "container.memory.swap", containerStats.Memory.Swap, tags)
		p.sendMetric(sender.Gauge, "container.memory.oomevents", containerStats.Memory.OOMEvents, tags)
		p.sendMetric(sender.Gauge, "container.memory.working_set", containerStats.Memory.PrivateWorkingSet, tags)
		p.sendMetric(sender.Gauge, "container.memory.commit", containerStats.Memory.CommitBytes, tags)
		p.sendMetric(sender.Gauge, "container.memory.commit.peak", containerStats.Memory.CommitPeakBytes, tags)
	}

	if containerStats.IO != nil {
		for deviceName, deviceStats := range containerStats.IO.Devices {
			deviceTags := extraTags(tags, "device_name:"+deviceName)
			p.sendMetric(sender.Rate, "container.io.read", deviceStats.ReadBytes, deviceTags)
			p.sendMetric(sender.Rate, "container.io.read.operations", deviceStats.ReadOperations, deviceTags)
			p.sendMetric(sender.Rate, "container.io.write", deviceStats.WriteBytes, deviceTags)
			p.sendMetric(sender.Rate, "container.io.write.operations", deviceStats.WriteOperations, deviceTags)
		}

		if len(containerStats.IO.Devices) == 0 {
			p.sendMetric(sender.Rate, "container.io.read", containerStats.IO.ReadBytes, tags)
			p.sendMetric(sender.Rate, "container.io.read.operations", containerStats.IO.ReadOperations, tags)
			p.sendMetric(sender.Rate, "container.io.write", containerStats.IO.WriteBytes, tags)
			p.sendMetric(sender.Rate, "container.io.write.operations", containerStats.IO.WriteOperations, tags)
		}
	}

	if containerStats.PID != nil {
		p.sendMetric(sender.Gauge, "container.pid.thread_count", containerStats.PID.ThreadCount, tags)
		p.sendMetric(sender.Gauge, "container.pid.thread_limit", containerStats.PID.ThreadLimit, tags)
	}

	return nil
}

func (p *Processor) sendMetric(senderFunc func(string, float64, string, []string), metricName string, value *float64, tags []string) {
	if value == nil {
		return
	}

	metricName, val := p.metricsAdapter.AdaptMetrics(metricName, *value)
	senderFunc(metricName, val, "", tags)
}

func extraTags(tags []string, extraTags ...string) []string {
	finalTags := make([]string, 0, len(tags)+len(extraTags))
	finalTags = append(finalTags, tags...)
	finalTags = append(finalTags, extraTags...)
	return finalTags
}
