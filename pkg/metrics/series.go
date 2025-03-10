// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package metrics

import (
	"bytes"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"sort"
	"strconv"
	"strings"

	jsoniter "github.com/json-iterator/go"

	"github.com/DataDog/datadog-agent/pkg/aggregator/ckey"
	"github.com/DataDog/datadog-agent/pkg/serializer/marshaler"
	"github.com/DataDog/datadog-agent/pkg/telemetry"
)

var (
	seriesExpvar = expvar.NewMap("series")

	tlmSeries = telemetry.NewCounter("metrics", "series_split",
		[]string{"action"}, "Series split")
)

// Point represents a metric value at a specific time
type Point struct {
	Ts    float64
	Value float64
}

// MarshalJSON return a Point as an array of value (to be compatible with v1 API)
// FIXME(maxime): to be removed when v2 endpoints are available
// Note: it is not used with jsoniter, encodePoints takes over
func (p *Point) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("[%v, %v]", int64(p.Ts), p.Value)), nil
}

// Serie holds a timeseries (w/ json serialization to DD API format)
type Serie struct {
	Name           string          `json:"metric"`
	Points         []Point         `json:"points"`
	Tags           []string        `json:"tags"`
	Host           string          `json:"host"`
	Device         string          `json:"device,omitempty"` // FIXME(olivier): remove as soon as the v1 API can handle `device` as a regular tag
	MType          APIMetricType   `json:"type"`
	Interval       int64           `json:"interval"`
	SourceTypeName string          `json:"source_type_name,omitempty"`
	ContextKey     ckey.ContextKey `json:"-"`
	NameSuffix     string          `json:"-"`
}

// Series represents a list of Serie ready to be serialize
type Series []*Serie

// MarshalStrings converts the timeseries to a sorted slice of string slices
func (series Series) MarshalStrings() ([]string, [][]string) {
	var headers = []string{"Metric", "Type", "Timestamp", "Value", "Tags"}
	var payload = make([][]string, len(series))

	for _, serie := range series {
		payload = append(payload, []string{
			serie.Name,
			serie.MType.String(),
			strconv.FormatFloat(serie.Points[0].Ts, 'f', 0, 64),
			strconv.FormatFloat(serie.Points[0].Value, 'f', -1, 64),
			strings.Join(serie.Tags, ", "),
		})
	}

	sort.Slice(payload, func(i, j int) bool {
		// edge cases
		if len(payload[i]) == 0 && len(payload[j]) == 0 {
			return false
		}
		if len(payload[i]) == 0 || len(payload[j]) == 0 {
			return len(payload[i]) == 0
		}
		// sort by metric name
		if payload[i][0] != payload[j][0] {
			return payload[i][0] < payload[j][0]
		}
		// then by timestamp
		if payload[i][2] != payload[j][2] {
			return payload[i][2] < payload[j][2]
		}
		// finally by tags (last field) as tie breaker
		return payload[i][len(payload[i])-1] < payload[j][len(payload[j])-1]
	})

	return headers, payload
}

// populateDeviceField removes any `device:` tag in the series tags and uses the value to
// populate the Serie.Device field
//FIXME(olivier): remove this as soon as the v1 API can handle `device` as a regular tag
func populateDeviceField(serie *Serie) {
	if !hasDeviceTag(serie) {
		return
	}
	// make a copy of the tags array. Otherwise the underlying array won't have
	// the device tag for the Nth iteration (N>1), and the deice field will
	// be lost
	filteredTags := make([]string, 0, len(serie.Tags))

	for _, tag := range serie.Tags {
		if strings.HasPrefix(tag, "device:") {
			serie.Device = tag[7:]
		} else {
			filteredTags = append(filteredTags, tag)
		}
	}

	serie.Tags = filteredTags
}

// hasDeviceTag checks whether a series contains a device tag
func hasDeviceTag(serie *Serie) bool {
	for _, tag := range serie.Tags {
		if strings.HasPrefix(tag, "device:") {
			return true
		}
	}
	return false
}

// MarshalJSON serializes timeseries to JSON so it can be sent to V1 endpoints
//FIXME(maxime): to be removed when v2 endpoints are available
func (series Series) MarshalJSON() ([]byte, error) {
	// use an alias to avoid infinite recursion while serializing a Series
	type SeriesAlias Series
	for _, serie := range series {
		populateDeviceField(serie)
	}

	data := map[string][]*Serie{
		"series": SeriesAlias(series),
	}
	reqBody := &bytes.Buffer{}
	err := json.NewEncoder(reqBody).Encode(data)
	return reqBody.Bytes(), err
}

