// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package clustername

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/DataDog/datadog-agent/pkg/config"
	"github.com/DataDog/datadog-agent/pkg/util/azure"
	"github.com/DataDog/datadog-agent/pkg/util/cache"
	"github.com/DataDog/datadog-agent/pkg/util/clusteragent"
	"github.com/DataDog/datadog-agent/pkg/util/ec2"
	"github.com/DataDog/datadog-agent/pkg/util/gce"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes/hostinfo"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

const (
	clusterIDEnv = "DD_ORCHESTRATOR_CLUSTER_ID"
)

var (
	// validClusterName matches exactly the same naming rule as the one enforced by GKE:
	// https://cloud.google.com/kubernetes-engine/docs/reference/rest/v1beta1/projects.locations.clusters#Cluster.FIELDS.name
	// The cluster name can be up to 40 characters with the following restrictions:
	// * Lowercase letters, numbers, dots and hyphens only.
	// * Must start with a letter.
	// * Must end with a number or a letter.
	// * Must be a valid FQDN (without trailing period)
	validClusterName = regexp.MustCompile(`^([a-z]([a-z0-9\-]*[a-z0-9])?\.)*([a-z]([a-z0-9\-]*[a-z0-9])?)$`)
)

type clusterNameData struct {
	clusterName string
	initDone    bool
	mutex       sync.Mutex
}

// Provider is a generic function to grab the clustername and return it
type Provider func(context.Context) (string, error)

// ProviderCatalog holds all the various kinds of clustername providers
var ProviderCatalog map[string]Provider

func newClusterNameData() *clusterNameData {
	return &clusterNameData{}
}

var defaultClusterNameData *clusterNameData

func init() {
	defaultClusterNameData = newClusterNameData()
	ProviderCatalog = map[string]Provider{
		"gce":   gce.GetClusterName,
		"azure": azure.GetClusterName,
		"ec2":   ec2.GetClusterName,
	}
}

func getClusterName(ctx context.Context, data *clusterNameData, hostname string) string {
	data.mutex.Lock()
	defer data.mutex.Unlock()

	if !data.initDone {
		data.clusterName = config.Datadog.GetString("cluster_name")
		if data.clusterName != "" {
			log.Infof("Got cluster name %s from config", data.clusterName)
			// the host alias "hostname-clustername" must not exceed 255 chars
			hostAlias := hostname + "-" + data.clusterName
			if !validClusterName.MatchString(data.clusterName) || len(hostAlias) > 255 {
				log.Errorf("\"%s\" isn’t a valid cluster name. It must be dot-separated tokens where tokens "+
					"start with a lowercase letter followed by lowercase letters, numbers, or "+
					"hyphens, and cannot end with a hyphen nor have a dot adjacent to a hyphen and \"%s\" must not "+
					"exceed 255 chars", data.clusterName, hostAlias)
				log.Errorf("As a consequence, the cluster name provided by the config will be ignored")
				data.clusterName = ""
			}
		}

		// autodiscover clustername through k8s providers' API
		if data.clusterName == "" {
			for cloudProvider, getClusterNameFunc := range ProviderCatalog {
				log.Debugf("Trying to auto discover the cluster name from the %s API...", cloudProvider)
				clusterName, err := getClusterNameFunc(ctx)
				if err != nil {
					log.Debugf("Unable to auto discover the cluster name from the %s API: %s", cloudProvider, err)
					// try the next cloud provider
					continue
				}
				// if the clustername is valid but contains a "_" in the middle of it, we will replace it later such that
				// to make it valid to RFC1123.
				if clusterName != "" {
					log.Infof("Using cluster name %s auto discovered from the %s API", clusterName, cloudProvider)
					if strings.HasSuffix(clusterName, "-") || strings.HasSuffix(clusterName, "_") {
						log.Errorf("Registering an invalid clusterName as they are not allowed to end with `_` or `-`")
					}
					data.clusterName = clusterName
					break
				}
			}
		}

		if data.clusterName == "" {
			clusterName, err := hostinfo.GetNodeClusterNameLabel(ctx)
			if err != nil {
				log.Debugf("Unable to auto discover the cluster name from node label : %s", err)
			} else {
				data.clusterName = clusterName
			}
		}

		if data.clusterName != "" {
			if lower := strings.ToLower(data.clusterName); lower != data.clusterName {
				log.Infof("Putting cluster name %q in lowercase, became: %q", data.clusterName, lower)
				data.clusterName = lower
			}
		}

		data.initDone = true
	}

	return data.clusterName
}

// GetClusterName returns a k8s cluster name if it exists, either directly specified or autodiscovered
func GetClusterName(ctx context.Context, hostname string) string {
	return getClusterName(ctx, defaultClusterNameData, hostname)
}

func resetClusterName(data *clusterNameData) {
	data.mutex.Lock()
	defer data.mutex.Unlock()
	data.initDone = false
}

// ResetClusterName resets the clustername, which allows it to be detected again. Used for tests
func ResetClusterName() {
	resetClusterName(defaultClusterNameData)
}

// GetClusterID looks for an env variable which should contain the cluster ID.
// This variable should come from a configmap, created by the cluster-agent.
// This function is meant for the node-agent to call (cluster-agent should call GetOrCreateClusterID)
func GetClusterID() (string, error) {
	cacheClusterIDKey := cache.BuildAgentKey(config.ClusterIDCacheKey)
	if cachedClusterID, found := cache.Cache.Get(cacheClusterIDKey); found {
		return cachedClusterID.(string), nil
	}

	// in older setups the cluster ID was exposed as an env var from a configmap created by the cluster agent
	clusterID, found := os.LookupEnv(clusterIDEnv)
	if !found {
		log.Debugf("Cluster ID env variable %s is missing, calling the Cluster Agent", clusterIDEnv)

		dcaClient, err := clusteragent.GetClusterAgentClient()
		if err != nil {
			return "", err
		}
		clusterID, err = dcaClient.GetKubernetesClusterID()
		if err != nil {
			return "", err
		}
		log.Debugf("Cluster ID retrieved from the Cluster Agent, set to %s", clusterID)
	}

	if len(clusterID) != 36 {
		err := fmt.Errorf("Unexpected value for Cluster ID: %s, ignoring it", clusterID)
		return "", err
	}

	cache.Cache.Set(cacheClusterIDKey, clusterID, cache.NoExpiration)
	return clusterID, nil
}

// setProviderCatalog should only be used for testing.
func setProviderCatalog(catalog map[string]Provider) {
	ProviderCatalog = catalog
}
