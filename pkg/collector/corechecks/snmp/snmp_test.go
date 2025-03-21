// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-2020 Datadog, Inc.

package snmp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cihub/seelog"
	"github.com/gosnmp/gosnmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/DataDog/datadog-agent/pkg/aggregator"
	"github.com/DataDog/datadog-agent/pkg/aggregator/mocksender"
	"github.com/DataDog/datadog-agent/pkg/collector/check"
	"github.com/DataDog/datadog-agent/pkg/metrics"
	"github.com/DataDog/datadog-agent/pkg/util/log"

	"github.com/DataDog/datadog-agent/pkg/collector/corechecks/snmp/checkconfig"
	"github.com/DataDog/datadog-agent/pkg/collector/corechecks/snmp/common"
	"github.com/DataDog/datadog-agent/pkg/collector/corechecks/snmp/gosnmplib"
	"github.com/DataDog/datadog-agent/pkg/collector/corechecks/snmp/session"
)

func TestBasicSample(t *testing.T) {
	checkconfig.SetConfdPathAndCleanProfiles()
	sess := session.CreateMockSession()
	session.NewSession = func(*checkconfig.CheckConfig) (session.Session, error) {
		return sess, nil
	}
	chk := Check{}
	aggregator.InitAggregatorWithFlushInterval(nil, nil, "", 1*time.Hour)

	// language=yaml
	rawInstanceConfig := []byte(`
ip_address: 1.2.3.4
community_string: public
metrics:
- symbol:
    OID: 1.3.6.1.2.1.2.1
    name: ifNumber
  metric_tags:
  - symboltag1:1
  - symboltag2:2
- symbol:
    OID: 1.2.3.4.0
    name: aMetricWithExtractValue
    extract_value: '(\d+)C'
- table:
    OID: 1.3.6.1.2.1.2.2
    name: ifTable
  symbols:
  - OID: 1.3.6.1.2.1.2.2.1.14
    name: ifInErrors
  - OID: 1.3.6.1.2.1.2.2.1.20
    name: ifOutErrors

  metric_tags:
  - tag: if_index
    index: 1
  - tag: if_desc
    column:
      OID: 1.3.6.1.2.1.2.2.1.2
      name: ifDescr
metric_tags:
  - OID: 1.3.6.1.2.1.1.5.0
    symbol: sysName
    tag: snmp_host
tags:
  - "mytag:foo"
`)

	err := chk.Configure(rawInstanceConfig, []byte(``), "test")
	assert.Nil(t, err)

	sender := mocksender.NewMockSender(chk.ID()) // required to initiate aggregator
	sender.On("Gauge", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("MonotonicCount", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("ServiceCheck", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("EventPlatformEvent", mock.Anything, mock.Anything).Return()

	sender.On("Commit").Return()

	packet := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.1.3.0",
				Type:  gosnmp.TimeTicks,
				Value: 20,
			},
			{
				Name:  "1.3.6.1.2.1.1.5.0",
				Type:  gosnmp.OctetString,
				Value: []byte("foo_sys_name"),
			},
			{
				Name:  "1.3.6.1.2.1.2.1",
				Type:  gosnmp.Integer,
				Value: 30,
			},
			{
				Name:  "1.2.3.4.0",
				Type:  gosnmp.OctetString,
				Value: []byte("22C"),
			},
		},
	}

	bulkPackets := []gosnmp.SnmpPacket{
		{
			Variables: []gosnmp.SnmpPDU{
				{
					Name:  "1.3.6.1.2.1.2.2.1.14.1",
					Type:  gosnmp.Integer,
					Value: 141,
				},
				{
					Name:  "1.3.6.1.2.1.2.2.1.2.1",
					Type:  gosnmp.OctetString,
					Value: []byte("desc1"),
				},
				{
					Name:  "1.3.6.1.2.1.2.2.1.20.1",
					Type:  gosnmp.Integer,
					Value: 201,
				},
				{
					Name:  "1.3.6.1.2.1.2.2.1.6.1",
					Type:  gosnmp.OctetString,
					Value: []byte("00:00:00:00:00:01"),
				},
				{
					Name:  "1.3.6.1.2.1.2.2.1.7.1",
					Type:  gosnmp.Integer,
					Value: 1,
				},
				{
					Name:  "1.3.6.1.2.1.2.2.1.14.2",
					Type:  gosnmp.Integer,
					Value: 142,
				},
				{
					Name:  "1.3.6.1.2.1.2.2.1.2.2",
					Type:  gosnmp.OctetString,
					Value: []byte("desc2"),
				},
				{
					Name:  "1.3.6.1.2.1.2.2.1.20.2",
					Type:  gosnmp.Integer,
					Value: 202,
				},
				{
					Name:  "1.3.6.1.2.1.2.2.1.6.2",
					Type:  gosnmp.OctetString,
					Value: []byte("00:00:00:00:00:02"),
				},
				{
					Name:  "1.3.6.1.2.1.2.2.1.7.2",
					Type:  gosnmp.Integer,
					Value: 3,
				},
			},
		},
		{
			Variables: []gosnmp.SnmpPDU{
				{
					Name:  "1.3.6.1.2.1.2.2.1.15.1",
					Type:  gosnmp.Integer,
					Value: 141,
				},
				{
					Name:  "1.3.6.1.2.1.2.2.1.3.2",
					Type:  gosnmp.OctetString,
					Value: []byte("none"),
				},
				{
					Name:  "1.3.6.1.2.1.2.2.1.21.1",
					Type:  gosnmp.Integer,
					Value: 201,
				},
				{
					Name:  "1.3.6.1.2.1.2.2.1.7.2",
					Type:  gosnmp.Integer,
					Value: 3,
				},
				{
					Name:  "1.3.6.1.2.1.2.2.1.8.2",
					Type:  gosnmp.Integer,
					Value: 1,
				},
			},
		},
		{
			Variables: []gosnmp.SnmpPDU{
				{
					Name:  "1.3.6.1.2.1.2.2.1.8.1",
					Type:  gosnmp.Integer,
					Value: 1,
				},
				{
					Name:  "1.3.6.1.2.1.31.1.1.1.1.1",
					Type:  gosnmp.OctetString,
					Value: []byte("nameRow1"),
				},
				{
					Name:  "1.3.6.1.2.1.31.1.1.1.18.1",
					Type:  gosnmp.OctetString,
					Value: []byte("descRow1"),
				},
				{
					Name:  "1.3.6.1.2.1.2.2.1.8.2",
					Type:  gosnmp.Integer,
					Value: 1,
				},
				{
					Name:  "1.3.6.1.2.1.31.1.1.1.1.2",
					Type:  gosnmp.OctetString,
					Value: []byte("nameRow2"),
				},
				{
					Name:  "1.3.6.1.2.1.31.1.1.1.18.2",
					Type:  gosnmp.OctetString,
					Value: []byte("descRow2"),
				},
			},
		},
		{
			Variables: []gosnmp.SnmpPDU{
				{
					Name:  "1.3.6.1.2.1.2.2.1.9.2",
					Type:  gosnmp.TimeTicks,
					Value: 123,
				},
				{
					Name:  "1.3.6.1.2.1.31.1.1.1.2.2",
					Type:  gosnmp.Integer,
					Value: 596,
				},
				{
					Name:  "1.3.6.1.2.1.31.1.1.1.19.2",
					Type:  gosnmp.TimeTicks,
					Value: 707,
				},
			},
		},
	}

	sess.On("GetNext", []string{"1.3"}).Return(&gosnmplib.MockValidReachableGetNextPacket, nil)
	sess.On("Get", mock.Anything).Return(&packet, nil)
	sess.On("Get", mock.Anything).Return(&packet, nil)
	sess.On("GetBulk", []string{"1.3.6.1.2.1.2.2.1.14", "1.3.6.1.2.1.2.2.1.2", "1.3.6.1.2.1.2.2.1.20", "1.3.6.1.2.1.2.2.1.6", "1.3.6.1.2.1.2.2.1.7"}, checkconfig.DefaultBulkMaxRepetitions).Return(&bulkPackets[0], nil)
	sess.On("GetBulk", []string{"1.3.6.1.2.1.2.2.1.14.2", "1.3.6.1.2.1.2.2.1.2.2", "1.3.6.1.2.1.2.2.1.20.2", "1.3.6.1.2.1.2.2.1.6.2", "1.3.6.1.2.1.2.2.1.7.2"}, checkconfig.DefaultBulkMaxRepetitions).Return(&bulkPackets[1], nil)
	sess.On("GetBulk", []string{"1.3.6.1.2.1.2.2.1.8", "1.3.6.1.2.1.31.1.1.1.1", "1.3.6.1.2.1.31.1.1.1.18"}, checkconfig.DefaultBulkMaxRepetitions).Return(&bulkPackets[2], nil)
	sess.On("GetBulk", []string{"1.3.6.1.2.1.2.2.1.8.2", "1.3.6.1.2.1.31.1.1.1.1.2", "1.3.6.1.2.1.31.1.1.1.18.2"}, checkconfig.DefaultBulkMaxRepetitions).Return(&bulkPackets[3], nil)

	err = chk.Run()
	assert.Nil(t, err)

	snmpTags := []string{"snmp_device:1.2.3.4"}
	snmpGlobalTags := append(common.CopyStrings(snmpTags), "snmp_host:foo_sys_name")
	snmpGlobalTagsWithLoader := append(common.CopyStrings(snmpGlobalTags), "loader:core")
	row1Tags := append(common.CopyStrings(snmpGlobalTags), "if_index:1", "if_desc:desc1")
	row2Tags := append(common.CopyStrings(snmpGlobalTags), "if_index:2", "if_desc:desc2")
	scalarTags := append(common.CopyStrings(snmpGlobalTags), "symboltag1:1", "symboltag2:2")

	sender.AssertMetric(t, "Gauge", "snmp.devices_monitored", float64(1), "", snmpGlobalTags)
	sender.AssertMetric(t, "Gauge", "snmp.sysUpTimeInstance", float64(20), "", snmpGlobalTags)
	sender.AssertMetric(t, "Gauge", "snmp.ifNumber", float64(30), "", scalarTags)
	sender.AssertMetric(t, "Gauge", "snmp.aMetricWithExtractValue", float64(22), "", snmpGlobalTags)
	sender.AssertMetric(t, "Gauge", "snmp.ifInErrors", float64(141), "", row1Tags)
	sender.AssertMetric(t, "Gauge", "snmp.ifInErrors", float64(142), "", row2Tags)
	sender.AssertMetric(t, "Gauge", "snmp.ifOutErrors", float64(201), "", row1Tags)
	sender.AssertMetric(t, "Gauge", "snmp.ifOutErrors", float64(202), "", row2Tags)

	sender.AssertMetricTaggedWith(t, "MonotonicCount", "datadog.snmp.check_interval", snmpGlobalTagsWithLoader)
	sender.AssertMetricTaggedWith(t, "Gauge", "datadog.snmp.check_duration", snmpGlobalTagsWithLoader)
	sender.AssertMetric(t, "Gauge", "datadog.snmp.submitted_metrics", 7, "", snmpGlobalTagsWithLoader)
}

