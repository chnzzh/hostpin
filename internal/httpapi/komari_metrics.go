package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/store"
)

type komariMetricDefinition struct {
	Name        string
	Description string
	Type        string
	Unit        string
}

var komariMetricDefinitions = []komariMetricDefinition{
	{Name: "cpu.usage", Description: "CPU usage percentage", Type: "gauge", Unit: "%"},
	{Name: "gpu.usage", Description: "GPU usage percentage", Type: "gauge", Unit: "%"},
	{Name: "gpu.device.usage", Description: "Per-device GPU utilization", Type: "gauge", Unit: "%"},
	{Name: "gpu.memory.used", Description: "GPU memory used", Type: "gauge", Unit: "bytes"},
	{Name: "gpu.memory.total", Description: "GPU memory total", Type: "gauge", Unit: "bytes"},
	{Name: "gpu.temperature", Description: "GPU temperature", Type: "gauge", Unit: "°C"},
	{Name: "memory.used", Description: "RAM used", Type: "gauge", Unit: "bytes"},
	{Name: "swap.used", Description: "Swap used", Type: "gauge", Unit: "bytes"},
	{Name: "load.average", Description: "System load average", Type: "gauge", Unit: ""},
	{Name: "disk.used", Description: "Disk used", Type: "gauge", Unit: "bytes"},
	{Name: "net.in.rate", Description: "Network in rate", Type: "gauge", Unit: "bytes/s"},
	{Name: "net.out.rate", Description: "Network out rate", Type: "gauge", Unit: "bytes/s"},
	{Name: "net.total.up", Description: "Network total upload", Type: "counter", Unit: "bytes"},
	{Name: "net.total.down", Description: "Network total download", Type: "counter", Unit: "bytes"},
	{Name: "traffic.up", Description: "Traffic upload delta", Type: "gauge", Unit: "bytes"},
	{Name: "traffic.down", Description: "Traffic download delta", Type: "gauge", Unit: "bytes"},
	{Name: "process.count", Description: "Process count", Type: "gauge", Unit: "count"},
	{Name: "connections.tcp", Description: "TCP connections", Type: "gauge", Unit: "count"},
	{Name: "connections.udp", Description: "UDP connections", Type: "gauge", Unit: "count"},
	{Name: "ping.latency_ms", Description: "Probe latency", Type: "gauge", Unit: "ms"},
	{Name: "ping.loss", Description: "Probe packet loss indicator", Type: "gauge", Unit: "ratio"},
}

func metricDefinitions() []map[string]any {
	result := make([]map[string]any, 0, len(komariMetricDefinitions))
	for _, item := range komariMetricDefinitions {
		// `name` is the metric key in Komari's RPC2 contract. `key` and `kind`
		// are harmless aliases retained for older third-party themes.
		result = append(result, map[string]any{
			"name": item.Name, "key": item.Name, "description": item.Description,
			"type": item.Type, "kind": item.Type, "unit": item.Unit,
			"retention_days": 7, "metadata": map[string]any{},
		})
	}
	return result
}

