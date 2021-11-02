// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// +build linux windows darwin
// I don't think windows and darwin can actually be docker hosts
// but keeping it this way for build consistency (for now)

package util

import (
	"context"

	"github.com/DataDog/datadog-agent/pkg/config"
	"github.com/DataDog/datadog-agent/pkg/util/hostname"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

func getContainerHostname(ctx context.Context) string {
	if config.IsFeaturePresent(config.Kubernetes) {
		// Cluster-agent logic: Kube apiserver
		if name, err := hostname.GetHostname("kube_apiserver", ctx, nil); err == nil {
			return name
		} else {
			log.Debug(err.Error())
		}
	}

	// Node-agent logic: docker or kubelet
	if config.IsFeaturePresent(config.Docker) {
		log.Debug("GetHostname trying Docker API...")
		if name, err := hostname.GetHostname("docker", ctx, nil); err == nil {
			return name
		} else {
			log.Debug(err.Error())
		}
	}

	if config.IsFeaturePresent(config.Kubernetes) {
		if name, err := hostname.GetHostname("kubelet", ctx, nil); err == nil {
			return name
		} else {
			log.Debug(err.Error())
		}
	}

	return ""
}
