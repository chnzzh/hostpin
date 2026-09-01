package collector

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"

	"github.com/chnzzh/hostpin/internal/model"
)

func collectGPUs(ctx context.Context) []model.GPUMetric {
	if metrics := collectNVIDIA(ctx); len(metrics) > 0 {
		return metrics
	}
	return collectAMD(ctx)
}

func collectNVIDIA(ctx context.Context) []model.GPUMetric {
	path, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil
	}
	command := exec.CommandContext(ctx, path,
		"--query-gpu=index,name,utilization.gpu,memory.total,memory.used,temperature.gpu",
		"--format=csv,noheader,nounits")
	output, err := command.Output()
	if err != nil {
		return nil
	}
	reader := csv.NewReader(strings.NewReader(string(output)))
	records, err := reader.ReadAll()
	if err != nil {
		return nil
	}
	result := make([]model.GPUMetric, 0, len(records))
	for _, record := range records {
		if len(record) < 6 {
			continue
		}
		index, _ := strconv.Atoi(strings.TrimSpace(record[0]))
		utilization, _ := strconv.ParseFloat(strings.TrimSpace(record[2]), 64)
		memoryTotal, _ := strconv.ParseUint(strings.TrimSpace(record[3]), 10, 64)
		memoryUsed, _ := strconv.ParseUint(strings.TrimSpace(record[4]), 10, 64)
		temperature, _ := strconv.ParseFloat(strings.TrimSpace(record[5]), 64)
		result = append(result, model.GPUMetric{
			Index: index, Name: strings.TrimSpace(record[1]), Utilization: utilization,
			MemoryTotal: memoryTotal * 1024 * 1024, MemoryUsed: memoryUsed * 1024 * 1024,
			Temperature: temperature,
		})
	}
	return result
}

func collectAMD(ctx context.Context) []model.GPUMetric {
	path, err := exec.LookPath("rocm-smi")
	if err != nil {
		return nil
	}
	output, err := exec.CommandContext(ctx, path, "--showproductname", "--showuse", "--showmeminfo", "vram", "--showtemp", "--json").Output()
	if err != nil {
		return nil
	}
	var payload map[string]map[string]any
	if err := json.Unmarshal(output, &payload); err != nil {
		return nil
	}
	result := make([]model.GPUMetric, 0, len(payload))
	index := 0
	for _, fields := range payload {
		metric := model.GPUMetric{Index: index, Name: stringField(fields, "Card series", "Card model", "Card vendor")}
		metric.Utilization = numberField(fields, "GPU use (%)", "GPU use")
		metric.MemoryTotal = uint64(numberField(fields, "VRAM Total Memory (B)"))
		metric.MemoryUsed = uint64(numberField(fields, "VRAM Total Used Memory (B)"))
		metric.Temperature = numberField(fields, "Temperature (Sensor edge) (C)", "Temperature (Sensor junction) (C)")
		result = append(result, metric)
		index++
	}
	return result
}

func stringField(fields map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := fields[key]; ok {
			return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(toString(value)), "%"))
		}
	}
	return "AMD GPU"
}

func numberField(fields map[string]any, keys ...string) float64 {
	for _, key := range keys {
		if value, ok := fields[key]; ok {
			raw := strings.TrimSpace(strings.TrimSuffix(toString(value), "%"))
			if parsed, err := strconv.ParseFloat(raw, 64); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func toString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}
