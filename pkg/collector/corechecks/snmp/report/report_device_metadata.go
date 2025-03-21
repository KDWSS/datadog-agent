package report

import (
	json "encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/DataDog/datadog-agent/pkg/epforwarder"
	"github.com/DataDog/datadog-agent/pkg/util"
	"github.com/DataDog/datadog-agent/pkg/util/log"

	"github.com/DataDog/datadog-agent/pkg/collector/corechecks/snmp/checkconfig"
	"github.com/DataDog/datadog-agent/pkg/collector/corechecks/snmp/common"
	"github.com/DataDog/datadog-agent/pkg/collector/corechecks/snmp/metadata"
	"github.com/DataDog/datadog-agent/pkg/collector/corechecks/snmp/valuestore"
)

// interfaceNameTagKey matches the `interface` tag used in `_generic-if.yaml` for ifName
var interfaceNameTagKey = "interface"

// ReportNetworkDeviceMetadata reports device metadata
func (ms *MetricSender) ReportNetworkDeviceMetadata(config *checkconfig.CheckConfig, store *valuestore.ResultValueStore, origTags []string, collectTime time.Time, deviceStatus metadata.DeviceStatus) {
	tags := common.CopyStrings(origTags)
	tags = util.SortUniqInPlace(tags)

	device := buildNetworkDeviceMetadata(config.DeviceID, config.DeviceIDTags, config, store, tags, deviceStatus)

	interfaces, err := buildNetworkInterfacesMetadata(config.DeviceID, store)
	if err != nil {
		log.Debugf("Unable to build interfaces metadata: %s", err)
	}

	metadataPayloads := batchPayloads(config.Namespace, config.ResolvedSubnetName, collectTime, metadata.PayloadMetadataBatchSize, device, interfaces)

	for _, payload := range metadataPayloads {
		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			log.Errorf("Error marshalling device metadata: %s", err)
			return
		}
		ms.sender.EventPlatformEvent(string(payloadBytes), epforwarder.EventTypeNetworkDevicesMetadata)
	}
}

func buildNetworkDeviceMetadata(deviceID string, idTags []string, config *checkconfig.CheckConfig, store *valuestore.ResultValueStore, tags []string, deviceStatus metadata.DeviceStatus) metadata.DeviceMetadata {
	var vendor, sysName, sysDescr, sysObjectID string
	if store != nil {
		sysName = store.GetScalarValueAsString(metadata.SysNameOID)
		sysDescr = store.GetScalarValueAsString(metadata.SysDescrOID)
		sysObjectID = store.GetScalarValueAsString(metadata.SysObjectIDOID)
	}

	if config.ProfileDef != nil {
		vendor = config.ProfileDef.Device.Vendor
	}

	return metadata.DeviceMetadata{
		ID:          deviceID,
		IDTags:      idTags,
		Name:        sysName,
		Description: sysDescr,
		IPAddress:   config.IPAddress,
		SysObjectID: sysObjectID,
		Profile:     config.Profile,
		Vendor:      vendor,
		Tags:        tags,
		Subnet:      config.ResolvedSubnetName,
		Status:      deviceStatus,
	}
}

func buildNetworkInterfacesMetadata(deviceID string, store *valuestore.ResultValueStore) ([]metadata.InterfaceMetadata, error) {
	if store == nil {
		// it's expected that the value store is nil if we can't reach the device
		// in that case, we just return an nil slice.
		return nil, nil
	}
	indexes, err := store.GetColumnIndexes(metadata.IfNameOID)
	if err != nil {
		return nil, fmt.Errorf("no interface indexes found: %s", err)
	}

	var interfaces []metadata.InterfaceMetadata
	for _, strIndex := range indexes {
		index, err := strconv.Atoi(strIndex)
		if err != nil {
			log.Warnf("interface metadata: invalid index: %s", index)
			continue
		}

		name := store.GetColumnValueAsString(metadata.IfNameOID, strIndex)
		networkInterface := metadata.InterfaceMetadata{
			DeviceID:    deviceID,
			Index:       int32(index),
			Name:        name,
			Alias:       store.GetColumnValueAsString(metadata.IfAliasOID, strIndex),
			Description: store.GetColumnValueAsString(metadata.IfDescrOID, strIndex),
			MacAddress:  store.GetColumnValueAsString(metadata.IfPhysAddressOID, strIndex),
			AdminStatus: int32(store.GetColumnValueAsFloat(metadata.IfAdminStatusOID, strIndex)),
			OperStatus:  int32(store.GetColumnValueAsFloat(metadata.IfOperStatusOID, strIndex)),
			IDTags:      []string{interfaceNameTagKey + ":" + name},
		}
		interfaces = append(interfaces, networkInterface)
	}
	return interfaces, err
}

func batchPayloads(namespace string, subnet string, collectTime time.Time, batchSize int, device metadata.DeviceMetadata, interfaces []metadata.InterfaceMetadata) []metadata.NetworkDevicesMetadata {
	var payloads []metadata.NetworkDevicesMetadata
	var resourceCount int
	payload := metadata.NetworkDevicesMetadata{
		Devices: []metadata.DeviceMetadata{
			device,
		},
		Subnet:           subnet,
		Namespace:        namespace,
		CollectTimestamp: collectTime.Unix(),
	}
	resourceCount++

	for _, interfaceMetadata := range interfaces {
		if resourceCount == batchSize {
			payloads = append(payloads, payload)
			payload = metadata.NetworkDevicesMetadata{
				Subnet:           subnet,
				Namespace:        namespace,
				CollectTimestamp: collectTime.Unix(),
			}
			resourceCount = 0
		}
		resourceCount++
		payload.Interfaces = append(payload.Interfaces, interfaceMetadata)
	}

	payloads = append(payloads, payload)
	return payloads
}
