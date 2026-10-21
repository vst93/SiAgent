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
//
// Host totals and the limits actually enforced on this process are reported
// separately. A container or systemd unit can cap CPU and memory well below the
// machine's real capacity, and showing only one of the two numbers misleads the
// administrator in either direction.
type Capacity struct {
	HostCores     int               `json:"host_cores"`
	HostMemory    uint64            `json:"host_memory_bytes,omitempty"`
	CPUCores      int               `json:"cpu_cores"`
	MemoryBytes   uint64            `json:"memory_bytes,omitempty"`
	CPULimit      string            `json:"cpu_limit,omitempty"`
	MemoryLimit   string            `json:"memory_limit,omitempty"`
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

// perRunMemory is the budget assumed for one concurrent run: model context,
// knowledge excerpts, tool output buffers and the Node heap overhead. It is a
// conservative figure, not a measurement of any particular model.
const perRunMemory = 640 << 20

// hostCores counts every processor the machine exposes. runtime.NumCPU returns
// the affinity mask, so a cgroup or taskset restriction would be reported as if
// the hardware were smaller; the hardware count stays available separately.
func hostCores() int {
	file, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return runtime.NumCPU()
	}
	defer file.Close()
	count := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "processor") {
			count++
		}
	}
	if count < 1 {
		return runtime.NumCPU()
	}
	return count
}

// readMemoryLimit returns the cgroup memory limit in bytes, if one applies.
func readMemoryLimit() (uint64, string) {
	for _, candidate := range []struct{ path, label string }{
		{"/sys/fs/cgroup/memory.max", "cgroup v2"},
		{"/sys/fs/cgroup/memory/memory.limit_in_bytes", "cgroup v1"},
	} {
		raw, err := os.ReadFile(candidate.path)
		if err != nil {
			continue
		}
		value := strings.TrimSpace(string(raw))
		if value == "max" || value == "" {
			continue
		}
		limit, err := strconv.ParseUint(value, 10, 64)
		// cgroup v1 reports a huge sentinel when unlimited.
		if err != nil || limit == 0 || limit > 1<<60 {
			continue
		}
		return limit, candidate.label
	}
	return 0, ""
}

// readCPULimit returns the cgroup CPU quota expressed in whole cores.
func readCPULimit() (int, string) {
	if raw, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		fields := strings.Fields(string(raw))
		if len(fields) == 2 && fields[0] != "max" {
			if quota, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
				if period, err := strconv.ParseInt(fields[1], 10, 64); err == nil && quota > 0 && period > 0 {
					return int((quota + period - 1) / period), "cgroup v2"
				}
			}
		}
	}
	quotaRaw, quotaErr := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	periodRaw, periodErr := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if quotaErr == nil && periodErr == nil {
		quota, qerr := strconv.ParseInt(strings.TrimSpace(string(quotaRaw)), 10, 64)
		period, perr := strconv.ParseInt(strings.TrimSpace(string(periodRaw)), 10, 64)
		if qerr == nil && perr == nil && quota > 0 && period > 0 {
			return int((quota + period - 1) / period), "cgroup v1"
		}
	}
	return 0, ""
}

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

// effectiveCapacity applies affinity and cgroup limits on top of the hardware.
func effectiveCapacity() (cores int, memory uint64, cpuLimit, memoryLimit string) {
	cores = runtime.NumCPU()
	if cores < 1 {
		cores = 1
	}
	if quota, kind := readCPULimit(); quota > 0 && quota < cores {
		cores, cpuLimit = quota, kind
	}
	memory = hostMemoryBytes()
	if limit, kind := readMemoryLimit(); limit > 0 && (memory == 0 || limit < memory) {
		memory, memoryLimit = limit, kind
	}
	return cores, memory, cpuLimit, memoryLimit
}

// recommendConcurrent sizes concurrency from how a run actually behaves. A run
// spends most of its time waiting on the model API, so CPU is not the only
// bound: two tasks per core is a reasonable starting point, memory (one budget
// per run) caps it, and the result is clamped to what one Runtime process can
// hold. Values above this are still allowed; they simply queue.
func recommendConcurrent(cores int, memory uint64) int {
	if cores < 1 {
		cores = 1
	}
	byCPU := cores * 2
	if byCPU < 2 {
		byCPU = 2
	}
	byMemory := runtimeActiveLimit
	if memory > 0 {
		byMemory = int(memory / perRunMemory)
	}
	if byMemory < 2 {
		byMemory = 2
	}
	result := min(byCPU, byMemory, runtimeActiveLimit)
	if result < 2 {
		result = 2
	}
	return result
}

// capacityReport reports hardware, the limits applied to this process and the
// derived recommendation.
func capacityReport() Capacity {
	host := hostCores()
	hostMemory := hostMemoryBytes()
	cores, memory, cpuLimit, memoryLimit := effectiveCapacity()
	concurrent := recommendConcurrent(cores, memory)
	report := Capacity{
		HostCores:     host,
		HostMemory:    hostMemory,
		CPUCores:      cores,
		MemoryBytes:   memory,
		CPULimit:      cpuLimit,
		MemoryLimit:   memoryLimit,
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
	switch {
	case host == 0 && memory == 0:
		report.Note = "无法读取 CPU 与内存容量，建议值按最小配置估算；请按实际负载调整。"
	case memoryLimit != "" || cpuLimit != "":
		report.Note = "本进程受容器或服务单元限制，建议值按受限后的 CPU 与内存计算；宿主实际规格更大，必要时可调高限制或并发。"
	}
	if memory == 0 {
		if report.Note != "" {
			report.Note += " "
		}
		report.Note += "未能读取内存容量，建议值仅按 CPU 估算。"
	}
	if concurrent >= runtimeActiveLimit {
		if report.Note != "" {
			report.Note += " "
		}
		report.Note += "建议并发已达到单个 Runtime 进程的同时运行上限；继续调高时多余任务会排队，而不是被拒绝。"
	}
	return report
}