func TestSupportedMetricTypes(t *testing.T) {
	checkconfig.SetConfdPathAndCleanProfiles()
	sess := session.CreateMockSession()
	session.NewSession = func(*checkconfig.CheckConfig) (session.Session, error) {
		return sess, nil
	}
	chk := Check{}
	// language=yaml
	rawInstanceConfig := []byte(`
collect_device_metadata: false
ip_address: 1.2.3.4
community_string: public
metrics:
- symbol:
    OID: 1.2.3.4.5.0
    name: SomeGaugeMetric
- symbol:
    OID: 1.2.3.4.5.1
    name: SomeCounter32Metric
- symbol:
    OID: 1.2.3.4.5.2
    name: SomeCounter64Metric
`)

	err := chk.Configure(rawInstanceConfig, []byte(``), "test")
	assert.Nil(t, err)

	sender := mocksender.NewMockSender(chk.ID()) // required to initiate aggregator
	sender.On("Gauge", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("MonotonicCount", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("Rate", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("ServiceCheck", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("Commit").Return()

	packet := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.1.3.0",
				Type:  gosnmp.TimeTicks,
				Value: 20,
			},
			{
				Name:  "1.2.3.4.5.0",
				Type:  gosnmp.Integer,
				Value: 30,
			},
			{
				Name:  "1.2.3.4.5.1",
				Type:  gosnmp.Counter32,
				Value: 40,
			},
			{
				Name:  "1.2.3.4.5.2",
				Type:  gosnmp.Counter64,
				Value: 50,
			},
		},
	}

	sess.On("GetNext", []string{"1.3"}).Return(&gosnmplib.MockValidReachableGetNextPacket, nil)
	sess.On("Get", mock.Anything).Return(&packet, nil)

	err = chk.Run()
	assert.Nil(t, err)

	tags := []string{"snmp_device:1.2.3.4"}
	sender.AssertMetric(t, "Gauge", "snmp.devices_monitored", float64(1), "", tags)
	sender.AssertMetric(t, "Gauge", "snmp.sysUpTimeInstance", float64(20), "", tags)
	sender.AssertMetric(t, "Gauge", "snmp.SomeGaugeMetric", float64(30), "", tags)
	sender.AssertMetric(t, "Rate", "snmp.SomeCounter32Metric", float64(40), "", tags)
	sender.AssertMetric(t, "Rate", "snmp.SomeCounter64Metric", float64(50), "", tags)
}

func TestProfile(t *testing.T) {
	timeNow = common.MockTimeNow
	aggregator.InitAggregatorWithFlushInterval(nil, nil, "", 1*time.Hour)
	checkconfig.SetConfdPathAndCleanProfiles()

	sess := session.CreateMockSession()
	session.NewSession = func(*checkconfig.CheckConfig) (session.Session, error) {
		return sess, nil
	}
	chk := Check{}
	// language=yaml
	rawInstanceConfig := []byte(`
ip_address: 1.2.3.4
community_string: public
profile: f5-big-ip
collect_device_metadata: true
oid_batch_size: 10
tags:
  - "mytag:val1"
  - "mytag:val1" # add duplicate tag for testing deduplication
  - "autodiscovery_subnet:127.0.0.0/30"
`)
	// language=yaml
	rawInitConfig := []byte(`
profiles:
  f5-big-ip:
    definition_file: f5-big-ip.yaml
`)

	err := chk.Configure(rawInstanceConfig, rawInitConfig, "test")
	assert.Nil(t, err)

	sender := mocksender.NewMockSender(chk.ID()) // required to initiate aggregator
	sender.On("Gauge", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("MonotonicCount", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("ServiceCheck", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("EventPlatformEvent", mock.Anything, mock.Anything).Return()
	sender.On("Commit").Return()

	packet := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.1.5.0",
				Type:  gosnmp.OctetString,
				Value: []byte("foo_sys_name"),
			},
			{
				Name:  "1.3.6.1.2.1.1.1.0",
				Type:  gosnmp.OctetString,
				Value: []byte("my_desc"),
			},
			{
				Name:  "1.3.6.1.2.1.1.2.0",
				Type:  gosnmp.ObjectIdentifier,
				Value: "1.2.3.4",
			},
			{
				Name:  "1.3.6.1.4.1.3375.2.1.1.2.1.44.0",
				Type:  gosnmp.Integer,
				Value: 30,
			},
			{
				Name:  "1.3.6.1.2.1.1.3.0",
				Type:  gosnmp.TimeTicks,
				Value: 20,
			},
		},
	}

	bulkPacket := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.2.2.1.13.1",
				Type:  gosnmp.Integer,
				Value: 131,
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.14.1",
				Type:  gosnmp.Integer,
				Value: 141,
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.2.1",
				Type:  gosnmp.OctetString,
				Value: []byte("ifDescRow1"),
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.6.1",
				Type:  gosnmp.OctetString,
				Value: []byte("00:00:00:00:00:01"),
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.7.1",
				Type:  gosnmp.Integer,
				Value: 1,
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.8.1",
				Type:  gosnmp.Integer,
				Value: 1,
			},
			{
				Name:  "1.3.6.1.2.1.31.1.1.1.1.1",
				Type:  gosnmp.OctetString,
				Value: []byte("nameRow1"),
			},
			{
				Name:  "1.3.6.1.2.1.31.1.1.1.18.1",
				Type:  gosnmp.OctetString,
				Value: []byte("descRow1"),
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.13.2",
				Type:  gosnmp.Integer,
				Value: 132,
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.14.2",
				Type:  gosnmp.Integer,
				Value: 142,
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.2.2",
				Type:  gosnmp.OctetString,
				Value: []byte("ifDescRow2"),
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.6.2",
				Type:  gosnmp.OctetString,
				Value: []byte("00:00:00:00:00:02"),
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.7.2",
				Type:  gosnmp.Integer,
				Value: 1,
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.8.2",
				Type:  gosnmp.Integer,
				Value: 1,
			},
			{
				Name:  "1.3.6.1.2.1.31.1.1.1.1.2",
				Type:  gosnmp.OctetString,
				Value: []byte("nameRow2"),
			},
			{
				Name:  "1.3.6.1.2.1.31.1.1.1.18.2",
				Type:  gosnmp.OctetString,
				Value: []byte("descRow2"),
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
		},
	}

	sess.On("GetNext", []string{"1.3"}).Return(&gosnmplib.MockValidReachableGetNextPacket, nil)
	sess.On("Get", []string{
		"1.3.6.1.2.1.1.5.0",
		"1.3.6.1.2.1.1.1.0",
		"1.3.6.1.2.1.1.2.0",
		"1.3.6.1.4.1.3375.2.1.1.2.1.44.0",
		"1.3.6.1.4.1.3375.2.1.1.2.1.44.999",
		"1.2.3.4.5",
		"1.3.6.1.2.1.1.3.0",
	}).Return(&packet, nil)
	sess.On("GetBulk", []string{
		"1.3.6.1.2.1.2.2.1.13",
		"1.3.6.1.2.1.2.2.1.14",
		"1.3.6.1.2.1.2.2.1.2",
		"1.3.6.1.2.1.2.2.1.6",
		"1.3.6.1.2.1.2.2.1.7",
		"1.3.6.1.2.1.2.2.1.8",
		"1.3.6.1.2.1.31.1.1.1.1",
		"1.3.6.1.2.1.31.1.1.1.18",
	}, checkconfig.DefaultBulkMaxRepetitions).Return(&bulkPacket, nil)

	err = chk.Run()
	assert.Nil(t, err)

	snmpTags := []string{"device_namespace:default", "snmp_device:1.2.3.4", "snmp_profile:f5-big-ip", "device_vendor:f5", "snmp_host:foo_sys_name"}
	row1Tags := append(common.CopyStrings(snmpTags), "interface:nameRow1", "interface_alias:descRow1")
	row2Tags := append(common.CopyStrings(snmpTags), "interface:nameRow2", "interface_alias:descRow2")

	sender.AssertMetric(t, "Gauge", "snmp.devices_monitored", float64(1), "", snmpTags)
	sender.AssertMetric(t, "Gauge", "snmp.sysUpTimeInstance", float64(20), "", snmpTags)
	sender.AssertMetric(t, "MonotonicCount", "snmp.ifInErrors", float64(141), "", row1Tags)
	sender.AssertMetric(t, "MonotonicCount", "snmp.ifInErrors", float64(142), "", row2Tags)
	sender.AssertMetric(t, "MonotonicCount", "snmp.ifInDiscards", float64(131), "", row1Tags)
	sender.AssertMetric(t, "MonotonicCount", "snmp.ifInDiscards", float64(132), "", row2Tags)
	sender.AssertMetric(t, "Gauge", "snmp.sysStatMemoryTotal", float64(30), "", snmpTags)

	// language=json
	event := []byte(`
{
  "subnet": "127.0.0.0/30",
  "namespace":"default",
  "devices": [
    {
      "id": "default:1.2.3.4",
      "id_tags": [
        "device_namespace:default",
        "snmp_device:1.2.3.4"
      ],
      "name": "foo_sys_name",
      "description": "my_desc",
      "ip_address": "1.2.3.4",
      "sys_object_id": "1.2.3.4",
      "profile": "f5-big-ip",
      "vendor": "f5",
      "subnet": "127.0.0.0/30",
      "tags": [
        "autodiscovery_subnet:127.0.0.0/30",
        "device_namespace:default",
        "device_vendor:f5",
        "mytag:val1",
        "prefix:f",
        "snmp_device:1.2.3.4",
        "snmp_host:foo_sys_name",
        "snmp_profile:f5-big-ip",
        "some_tag:some_tag_value",
        "suffix:oo_sys_name"
      ],
      "status": 1
    }
  ],
  "interfaces": [
    {
      "device_id": "default:1.2.3.4",
      "id_tags": ["interface:nameRow1"],
      "index": 1,
      "name": "nameRow1",
      "alias": "descRow1",
      "description": "ifDescRow1",
      "mac_address": "00:00:00:00:00:01",
      "admin_status": 1,
      "oper_status": 1
    },
    {
      "device_id": "default:1.2.3.4",
	  "id_tags": ["interface:nameRow2"],
      "index": 2,
      "name": "nameRow2",
      "alias": "descRow2",
      "description": "ifDescRow2",
      "mac_address": "00:00:00:00:00:02",
      "admin_status": 1,
      "oper_status": 1
    }
  ],
  "collect_timestamp":946684800
}
`)
	compactEvent := new(bytes.Buffer)
	err = json.Compact(compactEvent, event)
	assert.NoError(t, err)

	sender.AssertEventPlatformEvent(t, compactEvent.String(), "network-devices-metadata")

	sender.AssertServiceCheck(t, "snmp.can_check", metrics.ServiceCheckOK, "", snmpTags, "")
}