func (a *API) rpcQueryMetrics(ctx context.Context, authenticated bool, params map[string]any) (map[string]any, error) {
	metrics := stringSliceParam(params, "metric_keys")
	if len(metrics) == 0 {
		metrics = stringSliceParam(params, "metrics")
	}
	if len(metrics) == 0 {
		if single := stringParam(params, "metric_key"); single != "" {
			metrics = []string{single}
		}
	}
	if len(metrics) == 0 {
		return nil, errors.New("metric key is required")
	}
	for _, metric := range metrics {
		if _, ok := komariMetricDefinitionFor(metric); !ok {
			return nil, fmt.Errorf("unknown metric key: %s", metric)
		}
	}
	nodeIDs := stringSliceParam(params, "entity_ids")
	if len(nodeIDs) == 0 {
		if single := stringParam(params, "entity_id"); single != "" {
			nodeIDs = []string{single}
		}
	}
	if len(nodeIDs) == 0 {
		nodes, err := a.store.ListNodes(ctx, authenticated)
		if err != nil {
			return nil, err
		}
		for _, node := range nodes {
			if authenticated || !node.Hidden {
				nodeIDs = append(nodeIDs, node.ID)
			}
		}
	}
	end := time.Now().UTC()
	if value, ok := rpcTimeParam(params, "end", "end_time"); ok {
		end = value
	}
	hours := numberParam(params, "hours", 4)
	if hours <= 0 {
		hours = 4
	}
	start := end.Add(-time.Duration(hours * float64(time.Hour)))
	if value, ok := rpcTimeParam(params, "start", "start_time"); ok {
		start = value
	}
	if !end.After(start) {
		return nil, errors.New("end must be after start")
	}
	maxPoints := int(numberParam(params, "max_points", 500))
	if maxPoints <= 0 {
		return nil, errors.New("max_points must be a positive integer")
	}
	maxPoints = min(maxPoints, 4000)
	fillEmpty, _ := params["fill_empty"].(bool)

	sampleSets := make(map[string][]model.MetricSample, len(nodeIDs))
	visibleNodeIDs := make([]string, 0, len(nodeIDs))
	seenNodes := make(map[string]struct{}, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		if _, duplicate := seenNodes[nodeID]; duplicate {
			continue
		}
		seenNodes[nodeID] = struct{}{}
		if err := a.ensureVisibleNode(ctx, nodeID, authenticated); err != nil {
			continue
		}
		samples, err := a.store.History(ctx, store.HistoryQuery{NodeID: nodeID, Start: start, End: end, MaxPoints: maxPoints})
		if err != nil {
			return nil, err
		}
		visibleNodeIDs = append(visibleNodeIDs, nodeID)
		sampleSets[nodeID] = samples
	}

	series := make([]map[string]any, 0)
	for _, metric := range metrics {
		definition, _ := komariMetricDefinitionFor(metric)
		for _, nodeID := range visibleNodeIDs {
			if metric == "ping.latency_ms" || metric == "ping.loss" {
				probeSeries, err := a.rpcProbeMetricSeries(ctx, nodeID, metric, definition, start, end, maxPoints, fillEmpty, params)
				if err != nil {
					return nil, err
				}
				series = append(series, probeSeries...)
				continue
			}
			if isKomariGPUDeviceMetric(metric) {
				series = append(series, komariGPUDeviceSeries(nodeID, metric, definition, sampleSets[nodeID], maxPoints)...)
				continue
			}
			points := make([]map[string]any, 0, len(sampleSets[nodeID]))
			for index, sample := range sampleSets[nodeID] {
				value, ok := komariMetricValue(sampleSets[nodeID], index, metric)
				if !ok {
					continue
				}
				points = append(points, komariMetricPoint(sample.ReceivedAt, value, 1))
			}
			series = append(series, komariMetricSeries(metric, nodeID, definition, map[string]string{}, points, maxPoints))
		}
	}
	return map[string]any{
		"start": start, "end": end, "server_downsample_default": true,
		"default_points": 500, "series": series, "count": len(series),
	}, nil
}

func komariMetricDefinitionFor(metric string) (komariMetricDefinition, bool) {
	aliases := map[string]string{
		"cpu": "cpu.usage", "ram": "memory.used", "swap": "swap.used",
		"load": "load.average", "disk": "disk.used", "net_in": "net.in.rate",
		"net_out": "net.out.rate", "process": "process.count", "connections": "connections.tcp",
		"temperature": "host.temperature", "temp": "host.temperature",
		"memory.total": "memory.total", "swap.total": "swap.total", "disk.total": "disk.total",
	}
	canonical := metric
	if alias, ok := aliases[metric]; ok {
		canonical = alias
	}
	if canonical == "host.temperature" {
		return komariMetricDefinition{Name: metric, Description: "Host temperature", Type: "gauge", Unit: "°C"}, true
	}
	if canonical == "memory.total" || canonical == "swap.total" || canonical == "disk.total" {
		return komariMetricDefinition{Name: metric, Description: canonical, Type: "gauge", Unit: "bytes"}, true
	}
	for _, definition := range komariMetricDefinitions {
		if definition.Name == canonical {
			definition.Name = metric
			return definition, true
		}
	}
	return komariMetricDefinition{}, false
}

func komariMetricPoint(at time.Time, value any, count int) map[string]any {
	return map[string]any{"time": at.UTC(), "value": value, "count": count}
}

func komariMetricSeries(metric, nodeID string, definition komariMetricDefinition, tags map[string]string, points []map[string]any, maxPoints int) map[string]any {
	return map[string]any{
		"metric_key": metric, "entity_id": nodeID, "type": definition.Type,
		"unit": definition.Unit, "retention_days": 7, "tags": tags,
		"downsampled": false, "max_points": maxPoints, "count": len(points), "points": points,
	}
}