// SplitPayload breaks the payload into, at least, "times" number of pieces
func (series Series) SplitPayload(times int) ([]marshaler.AbstractMarshaler, error) {
	seriesExpvar.Add("TimesSplit", 1)
	tlmSeries.Inc("times_split")

	// We need to split series without splitting metrics across multiple
	// payload. So we first group series by metric name.
	metricsPerName := map[string]Series{}
	for _, s := range series {
		if _, ok := metricsPerName[s.Name]; ok {
			metricsPerName[s.Name] = append(metricsPerName[s.Name], s)
		} else {
			metricsPerName[s.Name] = Series{s}
		}
	}

	// if we only have one metric name we cannot split further
	if len(metricsPerName) == 1 {
		seriesExpvar.Add("SplitMetricsTooBig", 1)
		tlmSeries.Inc("split_metrics_too_big")
		return nil, fmt.Errorf("Cannot split metric '%s' into %d payload (it contains %d series)", series[0].Name, times, len(series))
	}

	nbSeriesPerPayload := len(series) / times

	payloads := []marshaler.AbstractMarshaler{}
	current := Series{}
	for _, m := range metricsPerName {
		// If on metric is bigger than the targeted size we directly
		// add it as a payload.
		if len(m) >= nbSeriesPerPayload {
			payloads = append(payloads, m)
			continue
		}

		// Then either append to the current payload if "m" is small
		// enough or flush the current payload and start a new one.
		// This may result in more than twice the number of payloads
		// asked for but is "good enough" and will loop only once
		// through metricsPerName
		if len(current)+len(m) < nbSeriesPerPayload {
			current = append(current, m...)
		} else {
			payloads = append(payloads, current)
			current = m
		}
	}
	if len(current) != 0 {
		payloads = append(payloads, current)
	}
	return payloads, nil
}

// MarshalSplitCompress not implemented
func (series Series) MarshalSplitCompress(bufferContext *marshaler.BufferContext) ([]*[]byte, error) {
	return nil, fmt.Errorf("Series MarshalSplitCompress is not implemented")
}

// UnmarshalJSON is a custom unmarshaller for Point (used for testing)
func (p *Point) UnmarshalJSON(buf []byte) error {
	tmp := []interface{}{&p.Ts, &p.Value}
	wantLen := len(tmp)
	if err := json.Unmarshal(buf, &tmp); err != nil {
		return err
	}
	if len(tmp) != wantLen {
		return fmt.Errorf("wrong number of fields in Point: %d != %d", len(tmp), wantLen)
	}
	return nil
}

// String could be used for debug logging
func (e Serie) String() string {
	s, err := json.Marshal(e)
	if err != nil {
		return ""
	}
	return string(s)
}

//// The following methods implement the StreamJSONMarshaler interface
//// for support of the enable_stream_payload_serialization option.

// WriteHeader writes the payload header for this type
func (series Series) WriteHeader(stream *jsoniter.Stream) error {
	stream.WriteObjectStart()
	stream.WriteObjectField("series")
	stream.WriteArrayStart()
	return stream.Flush()
}

// WriteFooter prints the payload footer for this type
func (series Series) WriteFooter(stream *jsoniter.Stream) error {
	stream.WriteArrayEnd()
	stream.WriteObjectEnd()
	return stream.Flush()
}

// WriteItem prints the json representation of an item
func (series Series) WriteItem(stream *jsoniter.Stream, i int) error {
	if i < 0 || i > len(series)-1 {
		return errors.New("out of range")
	}
	serie := series[i]
	populateDeviceField(serie)
	encodeSerie(serie, stream)
	return stream.Flush()
}

// Len returns the number of items to marshal
func (series Series) Len() int {
	return len(series)
}

// DescribeItem returns a text description for logs
func (series Series) DescribeItem(i int) string {
	if i < 0 || i > len(series)-1 {
		return "out of range"
	}
	return fmt.Sprintf("name %q, %d points", series[i].Name, len(series[i].Points))
}

func encodeSerie(serie *Serie, stream *jsoniter.Stream) {
	stream.WriteObjectStart()

	stream.WriteObjectField("metric")
	stream.WriteString(serie.Name)
	stream.WriteMore()

	stream.WriteObjectField("points")
	encodePoints(serie.Points, stream)
	stream.WriteMore()

	stream.WriteObjectField("tags")
	stream.WriteArrayStart()
	firstTag := true
	for _, s := range serie.Tags {
		if !firstTag {
			stream.WriteMore()
		}
		stream.WriteString(s)
		firstTag = false
	}
	stream.WriteArrayEnd()
	stream.WriteMore()

	stream.WriteObjectField("host")
	stream.WriteString(serie.Host)
	stream.WriteMore()

	if serie.Device != "" {
		stream.WriteObjectField("device")
		stream.WriteString(serie.Device)
		stream.WriteMore()
	}

	stream.WriteObjectField("type")
	stream.WriteString(serie.MType.String())
	stream.WriteMore()

	stream.WriteObjectField("interval")
	stream.WriteInt64(serie.Interval)

	if serie.SourceTypeName != "" {
		stream.WriteMore()
		stream.WriteObjectField("source_type_name")
		stream.WriteString(serie.SourceTypeName)
	}

	stream.WriteObjectEnd()
}

func encodePoints(points []Point, stream *jsoniter.Stream) {
	var needComa bool

	stream.WriteArrayStart()
	for _, p := range points {
		if needComa {
			stream.WriteMore()
		} else {
			needComa = true
		}
		stream.WriteArrayStart()
		stream.WriteInt64(int64(p.Ts))
		stream.WriteMore()
		stream.WriteFloat64(p.Value)
		stream.WriteArrayEnd()
	}
	stream.WriteArrayEnd()
}