func TestServiceCheckFailures(t *testing.T) {
	checkconfig.SetConfdPathAndCleanProfiles()
	sess := session.CreateMockSession()
	session.NewSession = func(*checkconfig.CheckConfig) (session.Session, error) {
		return sess, nil
	}
	sess.ConnectErr = fmt.Errorf("can't connect")
	chk := Check{}

	// language=yaml
	rawInstanceConfig := []byte(`
collect_device_metadata: false
ip_address: 1.2.3.4
community_string: public
`)

	err := chk.Configure(rawInstanceConfig, []byte(``), "test")
	assert.Nil(t, err)

	sender := mocksender.NewMockSender(chk.ID()) // required to initiate aggregator
	sender.On("Gauge", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("MonotonicCount", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("ServiceCheck", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("Commit").Return()

	err = chk.Run()
	assert.Error(t, err, "snmp connection error: can't connect")

	snmpTags := []string{"snmp_device:1.2.3.4"}

	sender.AssertMetric(t, "Gauge", "datadog.snmp.submitted_metrics", 0.0, "", snmpTags)
	sender.AssertMetricTaggedWith(t, "Gauge", "datadog.snmp.check_duration", snmpTags)
	sender.AssertMetricTaggedWith(t, "MonotonicCount", "datadog.snmp.check_interval", snmpTags)
	sender.AssertServiceCheck(t, "snmp.can_check", metrics.ServiceCheckCritical, "", snmpTags, "snmp connection error: can't connect")
}

func TestCheckID(t *testing.T) {
	checkconfig.SetConfdPathAndCleanProfiles()
	check1 := snmpFactory()
	check2 := snmpFactory()
	check3 := snmpFactory()
	// language=yaml
	rawInstanceConfig1 := []byte(`
ip_address: 1.1.1.1
community_string: abc
`)
	// language=yaml
	rawInstanceConfig2 := []byte(`
ip_address: 2.2.2.2
community_string: abc
`)
	// language=yaml
	rawInstanceConfig3 := []byte(`
ip_address: 3.3.3.3
community_string: abc
namespace: ns3
`)

	err := check1.Configure(rawInstanceConfig1, []byte(``), "test")
	assert.Nil(t, err)
	err = check2.Configure(rawInstanceConfig2, []byte(``), "test")
	assert.Nil(t, err)
	err = check3.Configure(rawInstanceConfig3, []byte(``), "test")
	assert.Nil(t, err)

	assert.Equal(t, check.ID("snmp:default:1.1.1.1:a3ec59dfb03e4457"), check1.ID())
	assert.Equal(t, check.ID("snmp:default:2.2.2.2:3979cd473e4beb3f"), check2.ID())
	assert.Equal(t, check.ID("snmp:ns3:3.3.3.3:819516f4c3986cc6"), check3.ID())
	assert.NotEqual(t, check1.ID(), check2.ID())
}

func TestCheck_Run(t *testing.T) {
	sysObjectIDPacketInvalidSysObjectIDMock := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.1.2.0",
				Type:  gosnmp.ObjectIdentifier,
				Value: "1.999999",
			},
		},
	}

	sysObjectIDPacketInvalidValueMock := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.1.2.0",
				Type:  gosnmp.ObjectIdentifier,
				Value: 1.0,
			},
		},
	}

	sysObjectIDPacketInvalidConversionMock := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.1.2.0",
				Type:  gosnmp.ObjectIdentifier,
				Value: gosnmp.SnmpPDU{},
			},
		},
	}

	sysObjectIDPacketInvalidOidMock := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.6.3.15.1.1.1.0", // usmStatsUnsupportedSecLevels
				Type:  gosnmp.Counter32,
				Value: 123,
			},
		},
	}

	sysObjectIDPacketOkMock := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.1.2.0",
				Type:  gosnmp.ObjectIdentifier,
				Value: "1.3.6.1.4.1.3375.2.1.3.4.1",
			},
		},
	}

	valuesPacketErrMock := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.1.3.0",
				Type:  gosnmp.TimeTicks,
				Value: 20,
			},
			{
				Name:  "1.3.6.1.2.1.1.5.0",
				Type:  gosnmp.OctetString,
				Value: []byte("foo_sys_name"),
			},
			{
				Name:  "1.3.6.1.2.1.1.5.0",
				Type:  gosnmp.OctetString,
				Value: []byte("foo_sys_name"),
			},
			{
				Name:  "1.3.6.1.4.1.3375.2.1.1.2.1.44.0",
				Type:  gosnmp.Integer,
				Value: 30,
			},
		},
	}

	valuesPacketUptime := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.1.3.0",
				Type:  gosnmp.TimeTicks,
				Value: 20,
			},
		},
	}

	tests := []struct {
		name                     string
		disableAggregator        bool
		sessionConnError         error
		sysObjectIDPacket        gosnmp.SnmpPacket
		sysObjectIDError         error
		reachableGetNextError    error
		reachableValuesPacket    gosnmp.SnmpPacket
		valuesPacket             gosnmp.SnmpPacket
		valuesError              error
		expectedErr              string
		expectedSubmittedMetrics float64
	}{
		{
			name:             "connection error",
			sessionConnError: fmt.Errorf("can't connect"),
			expectedErr:      "snmp connection error: can't connect",
		},
		{
			name:                     "failed to fetch sysobjectid",
			sysObjectIDError:         fmt.Errorf("no sysobjectid"),
			valuesPacket:             valuesPacketUptime,
			reachableValuesPacket:    gosnmplib.MockValidReachableGetNextPacket,
			expectedErr:              "failed to autodetect profile: failed to fetch sysobjectid: cannot get sysobjectid: no sysobjectid",
			expectedSubmittedMetrics: 1.0,
		},
		{
			name:                  "unexpected values count",
			reachableValuesPacket: gosnmplib.MockValidReachableGetNextPacket,
			expectedErr:           "failed to autodetect profile: failed to fetch sysobjectid: expected 1 value, but got 0: variables=[]",
		},
		{
			name:                  "failed to fetch sysobjectid with invalid value",
			reachableValuesPacket: gosnmplib.MockValidReachableGetNextPacket,
			sysObjectIDPacket:     sysObjectIDPacketInvalidValueMock,
			expectedErr:           "failed to autodetect profile: failed to fetch sysobjectid: error getting value from pdu: oid 1.3.6.1.2.1.1.2.0: ObjectIdentifier should be string type but got float64 type: gosnmp.SnmpPDU{Name:\"1.3.6.1.2.1.1.2.0\", Type:0x6, Value:1}",
		},
		{
			name:                  "failed to fetch sysobjectid with conversion error",
			reachableValuesPacket: gosnmplib.MockValidReachableGetNextPacket,
			sysObjectIDPacket:     sysObjectIDPacketInvalidConversionMock,
			expectedErr:           "failed to autodetect profile: failed to fetch sysobjectid: error getting value from pdu: oid 1.3.6.1.2.1.1.2.0: ObjectIdentifier should be string type but got gosnmp.SnmpPDU type: gosnmp.SnmpPDU{Name:\"1.3.6.1.2.1.1.2.0\", Type:0x6, Value:gosnmp.SnmpPDU{Name:\"\", Type:0x0, Value:interface {}(nil)}}",
		},
		{
			name:                  "failed to fetch sysobjectid with error oid",
			reachableValuesPacket: gosnmplib.MockValidReachableGetNextPacket,
			sysObjectIDPacket:     sysObjectIDPacketInvalidOidMock,
			expectedErr:           "failed to autodetect profile: failed to fetch sysobjectid: expect `1.3.6.1.2.1.1.2.0` OID but got `1.3.6.1.6.3.15.1.1.1.0` OID with value `{counter 123}`",
		},
		{
			name:                  "failed to get profile sys object id",
			reachableValuesPacket: gosnmplib.MockValidReachableGetNextPacket,
			sysObjectIDPacket:     sysObjectIDPacketInvalidSysObjectIDMock,
			expectedErr:           "failed to autodetect profile: failed to get profile sys object id for `1.999999`: failed to get most specific profile for sysObjectID `1.999999`, for matched oids []: cannot get most specific oid from empty list of oids",
		},
		{
			name:                  "failed to fetch values",
			reachableValuesPacket: gosnmplib.MockValidReachableGetNextPacket,
			sysObjectIDPacket:     sysObjectIDPacketOkMock,
			valuesPacket:          valuesPacketErrMock,
			valuesError:           fmt.Errorf("no value"),
			expectedErr:           "failed to fetch values: failed to fetch scalar oids with batching: failed to fetch scalar oids: fetch scalar: error getting oids `[1.3.6.1.2.1.1.3.0 1.3.6.1.4.1.3375.2.1.1.2.1.44.0 1.3.6.1.4.1.3375.2.1.1.2.1.44.999 1.2.3.4.5 1.3.6.1.2.1.1.5.0]`: no value",
		},
		{
			name:                  "failed to fetch sysobjectid and failed to fetch values",
			reachableValuesPacket: gosnmplib.MockValidReachableGetNextPacket,
			sysObjectIDError:      fmt.Errorf("no sysobjectid"),
			valuesPacket:          valuesPacketErrMock,
			valuesError:           fmt.Errorf("no value"),
			expectedErr:           "failed to autodetect profile: failed to fetch sysobjectid: cannot get sysobjectid: no sysobjectid; failed to fetch values: failed to fetch scalar oids with batching: failed to fetch scalar oids: fetch scalar: error getting oids `[1.3.6.1.2.1.1.3.0]`: no value",
		},
		{
			name:                  "failed reachability check",
			sysObjectIDError:      fmt.Errorf("no sysobjectid"),
			reachableGetNextError: fmt.Errorf("no value for GextNext"),
			valuesPacket:          valuesPacketErrMock,
			valuesError:           fmt.Errorf("no value"),
			expectedErr:           "check device reachable: failed: no value for GextNext; failed to autodetect profile: failed to fetch sysobjectid: cannot get sysobjectid: no sysobjectid; failed to fetch values: failed to fetch scalar oids with batching: failed to fetch scalar oids: fetch scalar: error getting oids `[1.3.6.1.2.1.1.3.0]`: no value",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkconfig.SetConfdPathAndCleanProfiles()
			sess := session.CreateMockSession()
			session.NewSession = func(*checkconfig.CheckConfig) (session.Session, error) {
				return sess, nil
			}
			sess.ConnectErr = tt.sessionConnError
			chk := Check{}

			// language=yaml
			rawInstanceConfig := []byte(`
collect_device_metadata: false
ip_address: 1.2.3.4
community_string: public
`)

			err := chk.Configure(rawInstanceConfig, []byte(``), "test")
			assert.Nil(t, err)

			sender := new(mocksender.MockSender)

			if !tt.disableAggregator {
				aggregator.InitAggregatorWithFlushInterval(nil, nil, "", 1*time.Hour)
			}

			mocksender.SetSender(sender, chk.ID())

			sess.On("GetNext", []string{"1.3"}).Return(&tt.reachableValuesPacket, tt.reachableGetNextError)
			sess.On("Get", []string{"1.3.6.1.2.1.1.2.0"}).Return(&tt.sysObjectIDPacket, tt.sysObjectIDError)
			sess.On("Get", []string{"1.3.6.1.2.1.1.3.0", "1.3.6.1.4.1.3375.2.1.1.2.1.44.0", "1.3.6.1.4.1.3375.2.1.1.2.1.44.999", "1.2.3.4.5", "1.3.6.1.2.1.1.5.0"}).Return(&tt.valuesPacket, tt.valuesError)
			sess.On("Get", []string{"1.3.6.1.2.1.1.3.0"}).Return(&tt.valuesPacket, tt.valuesError)

			sender.On("Gauge", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
			sender.On("Gauge", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
			sender.On("MonotonicCount", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
			sender.On("ServiceCheck", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
			sender.On("Commit").Return()

			err = chk.Run()
			assert.EqualError(t, err, tt.expectedErr)

			snmpTags := []string{"snmp_device:1.2.3.4"}

			sender.AssertMetric(t, "Gauge", "datadog.snmp.submitted_metrics", tt.expectedSubmittedMetrics, "", snmpTags)
			sender.AssertMetricTaggedWith(t, "Gauge", "datadog.snmp.check_duration", snmpTags)
			sender.AssertMetricTaggedWith(t, "MonotonicCount", "datadog.snmp.check_interval", snmpTags)

			sender.AssertServiceCheck(t, "snmp.can_check", metrics.ServiceCheckCritical, "", snmpTags, tt.expectedErr)
		})
	}
}

func TestCheck_Run_sessionCloseError(t *testing.T) {
	checkconfig.SetConfdPathAndCleanProfiles()

	var b bytes.Buffer
	w := bufio.NewWriter(&b)

	l, err := seelog.LoggerFromWriterWithMinLevelAndFormat(w, seelog.DebugLvl, "[%LEVEL] %FuncShort: %Msg")
	assert.Nil(t, err)
	log.SetupLogger(l, "debug")

	sess := session.CreateMockSession()
	session.NewSession = func(*checkconfig.CheckConfig) (session.Session, error) {
		return sess, nil
	}
	sess.CloseErr = fmt.Errorf("close error")
	chk := Check{}

	// language=yaml
	rawInstanceConfig := []byte(`
collect_device_metadata: false
ip_address: 1.2.3.4
community_string: public
metrics:
- symbol:
    OID: 1.2.3
    name: myMetric
`)

	err = chk.Configure(rawInstanceConfig, []byte(``), "test")
	assert.Nil(t, err)

	sender := mocksender.NewMockSender(chk.ID()) // required to initiate aggregator

	mocksender.SetSender(sender, chk.ID())

	packet := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{},
	}
	sess.On("GetNext", []string{"1.3"}).Return(&gosnmplib.MockValidReachableGetNextPacket, nil)
	sess.On("Get", []string{"1.2.3", "1.3.6.1.2.1.1.3.0"}).Return(&packet, nil)
	sender.SetupAcceptAll()

	err = chk.Run()
	assert.Nil(t, err)

	w.Flush()
	logs := b.String()

	snmpTags := []string{"snmp_device:1.2.3.4"}
	sender.AssertMetric(t, "Gauge", "datadog.snmp.submitted_metrics", 0.0, "", snmpTags)
	sender.AssertMetricTaggedWith(t, "Gauge", "datadog.snmp.check_duration", snmpTags)
	sender.AssertMetricTaggedWith(t, "MonotonicCount", "datadog.snmp.check_interval", snmpTags)

	sender.AssertServiceCheck(t, "snmp.can_check", metrics.ServiceCheckOK, "", snmpTags, "")

	assert.Equal(t, strings.Count(logs, "failed to close sess"), 1, logs)
}

func TestReportDeviceMetadataEvenOnProfileError(t *testing.T) {
	timeNow = common.MockTimeNow
	aggregator.InitAggregatorWithFlushInterval(nil, nil, "", 1*time.Hour)
	checkconfig.SetConfdPathAndCleanProfiles()

	sess := session.CreateMockSession()
	session.NewSession = func(*checkconfig.CheckConfig) (session.Session, error) {
		return sess, nil
	}
	chk := Check{}
	// language=yaml
	rawInstanceConfig := []byte(`
ip_address: 1.2.3.4
community_string: public
collect_device_metadata: true
oid_batch_size: 10
tags:
  - "mytag:val1"
  - "autodiscovery_subnet:127.0.0.0/30"
`)
	// language=yaml
	rawInitConfig := []byte(``)

	err := chk.Configure(rawInstanceConfig, rawInitConfig, "test")
	assert.Nil(t, err)

	sender := mocksender.NewMockSender(chk.ID()) // required to initiate aggregator
	sender.On("Gauge", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("MonotonicCount", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("ServiceCheck", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("EventPlatformEvent", mock.Anything, mock.Anything).Return()
	sender.On("Commit").Return()

	packet := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.1.5.0",
				Type:  gosnmp.OctetString,
				Value: []byte("foo_sys_name"),
			},
			{
				Name:  "1.3.6.1.2.1.1.1.0",
				Type:  gosnmp.OctetString,
				Value: []byte("my_desc"),
			},
			{
				Name:  "1.3.6.1.2.1.1.2.0",
				Type:  gosnmp.ObjectIdentifier,
				Value: "1.2.3.4",
			},
			{
				Name:  "1.3.6.1.2.1.1.3.0",
				Type:  gosnmp.TimeTicks,
				Value: 20,
			},
		},
	}

	bulkPacket := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.2.2.1.2.1",
				Type:  gosnmp.OctetString,
				Value: []byte("ifDescRow1"),
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.6.1",
				Type:  gosnmp.OctetString,
				Value: []byte("00:00:00:00:00:01"),
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.7.1",
				Type:  gosnmp.Integer,
				Value: 1,
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.8.1",
				Type:  gosnmp.Integer,
				Value: 1,
			},
			{
				Name:  "1.3.6.1.2.1.31.1.1.1.1.1",
				Type:  gosnmp.OctetString,
				Value: []byte("nameRow1"),
			},
			{
				Name:  "1.3.6.1.2.1.31.1.1.1.18.1",
				Type:  gosnmp.OctetString,
				Value: []byte("descRow1"),
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.2.2",
				Type:  gosnmp.OctetString,
				Value: []byte("ifDescRow2"),
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.6.2",
				Type:  gosnmp.OctetString,
				Value: []byte("00:00:00:00:00:02"),
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.7.2",
				Type:  gosnmp.Integer,
				Value: 1,
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.8.2",
				Type:  gosnmp.Integer,
				Value: 1,
			},
			{
				Name:  "1.3.6.1.2.1.31.1.1.1.1.2",
				Type:  gosnmp.OctetString,
				Value: []byte("nameRow2"),
			},
			{
				Name:  "1.3.6.1.2.1.31.1.1.1.18.2",
				Type:  gosnmp.OctetString,
				Value: []byte("descRow2"),
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
		},
	}
	sess.On("GetNext", []string{"1.3"}).Return(&gosnmplib.MockValidReachableGetNextPacket, nil)
	var sysObjectIDPacket *gosnmp.SnmpPacket
	sess.On("Get", []string{"1.3.6.1.2.1.1.2.0"}).Return(sysObjectIDPacket, fmt.Errorf("no value"))

	sess.On("Get", []string{
		"1.3.6.1.2.1.1.5.0",
		"1.3.6.1.2.1.1.1.0",
		"1.3.6.1.2.1.1.2.0",
		"1.3.6.1.2.1.1.3.0",
	}).Return(&packet, nil)
	sess.On("GetBulk", []string{
		//"1.3.6.1.2.1.2.2.1.13",
		//"1.3.6.1.2.1.2.2.1.14",
		"1.3.6.1.2.1.2.2.1.2",
		"1.3.6.1.2.1.2.2.1.6",
		"1.3.6.1.2.1.2.2.1.7",
		"1.3.6.1.2.1.2.2.1.8",
		"1.3.6.1.2.1.31.1.1.1.1",
		"1.3.6.1.2.1.31.1.1.1.18",
	}, checkconfig.DefaultBulkMaxRepetitions).Return(&bulkPacket, nil)

	err = chk.Run()
	assert.EqualError(t, err, "failed to autodetect profile: failed to fetch sysobjectid: cannot get sysobjectid: no value")

	snmpTags := []string{"device_namespace:default", "snmp_device:1.2.3.4"}

	sender.AssertMetric(t, "Gauge", "snmp.devices_monitored", float64(1), "", snmpTags)
	sender.AssertMetric(t, "Gauge", "snmp.sysUpTimeInstance", float64(20), "", snmpTags)

	// language=json
	event := []byte(`
{
  "subnet": "127.0.0.0/30",
  "namespace":"default",
  "devices": [
    {
      "id": "default:1.2.3.4",
      "id_tags": [
        "device_namespace:default",
        "snmp_device:1.2.3.4"
      ],
      "name": "foo_sys_name",
      "description": "my_desc",
      "ip_address": "1.2.3.4",
      "sys_object_id": "1.2.3.4",
      "profile": "",
      "vendor": "",
      "subnet": "127.0.0.0/30",
      "tags": [
        "autodiscovery_subnet:127.0.0.0/30",
        "device_namespace:default",
        "mytag:val1",
        "snmp_device:1.2.3.4"
      ],
      "status": 1
    }
  ],
  "interfaces": [
    {
      "device_id": "default:1.2.3.4",
      "id_tags": ["interface:nameRow1"],
      "index": 1,
      "name": "nameRow1",
      "alias": "descRow1",
      "description": "ifDescRow1",
      "mac_address": "00:00:00:00:00:01",
      "admin_status": 1,
      "oper_status": 1
    },
    {
      "device_id": "default:1.2.3.4",
      "id_tags": ["interface:nameRow2"],
      "index": 2,
      "name": "nameRow2",
      "alias": "descRow2",
      "description": "ifDescRow2",
      "mac_address": "00:00:00:00:00:02",
      "admin_status": 1,
      "oper_status": 1
    }
  ],
  "collect_timestamp":946684800
}
`)
	compactEvent := new(bytes.Buffer)
	err = json.Compact(compactEvent, event)
	assert.NoError(t, err)

	sender.AssertEventPlatformEvent(t, compactEvent.String(), "network-devices-metadata")

	sender.AssertServiceCheck(t, "snmp.can_check", metrics.ServiceCheckCritical, "", snmpTags, "failed to autodetect profile: failed to fetch sysobjectid: cannot get sysobjectid: no value")
}