func rpcTimeParam(params map[string]any, keys ...string) (time.Time, bool) {
	for _, key := range keys {
		raw := strings.TrimSpace(stringParam(params, key))
		if raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func komariMetricValue(samples []model.MetricSample, index int, metric string) (float64, bool) {
	sample := samples[index]
	switch metric {
	case "cpu", "cpu.usage":
		return sample.CPU, true
	case "gpu.usage":
		if len(sample.GPUs) == 0 {
			return 0, true
		}
		value := 0.0
		for _, gpu := range sample.GPUs {
			value += gpu.Utilization
		}
		return value / float64(len(sample.GPUs)), true
	case "ram", "memory.used":
		return float64(sample.MemoryUsed), true
	case "memory.total":
		return float64(sample.MemoryTotal), true
	case "swap", "swap.used":
		return float64(sample.SwapUsed), true
	case "swap.total":
		return float64(sample.SwapTotal), true
	case "load", "load.average":
		return sample.Load1, true
	case "temperature", "temp":
		return sample.Temperature, true
	case "disk", "disk.used":
		return float64(sample.DiskUsed), true
	case "disk.total":
		return float64(sample.DiskTotal), true
	case "net_in", "net.in.rate":
		return sample.NetRxBPS, true
	case "net_out", "net.out.rate":
		return sample.NetTxBPS, true
	case "net.total.up":
		return float64(sample.NetTxBytes), true
	case "net.total.down":
		return float64(sample.NetRxBytes), true
	case "traffic.up":
		return float64(counterDeltaAt(samples, index, true)), true
	case "traffic.down":
		return float64(counterDeltaAt(samples, index, false)), true
	case "process", "process.count":
		return float64(sample.Processes), true
	case "connections", "connections.tcp":
		return float64(sample.TCPConnections), true
	case "connections.udp":
		return float64(sample.UDPConnections), true
	default:
		return 0, false
	}
}

func counterDeltaAt(samples []model.MetricSample, index int, upload bool) uint64 {
	if index <= 0 {
		return 0
	}
	current, previous := samples[index].NetRxBytes, samples[index-1].NetRxBytes
	if upload {
		current, previous = samples[index].NetTxBytes, samples[index-1].NetTxBytes
	}
	if current >= previous {
		return current - previous
	}
	return current
}

func isKomariGPUDeviceMetric(metric string) bool {
	return metric == "gpu.device.usage" || metric == "gpu.memory.used" || metric == "gpu.memory.total" || metric == "gpu.temperature"
}

func komariGPUDeviceSeries(nodeID, metric string, definition komariMetricDefinition, samples []model.MetricSample, maxPoints int) []map[string]any {
	type deviceSeries struct {
		name   string
		points []map[string]any
	}
	devices := make(map[int]*deviceSeries)
	order := make([]int, 0)
	for _, sample := range samples {
		for position, gpu := range sample.GPUs {
			index := gpu.Index
			if index < 0 {
				index = position
			}
			device := devices[index]
			if device == nil {
				device = &deviceSeries{name: gpu.Name, points: make([]map[string]any, 0, len(samples))}
				devices[index] = device
				order = append(order, index)
			}
			var value float64
			switch metric {
			case "gpu.device.usage":
				value = gpu.Utilization
			case "gpu.memory.used":
				value = float64(gpu.MemoryUsed)
			case "gpu.memory.total":
				value = float64(gpu.MemoryTotal)
			case "gpu.temperature":
				value = gpu.Temperature
			}
			device.points = append(device.points, komariMetricPoint(sample.ReceivedAt, value, 1))
		}
	}
	sort.Ints(order)
	result := make([]map[string]any, 0, max(1, len(order)))
	for _, index := range order {
		device := devices[index]
		tags := map[string]string{"device_index": strconv.Itoa(index), "device_name": device.name}
		result = append(result, komariMetricSeries(metric, nodeID, definition, tags, device.points, maxPoints))
	}
	if len(result) == 0 {
		result = append(result, komariMetricSeries(metric, nodeID, definition, map[string]string{}, []map[string]any{}, maxPoints))
	}
	return result
}

func (a *API) rpcProbeMetricSeries(ctx context.Context, nodeID, metric string, definition komariMetricDefinition, start, end time.Time, maxPoints int, fillEmpty bool, params map[string]any) ([]map[string]any, error) {
	tasks, err := a.store.ListProbeTasks(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	requestedTask := ""
	if tags, ok := params["tags"].(map[string]any); ok {
		if taskID, exists := tags["task_id"]; exists && taskID != nil {
			requestedTask = strings.TrimSpace(fmt.Sprint(taskID))
		}
	}
	byTask := make(map[int64][]map[string]any, len(tasks))
	taskMap := make(map[int64]model.ProbeTask, len(tasks))
	for _, task := range tasks {
		if requestedTask != "" && requestedTask != strconv.FormatInt(task.ID, 10) {
			continue
		}
		byTask[task.ID] = []map[string]any{}
		taskMap[task.ID] = task
	}
	limit := min(20000, maxPoints*max(1, len(taskMap)))
	records, err := a.store.ProbeHistory(ctx, nodeID, 0, start, end, limit)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if _, ok := taskMap[record.TaskID]; !ok {
			continue
		}
		value := record.LatencyMS
		if metric == "ping.loss" {
			value = effectiveProbeLoss(record) / 100
		} else if !record.Success {
			value = -1
		}
		var pointValue any = value
		if fillEmpty && metric == "ping.latency_ms" && value < 0 {
			pointValue = nil
		}
		byTask[record.TaskID] = append(byTask[record.TaskID], komariMetricPoint(record.ReceivedAt, pointValue, 1))
	}
	result := make([]map[string]any, 0, max(1, len(taskMap)))
	for _, task := range tasks {
		points, ok := byTask[task.ID]
		if !ok {
			continue
		}
		tags := map[string]string{"task_id": strconv.FormatInt(task.ID, 10)}
		result = append(result, komariMetricSeries(metric, nodeID, definition, tags, points, maxPoints))
	}
	if len(result) == 0 {
		result = append(result, komariMetricSeries(metric, nodeID, definition, map[string]string{}, []map[string]any{}, maxPoints))
	}
	return result, nil
}

func (a *API) rpcPingStats(ctx context.Context, params map[string]any, authenticated bool) (map[string]any, error) {
	end := time.Now().UTC()
	if value, ok := rpcTimeParam(params, "end", "end_time"); ok {
		end = value
	}
	hours := numberParam(params, "hours", 4)
	if hours <= 0 {
		hours = 4
	}
	start := end.Add(-time.Duration(hours * float64(time.Hour)))
	if value, ok := rpcTimeParam(params, "start", "start_time"); ok {
		start = value
	}
	if !end.After(start) {
		return nil, errors.New("end must be after start")
	}
	nodeIDs := stringSliceParam(params, "entity_ids")
	if len(nodeIDs) == 0 {
		if single := firstNonEmpty(stringParam(params, "entity_id"), stringParam(params, "uuid")); single != "" {
			nodeIDs = []string{single}
		}
	}
	if len(nodeIDs) == 0 {
		nodes, err := a.store.ListNodes(ctx, authenticated)
		if err != nil {
			return nil, err
		}
		for _, node := range nodes {
			if authenticated || !node.Hidden {
				nodeIDs = append(nodeIDs, node.ID)
			}
		}
	}
	tasks, err := a.store.ListProbeTasks(ctx, "")
	if err != nil {
		return nil, err
	}
	taskMap := make(map[int64]model.ProbeTask, len(tasks))
	for _, task := range tasks {
		taskMap[task.ID] = task
	}
	taskFilter := rpcTaskIDFilter(params)
	type groupKey struct {
		nodeID string
		taskID int64
	}
	groups := make(map[groupKey][]model.ProbeResult)
	seenNodes := make(map[string]struct{}, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		if _, duplicate := seenNodes[nodeID]; duplicate {
			continue
		}
		seenNodes[nodeID] = struct{}{}
		if err := a.ensureVisibleNode(ctx, nodeID, authenticated); err != nil {
			continue
		}
		records, err := a.store.ProbeHistory(ctx, nodeID, 0, start, end, 20000)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			if len(taskFilter) > 0 && !taskFilter[record.TaskID] {
				continue
			}
			groups[groupKey{nodeID: nodeID, taskID: record.TaskID}] = append(groups[groupKey{nodeID: nodeID, taskID: record.TaskID}], record)
		}
	}
	stats := make([]map[string]any, 0, len(groups))
	for key, records := range groups {
		stat := komariPingStat(key.nodeID, key.taskID, taskMap[key.taskID], records)
		stats = append(stats, stat)
	}
	sort.Slice(stats, func(i, j int) bool {
		leftNode, rightNode := fmt.Sprint(stats[i]["entity_id"]), fmt.Sprint(stats[j]["entity_id"])
		if leftNode != rightNode {
			return leftNode < rightNode
		}
		return fmt.Sprint(stats[i]["task_id"]) < fmt.Sprint(stats[j]["task_id"])
	})
	maxPoints := int(numberParam(params, "max_points", 500))
	if maxPoints <= 0 {
		maxPoints = 500
	}
	interval := math.Ceil(end.Sub(start).Seconds() / float64(maxPoints))
	if interval < 1 {
		interval = 1
	}
	return map[string]any{"start": start, "end": end, "interval_seconds": interval, "stats": stats, "count": len(stats)}, nil
}

func rpcTaskIDFilter(params map[string]any) map[int64]bool {
	result := make(map[int64]bool)
	add := func(value any) {
		var raw string
		switch typed := value.(type) {
		case nil:
			return
		case float64:
			raw = strconv.FormatInt(int64(typed), 10)
		default:
			raw = strings.TrimSpace(fmt.Sprint(typed))
		}
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			result[parsed] = true
		}
	}
	add(params["task_id"])
	if values, ok := params["task_ids"].([]any); ok {
		for _, value := range values {
			add(value)
		}
	}
	return result
}

