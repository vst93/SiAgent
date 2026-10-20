package platform

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// runtimeActiveLimit is the hard ceiling inside the Runtime process: it keeps
// one Node process from admitting unbounded concurrent tasks. It is not a
// product limit; an administrator may configure more, and the extra work waits
// in the platform queue instead of failing.
const runtimeActiveLimit = 32

// Capacity describes what this host is likely to sustain. Every number here is
// advisory: the administrator decides the real limits, and the platform only
// uses these values for the recommendation shown in the UI.
type Capacity struct {
	CPUCores      int               `json:"cpu_cores"`
	MemoryBytes   uint64            `json:"memory_bytes,omitempty"`
	RuntimeActive int               `json:"runtime_active"`
	Recommended   RecommendedLimits `json:"recommended"`
	Note          string            `json:"note,omitempty"`
}

// RecommendedLimits is the suggestion derived from the host, never applied
// automatically: existing installations keep their saved values, and a new one
// starts from the documented defaults.
type RecommendedLimits struct {
	Concurrent         int `json:"concurrent"`
	ConversationQueue  int `json:"conversation_queue"`
	GlobalQueue        int `json:"global_queue"`
	RunTimeoutSeconds  int `json:"run_timeout_seconds"`
	ToolTimeoutSeconds int `json:"tool_timeout_seconds"`
	ToolRounds         int `json:"tool_rounds"`
}

// hostMemoryBytes reads MemTotal on Linux. Other platforms report 0 and the
// recommendation then relies on CPU cores alone.
func hostMemoryBytes() uint64 {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			if kib, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
				return kib * 1024
			}
		}
	}
	return 0
}

// recommendConcurrent sizes concurrency by CPU and memory. Each run holds a
// model context of a few hundred megabytes at most, so memory is budgeted at
// roughly 512 MiB per concurrent task and the smaller of the two estimates wins.
func recommendConcurrent(cores int, memory uint64) int {
	if cores < 1 {
		cores = 1
	}
	byCPU := cores
	if byCPU < 2 {
		byCPU = 2
	}
	if memory == 0 {
		return min(byCPU, runtimeActiveLimit)
	}
	byMemory := int(memory / (512 << 20))
	if byMemory < 1 {
		byMemory = 1
	}
	return max(1, min(min(byCPU, byMemory), runtimeActiveLimit))
}

// capacityReport reports CPU, memory and the derived recommendation.
func capacityReport() Capacity {
	cores := runtime.NumCPU()
	if cores < 1 {
		cores = 1
	}
	memory := hostMemoryBytes()
	concurrent := recommendConcurrent(cores, memory)
	report := Capacity{
		CPUCores:      cores,
		MemoryBytes:   memory,
		RuntimeActive: runtimeActiveLimit,
		Recommended: RecommendedLimits{
			Concurrent:         concurrent,
			ConversationQueue:  concurrent * 5,
			GlobalQueue:        concurrent * 50,
			RunTimeoutSeconds:  300,
			ToolTimeoutSeconds: 60,
			ToolRounds:         10,
		},
	}
	if memory == 0 {
		report.Note = "无法读取内存容量，建议值仅按 CPU 核心数估算。"
	}
	if concurrent >= runtimeActiveLimit {
		report.Note = "建议并发已达到单个 Runtime 进程的同时运行上限；继续调高时多余任务会排队，而不是被拒绝。"
	}
	return report
}