func TestReportDeviceMetadataWithFetchError(t *testing.T) {
	timeNow = common.MockTimeNow
	aggregator.InitAggregatorWithFlushInterval(nil, nil, "", 1*time.Hour)
	checkconfig.SetConfdPathAndCleanProfiles()

	sess := session.CreateMockSession()
	session.NewSession = func(*checkconfig.CheckConfig) (session.Session, error) {
		return sess, nil
	}
	chk := Check{}
	// language=yaml
	rawInstanceConfig := []byte(`
ip_address: 1.2.3.5
community_string: public
collect_device_metadata: true
tags:
  - "mytag:val1"
  - "autodiscovery_subnet:127.0.0.0/30"
`)
	// language=yaml
	rawInitConfig := []byte(``)

	err := chk.Configure(rawInstanceConfig, rawInitConfig, "test")
	assert.Nil(t, err)

	sender := mocksender.NewMockSender(chk.ID()) // required to initiate aggregator
	sender.On("Gauge", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("MonotonicCount", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("ServiceCheck", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("EventPlatformEvent", mock.Anything, mock.Anything).Return()
	sender.On("Commit").Return()

	var nilPacket *gosnmp.SnmpPacket
	sess.On("GetNext", []string{"1.3"}).Return(nilPacket, fmt.Errorf("no value for GetNext"))
	sess.On("Get", []string{"1.3.6.1.2.1.1.2.0"}).Return(nilPacket, fmt.Errorf("no value"))

	sess.On("Get", []string{
		"1.3.6.1.2.1.1.5.0",
		"1.3.6.1.2.1.1.1.0",
		"1.3.6.1.2.1.1.2.0",
		"1.3.6.1.2.1.1.3.0",
	}).Return(nilPacket, fmt.Errorf("device failure"))

	expectedErrMsg := "check device reachable: failed: no value for GetNext; failed to autodetect profile: failed to fetch sysobjectid: cannot get sysobjectid: no value; failed to fetch values: failed to fetch scalar oids with batching: failed to fetch scalar oids: fetch scalar: error getting oids `[1.3.6.1.2.1.1.5.0 1.3.6.1.2.1.1.1.0 1.3.6.1.2.1.1.2.0 1.3.6.1.2.1.1.3.0]`: device failure"

	err = chk.Run()
	assert.EqualError(t, err, expectedErrMsg)

	snmpTags := []string{"device_namespace:default", "snmp_device:1.2.3.5"}

	sender.AssertMetric(t, "Gauge", "snmp.devices_monitored", float64(1), "", snmpTags)

	// language=json
	event := []byte(`
{
  "subnet": "127.0.0.0/30",
  "namespace":"default",
  "devices": [
    {
      "id": "default:1.2.3.5",
      "id_tags": [
        "device_namespace:default",
        "snmp_device:1.2.3.5"
      ],
      "name": "",
      "description": "",
      "ip_address": "1.2.3.5",
      "sys_object_id": "",
      "profile": "",
      "vendor": "",
      "subnet": "127.0.0.0/30",
      "tags": [
        "autodiscovery_subnet:127.0.0.0/30",
        "device_namespace:default",
        "mytag:val1",
        "snmp_device:1.2.3.5"
      ],
      "status": 2
    }
  ],
  "collect_timestamp":946684800
}
`)
	compactEvent := new(bytes.Buffer)
	err = json.Compact(compactEvent, event)
	assert.NoError(t, err)

	sender.AssertEventPlatformEvent(t, compactEvent.String(), "network-devices-metadata")

	sender.AssertServiceCheck(t, "snmp.can_check", metrics.ServiceCheckCritical, "", snmpTags, expectedErrMsg)
}

func TestDiscovery(t *testing.T) {
	timeNow = common.MockTimeNow
	checkconfig.SetConfdPathAndCleanProfiles()
	sess := session.CreateMockSession()
	session.NewSession = func(*checkconfig.CheckConfig) (session.Session, error) {
		return sess, nil
	}
	chk := Check{}
	aggregator.InitAggregatorWithFlushInterval(nil, nil, "", 1*time.Hour)

	// language=yaml
	rawInstanceConfig := []byte(`
network_address: 10.10.0.0/30
community_string: public
collect_device_metadata: true
oid_batch_size: 10
metrics:
- symbol:
    OID: 1.3.6.1.2.1.2.1
    name: ifNumber
  metric_tags:
  - symboltag1:1
  - symboltag2:2
metric_tags:
  - OID: 1.3.6.1.2.1.1.5.0
    symbol: sysName
    tag: snmp_host
`)

	discoveryPacket := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.1.2.0",
				Type:  gosnmp.ObjectIdentifier,
				Value: "1.2.3",
			},
		},
	}

	sess.On("GetNext", []string{"1.3"}).Return(&gosnmplib.MockValidReachableGetNextPacket, nil)
	sess.On("Get", []string{"1.3.6.1.2.1.1.2.0"}).Return(&discoveryPacket, nil)

	err := chk.Configure(rawInstanceConfig, []byte(``), "test")
	assert.Nil(t, err)

	time.Sleep(100 * time.Millisecond)
	devices := chk.discovery.GetDiscoveredDeviceConfigs()
	assert.Equal(t, 4, len(devices))

	sender := mocksender.NewMockSender(chk.ID()) // required to initiate aggregator
	sender.On("Gauge", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("MonotonicCount", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("ServiceCheck", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("EventPlatformEvent", mock.Anything, mock.Anything).Return()
	sender.On("Commit").Return()

	packet := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.1.3.0",
				Type:  gosnmp.TimeTicks,
				Value: 20,
			},
			{
				Name:  "1.3.6.1.2.1.1.5.0",
				Type:  gosnmp.OctetString,
				Value: []byte("foo_sys_name"),
			},
			{
				Name:  "1.3.6.1.2.1.2.1",
				Type:  gosnmp.Integer,
				Value: 30,
			},
		},
	}

	sess.On("Get", mock.Anything).Return(&packet, nil)

	bulkPacket := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.2.2.1.2.1",
				Type:  gosnmp.OctetString,
				Value: []byte("ifDescRow1"),
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.6.1",
				Type:  gosnmp.OctetString,
				Value: []byte("00:00:00:00:00:01"),
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.7.1",
				Type:  gosnmp.Integer,
				Value: 1,
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.8.1",
				Type:  gosnmp.Integer,
				Value: 1,
			},
			{
				Name:  "1.3.6.1.2.1.31.1.1.1.1.1",
				Type:  gosnmp.OctetString,
				Value: []byte("nameRow1"),
			},
			{
				Name:  "1.3.6.1.2.1.31.1.1.1.18.1",
				Type:  gosnmp.OctetString,
				Value: []byte("descRow1"),
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.2.2",
				Type:  gosnmp.OctetString,
				Value: []byte("ifDescRow2"),
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.6.2",
				Type:  gosnmp.OctetString,
				Value: []byte("00:00:00:00:00:02"),
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.7.2",
				Type:  gosnmp.Integer,
				Value: 1,
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.8.2",
				Type:  gosnmp.Integer,
				Value: 1,
			},
			{
				Name:  "1.3.6.1.2.1.31.1.1.1.1.2",
				Type:  gosnmp.OctetString,
				Value: []byte("nameRow2"),
			},
			{
				Name:  "1.3.6.1.2.1.31.1.1.1.18.2",
				Type:  gosnmp.OctetString,
				Value: []byte("descRow2"),
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
		},
	}

	sess.On("GetBulk", []string{
		//"1.3.6.1.2.1.2.2.1.2", "1.3.6.1.2.1.2.2.1.6", "1.3.6.1.2.1.2.2.1.7", "1.3.6.1.2.1.2.2.1.8", "1.3.6.1.2.1.31.1.1.1.1"
		"1.3.6.1.2.1.2.2.1.2",
		"1.3.6.1.2.1.2.2.1.6",
		"1.3.6.1.2.1.2.2.1.7",
		"1.3.6.1.2.1.2.2.1.8",
		"1.3.6.1.2.1.31.1.1.1.1",
		"1.3.6.1.2.1.31.1.1.1.18",
	}, checkconfig.DefaultBulkMaxRepetitions).Return(&bulkPacket, nil)

	deviceMap := []struct {
		ipAddress string
		deviceID  string
	}{
		{ipAddress: "10.10.0.0", deviceID: "default:10.10.0.0"},
		{ipAddress: "10.10.0.1", deviceID: "default:10.10.0.1"},
		{ipAddress: "10.10.0.2", deviceID: "default:10.10.0.2"},
		{ipAddress: "10.10.0.3", deviceID: "default:10.10.0.3"},
	}

	err = chk.Run()
	assert.Nil(t, err)

	for _, deviceData := range deviceMap {
		snmpTags := []string{"device_namespace:default", "snmp_device:" + deviceData.ipAddress, "autodiscovery_subnet:10.10.0.0/30"}
		snmpGlobalTags := append(common.CopyStrings(snmpTags), "snmp_host:foo_sys_name")
		snmpGlobalTagsWithLoader := append(common.CopyStrings(snmpGlobalTags), "loader:core")
		scalarTags := append(common.CopyStrings(snmpGlobalTags), "symboltag1:1", "symboltag2:2")

		sender.AssertMetric(t, "Gauge", "snmp.devices_monitored", float64(1), "", snmpGlobalTags)
		sender.AssertMetric(t, "Gauge", "snmp.sysUpTimeInstance", float64(20), "", snmpGlobalTags)
		sender.AssertMetric(t, "Gauge", "snmp.ifNumber", float64(30), "", scalarTags)

		sender.AssertMetricTaggedWith(t, "MonotonicCount", "datadog.snmp.check_interval", snmpGlobalTagsWithLoader)
		sender.AssertMetricTaggedWith(t, "Gauge", "datadog.snmp.check_duration", snmpGlobalTagsWithLoader)
		sender.AssertMetric(t, "Gauge", "datadog.snmp.submitted_metrics", 2, "", snmpGlobalTagsWithLoader)

		// language=json
		event := []byte(fmt.Sprintf(`
{
  "subnet": "10.10.0.0/30",
  "namespace":"default",
  "devices": [
    {
      "id": "%s",
      "id_tags": [
        "device_namespace:default",
        "snmp_device:%s"
      ],
      "name": "foo_sys_name",
      "description": "",
      "ip_address": "%s",
      "sys_object_id": "",
      "profile": "",
      "vendor": "",
      "subnet": "10.10.0.0/30",
      "tags": [
        "autodiscovery_subnet:10.10.0.0/30",
        "device_namespace:default",
        "snmp_device:%s",
        "snmp_host:foo_sys_name"
      ],
      "status": 1
    }
  ],
  "interfaces": [
    {
      "device_id": "%s",
      "id_tags": ["interface:nameRow1"],
      "index": 1,
      "name": "nameRow1",
      "alias": "descRow1",
      "description": "ifDescRow1",
      "mac_address": "00:00:00:00:00:01",
      "admin_status": 1,
      "oper_status": 1
    },
    {
      "device_id": "%s",
	  "id_tags": ["interface:nameRow2"],
      "index": 2,
      "name": "nameRow2",
      "alias": "descRow2",
      "description": "ifDescRow2",
      "mac_address": "00:00:00:00:00:02",
      "admin_status": 1,
      "oper_status": 1
    }
  ],
  "collect_timestamp":946684800
}
`, deviceData.deviceID, deviceData.ipAddress, deviceData.ipAddress, deviceData.ipAddress, deviceData.deviceID, deviceData.deviceID))
		compactEvent := new(bytes.Buffer)
		err = json.Compact(compactEvent, event)
		assert.NoError(t, err)

		sender.AssertEventPlatformEvent(t, compactEvent.String(), "network-devices-metadata")
	}
	networkTags := []string{"network:10.10.0.0/30", "autodiscovery_subnet:10.10.0.0/30"}
	sender.AssertMetric(t, "Gauge", "snmp.discovered_devices_count", 4, "", networkTags)
}