func komariPingStat(nodeID string, taskID int64, task model.ProbeTask, records []model.ProbeResult) map[string]any {
	values := make([]float64, 0, len(records))
	sum := 0.0
	lossTotal := 0.0
	var latest *float64
	for _, record := range records {
		lossTotal += effectiveProbeLoss(record)
		if !record.Success || record.LatencyMS < 0 {
			continue
		}
		value := record.LatencyMS
		values = append(values, value)
		sum += value
		latest = &value
	}
	result := map[string]any{
		"entity_id": nodeID, "task_id": strconv.FormatInt(taskID, 10),
		"tags":  map[string]string{"task_id": strconv.FormatInt(taskID, 10)},
		"total": len(records), "valid": len(values),
	}
	if len(records) > 0 {
		result["loss"] = lossTotal / float64(len(records))
	} else {
		result["loss"] = 0.0
	}
	if task.ID != 0 {
		result["name"], result["type"], result["interval"] = task.Name, string(task.Type), task.IntervalSeconds
	}
	if len(values) == 0 {
		return result
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	average := sum / float64(len(values))
	p50, p99 := percentile(sorted, 0.50), percentile(sorted, 0.99)
	variance := 0.0
	for _, value := range values {
		delta := value - average
		variance += delta * delta
	}
	result["min"], result["max"], result["avg"] = sorted[0], sorted[len(sorted)-1], average
	result["latest"], result["p50"], result["p99"] = *latest, p50, p99
	result["stddev"] = math.Sqrt(variance / float64(len(values)))
	if p50 > 0 && p99 >= p50 {
		result["p99_p50_ratio"] = (p99 - p50) / math.Max(math.Min(p50, 50), 10)
	} else {
		result["p99_p50_ratio"] = 0.0
	}
	return result
}

func percentile(sorted []float64, fraction float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 || fraction <= 0 {
		return sorted[0]
	}
	if fraction >= 1 {
		return sorted[len(sorted)-1]
	}
	position := float64(len(sorted)-1) * fraction
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}
	return sorted[lower] + (sorted[upper]-sorted[lower])*(position-float64(lower))
}

func (a *API) ensureVisibleNode(ctx context.Context, nodeID string, authenticated bool) error {
	if nodeID == "" {
		return errors.New("uuid is required")
	}
	node, err := a.store.GetNode(ctx, nodeID)
	if err != nil || (node.Hidden && !authenticated) {
		return errors.New("node not found")
	}
	return nil
}

func stringParam(params map[string]any, key string) string {
	value, ok := params[key]
	if !ok || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func numberParam(params map[string]any, key string, fallback float64) float64 {
	value, ok := params[key]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case float64:
		return typed
	case json.Number:
		parsed, _ := typed.Float64()
		return parsed
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func stringSliceParam(params map[string]any, key string) []string {
	value, ok := params[key]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			result = append(result, fmt.Sprint(item))
		}
		return result
	case string:
		return []string{typed}
	default:
		return nil
	}
}

func parseHours(raw string) float64 {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value <= 0 {
		return 4
	}
	return min(value, 24*365)
}