func TestDiscovery_CheckError(t *testing.T) {
	checkconfig.SetConfdPathAndCleanProfiles()

	var b bytes.Buffer
	w := bufio.NewWriter(&b)
	l, err := seelog.LoggerFromWriterWithMinLevelAndFormat(w, seelog.DebugLvl, "[%LEVEL] %FuncShort: %Msg")
	assert.Nil(t, err)
	log.SetupLogger(l, "debug")

	sess := session.CreateMockSession()
	session.NewSession = func(*checkconfig.CheckConfig) (session.Session, error) {
		return sess, nil
	}
	chk := Check{}
	aggregator.InitAggregatorWithFlushInterval(nil, nil, "", 1*time.Hour)

	// language=yaml
	rawInstanceConfig := []byte(`
collect_device_metadata: false
network_address: 10.10.0.0/30
community_string: public
metrics:
- symbol:
    OID: 1.3.6.1.2.1.2.1
    name: ifNumber
  metric_tags:
  - symboltag1:1
  - symboltag2:2
metric_tags:
  - OID: 1.3.6.1.2.1.1.5.0
    symbol: sysName
    tag: snmp_host
`)

	discoveryPacket := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.1.2.0",
				Type:  gosnmp.ObjectIdentifier,
				Value: "1.2.3",
			},
		},
	}

	sess.On("GetNext", []string{"1.3"}).Return(&gosnmplib.MockValidReachableGetNextPacket, nil)
	sess.On("Get", []string{"1.3.6.1.2.1.1.2.0"}).Return(&discoveryPacket, nil)

	err = chk.Configure(rawInstanceConfig, []byte(``), "test")
	assert.Nil(t, err)

	time.Sleep(100 * time.Millisecond)
	devices := chk.discovery.GetDiscoveredDeviceConfigs()
	assert.Equal(t, 4, len(devices))

	sender := mocksender.NewMockSender(chk.ID()) // required to initiate aggregator
	sender.On("Gauge", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("MonotonicCount", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("ServiceCheck", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("Commit").Return()

	sess.On("Get", mock.Anything).Return(&gosnmp.SnmpPacket{}, fmt.Errorf("get error"))

	err = chk.Run()
	assert.Nil(t, err)

	for i := 0; i < 4; i++ {
		snmpTags := []string{fmt.Sprintf("snmp_device:10.10.0.%d", i)}
		snmpGlobalTags := common.CopyStrings(snmpTags)
		snmpGlobalTagsWithLoader := append(common.CopyStrings(snmpGlobalTags), "loader:core")

		sender.AssertMetric(t, "Gauge", "snmp.devices_monitored", float64(1), "", snmpGlobalTags)
		sender.AssertMetricTaggedWith(t, "MonotonicCount", "datadog.snmp.check_interval", snmpGlobalTagsWithLoader)
		sender.AssertMetricTaggedWith(t, "Gauge", "datadog.snmp.check_duration", snmpGlobalTagsWithLoader)
		sender.AssertMetric(t, "Gauge", "datadog.snmp.submitted_metrics", 0, "", snmpGlobalTagsWithLoader)
	}

	w.Flush()
	logs := b.String()

	assert.Equal(t, strings.Count(logs, "error collecting for device 10.10.0."), 4, logs)
	for i := 0; i < 4; i++ {
		assert.Equal(t, strings.Count(logs, "error collecting for device 10.10.0."+strconv.Itoa(i)), 1, logs)
	}
}

func TestDeviceIDAsHostname(t *testing.T) {
	checkconfig.SetConfdPathAndCleanProfiles()
	sess := session.CreateMockSession()
	session.NewSession = func(*checkconfig.CheckConfig) (session.Session, error) {
		return sess, nil
	}
	chk := Check{}
	aggregator.InitAggregatorWithFlushInterval(nil, nil, "", 1*time.Hour)

	// language=yaml
	rawInstanceConfig := []byte(`
ip_address: 1.2.3.4
community_string: public
metrics:
- symbol:
    OID: 1.3.6.1.2.1.2.1
    name: ifNumber
  metric_tags:
  - symboltag1:1
  - symboltag2:2
use_device_id_as_hostname: true
`)

	err := chk.Configure(rawInstanceConfig, []byte(``), "test")
	assert.Nil(t, err)

	sender := mocksender.NewMockSender(chk.ID()) // required to initiate aggregator
	sender.On("Gauge", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("MonotonicCount", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("ServiceCheck", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("EventPlatformEvent", mock.Anything, mock.Anything).Return()
	sender.On("Commit").Return()

	sess.On("GetNext", []string{"1.3"}).Return(&gosnmplib.MockValidReachableGetNextPacket, nil)

	packet := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.1.3.0",
				Type:  gosnmp.TimeTicks,
				Value: 20,
			},
			{
				Name:  "1.3.6.1.2.1.2.1",
				Type:  gosnmp.Integer,
				Value: 30,
			},
		},
	}

	sess.On("Get", mock.Anything).Return(&packet, nil)

	bulkPackets := []gosnmp.SnmpPacket{
		{
			Variables: []gosnmp.SnmpPDU{
				{
					Name:  "1.3.6.1.2.1.2.2.1.2.1",
					Type:  gosnmp.OctetString,
					Value: []byte("ifDescRow1"),
				},
				{
					Name:  "1.3.6.1.2.1.2.2.1.6.1",
					Type:  gosnmp.OctetString,
					Value: []byte("00:00:00:00:00:01"),
				},
				{
					Name:  "1.3.6.1.2.1.2.2.1.7.1",
					Type:  gosnmp.Integer,
					Value: 1,
				},
				{
					Name:  "1.3.6.1.2.1.2.2.1.8.1",
					Type:  gosnmp.Integer,
					Value: 1,
				},
				{
					Name:  "1.3.6.1.2.1.31.1.1.1.1.1",
					Type:  gosnmp.OctetString,
					Value: []byte("nameRow1"),
				},
				{
					Name:  "9", // exit table
					Type:  gosnmp.Integer,
					Value: 999,
				},
				{
					Name:  "9", // exit table
					Type:  gosnmp.Integer,
					Value: 999,
				},
				{
					Name:  "9", // exit table
					Type:  gosnmp.Integer,
					Value: 999,
				},
				{
					Name:  "9", // exit table
					Type:  gosnmp.Integer,
					Value: 999,
				},
				{
					Name:  "9", // exit table
					Type:  gosnmp.Integer,
					Value: 999,
				},
			},
		}, {
			Variables: []gosnmp.SnmpPDU{
				{
					Name:  "1.3.6.1.2.1.31.1.1.1.18.1",
					Type:  gosnmp.OctetString,
					Value: []byte("ifDescRow1"),
				},
				{
					Name:  "9", // exit table
					Type:  gosnmp.Integer,
					Value: 999,
				},
			},
		},
	}
	sess.On("GetBulk", []string{
		"1.3.6.1.2.1.2.2.1.2", "1.3.6.1.2.1.2.2.1.6", "1.3.6.1.2.1.2.2.1.7", "1.3.6.1.2.1.2.2.1.8", "1.3.6.1.2.1.31.1.1.1.1",
	}, checkconfig.DefaultBulkMaxRepetitions).Return(&bulkPackets[0], nil)
	sess.On("GetBulk", []string{
		"1.3.6.1.2.1.31.1.1.1.18",
	}, checkconfig.DefaultBulkMaxRepetitions).Return(&bulkPackets[1], nil)

	err = chk.Run()
	assert.Nil(t, err)

	hostname := "device:default:1.2.3.4"
	snmpTags := []string{"snmp_device:1.2.3.4"}
	snmpGlobalTags := common.CopyStrings(snmpTags)
	snmpGlobalTagsWithLoader := append(common.CopyStrings(snmpGlobalTags), "loader:core")
	scalarTags := append(common.CopyStrings(snmpGlobalTags), "symboltag1:1", "symboltag2:2")

	sender.AssertMetric(t, "Gauge", "snmp.devices_monitored", float64(1), hostname, snmpGlobalTags)
	sender.AssertMetric(t, "Gauge", "snmp.sysUpTimeInstance", float64(20), hostname, snmpGlobalTags)
	sender.AssertMetric(t, "Gauge", "snmp.ifNumber", float64(30), hostname, scalarTags)

	sender.AssertMetricTaggedWith(t, "MonotonicCount", "datadog.snmp.check_interval", snmpGlobalTagsWithLoader)
	sender.AssertMetricTaggedWith(t, "Gauge", "datadog.snmp.check_duration", snmpGlobalTagsWithLoader)
	sender.AssertMetric(t, "Gauge", "datadog.snmp.submitted_metrics", 2, hostname, snmpGlobalTagsWithLoader)
}

func TestDiscoveryDeviceIDAsHostname(t *testing.T) {
	timeNow = common.MockTimeNow
	checkconfig.SetConfdPathAndCleanProfiles()
	sess := session.CreateMockSession()
	session.NewSession = func(*checkconfig.CheckConfig) (session.Session, error) {
		return sess, nil
	}
	chk := Check{}
	aggregator.InitAggregatorWithFlushInterval(nil, nil, "", 1*time.Hour)

	// language=yaml
	rawInstanceConfig := []byte(`
network_address: 10.10.0.0/30
community_string: public
use_device_id_as_hostname: true
oid_batch_size: 10
metrics:
- symbol:
    OID: 1.3.6.1.2.1.2.1
    name: ifNumber
  metric_tags:
  - symboltag1:1
  - symboltag2:2
`)

	sess.On("GetNext", []string{"1.3"}).Return(&gosnmplib.MockValidReachableGetNextPacket, nil)

	discoveryPacket := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.1.2.0",
				Type:  gosnmp.ObjectIdentifier,
				Value: "1.2.3",
			},
		},
	}
	sess.On("Get", []string{"1.3.6.1.2.1.1.2.0"}).Return(&discoveryPacket, nil)

	err := chk.Configure(rawInstanceConfig, []byte(``), "test")
	assert.Nil(t, err)

	time.Sleep(100 * time.Millisecond)
	devices := chk.discovery.GetDiscoveredDeviceConfigs()
	assert.Equal(t, 4, len(devices))

	sender := mocksender.NewMockSender(chk.ID()) // required to initiate aggregator
	sender.On("Gauge", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("MonotonicCount", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("ServiceCheck", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	sender.On("EventPlatformEvent", mock.Anything, mock.Anything).Return()
	sender.On("Commit").Return()

	packet := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.1.3.0",
				Type:  gosnmp.TimeTicks,
				Value: 20,
			},
			{
				Name:  "1.3.6.1.2.1.1.5.0",
				Type:  gosnmp.OctetString,
				Value: []byte("foo_sys_name"),
			},
			{
				Name:  "1.3.6.1.2.1.2.1",
				Type:  gosnmp.Integer,
				Value: 30,
			},
		},
	}
	sess.On("Get", mock.Anything).Return(&packet, nil)

	bulkPacket := gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{
				Name:  "1.3.6.1.2.1.2.2.1.2.1",
				Type:  gosnmp.OctetString,
				Value: []byte("ifDescRow1"),
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.6.1",
				Type:  gosnmp.OctetString,
				Value: []byte("00:00:00:00:00:01"),
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.7.1",
				Type:  gosnmp.Integer,
				Value: 1,
			},
			{
				Name:  "1.3.6.1.2.1.2.2.1.8.1",
				Type:  gosnmp.Integer,
				Value: 1,
			},
			{
				Name:  "1.3.6.1.2.1.31.1.1.1.1.1",
				Type:  gosnmp.OctetString,
				Value: []byte("nameRow1"),
			},
			{
				Name:  "1.3.6.1.2.1.31.1.1.1.18.1",
				Type:  gosnmp.OctetString,
				Value: []byte("ifDescRow1"),
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
			{
				Name:  "9", // exit table
				Type:  gosnmp.Integer,
				Value: 999,
			},
		},
	}
	sess.On("GetBulk", []string{
		"1.3.6.1.2.1.2.2.1.2", "1.3.6.1.2.1.2.2.1.6", "1.3.6.1.2.1.2.2.1.7", "1.3.6.1.2.1.2.2.1.8", "1.3.6.1.2.1.31.1.1.1.1", "1.3.6.1.2.1.31.1.1.1.18",
	}, checkconfig.DefaultBulkMaxRepetitions).Return(&bulkPacket, nil)

	deviceMap := []struct {
		ipAddress string
		deviceID  string
	}{
		{ipAddress: "10.10.0.0", deviceID: "default:10.10.0.0"},
		{ipAddress: "10.10.0.1", deviceID: "default:10.10.0.1"},
		{ipAddress: "10.10.0.2", deviceID: "default:10.10.0.2"},
		{ipAddress: "10.10.0.3", deviceID: "default:10.10.0.3"},
	}

	err = chk.Run()
	assert.Nil(t, err)

	for _, deviceData := range deviceMap {
		hostname := "device:" + deviceData.deviceID
		snmpTags := []string{"snmp_device:" + deviceData.ipAddress, "autodiscovery_subnet:10.10.0.0/30"}
		snmpGlobalTags := common.CopyStrings(snmpTags)
		snmpGlobalTagsWithLoader := append(common.CopyStrings(snmpGlobalTags), "loader:core")
		scalarTags := append(common.CopyStrings(snmpGlobalTags), "symboltag1:1", "symboltag2:2")

		sender.AssertMetric(t, "Gauge", "snmp.devices_monitored", float64(1), hostname, snmpGlobalTags)
		sender.AssertMetric(t, "Gauge", "snmp.sysUpTimeInstance", float64(20), hostname, snmpGlobalTags)
		sender.AssertMetric(t, "Gauge", "snmp.ifNumber", float64(30), hostname, scalarTags)

		sender.AssertMetricTaggedWith(t, "MonotonicCount", "datadog.snmp.check_interval", snmpGlobalTagsWithLoader)
		sender.AssertMetricTaggedWith(t, "Gauge", "datadog.snmp.check_duration", snmpGlobalTagsWithLoader)
		sender.AssertMetric(t, "Gauge", "datadog.snmp.submitted_metrics", 2, hostname, snmpGlobalTagsWithLoader)
	}
	networkTags := []string{"network:10.10.0.0/30", "autodiscovery_subnet:10.10.0.0/30"}
	sender.AssertMetric(t, "Gauge", "snmp.discovered_devices_count", 4, "", networkTags)
}
