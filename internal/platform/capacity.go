package platform

import (
	"bufio"
	"os"
	"path/filepath"
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
// Hardware totals and the limits actually enforced on this process are reported
// separately. A container or a systemd unit can cap CPU and memory well below
// the machine's real capacity, and showing only one of the two numbers misleads
// the administrator in either direction.
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

// Memory budget assumptions, documented so the recommendation can be argued
// with rather than taken on faith. Measured from the Pi Runtime: a conversation
// keeps its SQLite journal, the last compaction summary and the live tool
// buffers in memory, and every concurrent run holds its own Node promise chain,
// stream decoder and up to the model's max_tokens of pending output.
const (
	// runBaseBudget is a conservative per-run reservation: prompt and message
	// buffers, the stream decoder, pending completion text and tool output. Real
	// usage is frequently lower, but a recommendation that assumes the floor
	// keeps a small server responsive under several long answers at once.
	runBaseBudget = 512 << 20
	// runtimeBaseBudget covers the Node process itself plus the MCP client and
	// the per-conversation SQLite journal caches.
	runtimeBaseBudget = 256 << 20
	// osReserve is the floor of memory left to the operating system and other
	// services; the larger of this and 25% of memory is used.
	osReserve = 512 << 20
	// minConcurrent / maxConcurrent bound the suggestion. Two keeps a chat usable
	// while a long tool call blocks the other slot; above eight one Node process
	// spends more time scheduling than waiting on the model API.
	minConcurrent = 2
	maxConcurrent = 8
)

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

// cgroupRoots lists the places a cgroup hierarchy may be mounted. A process is
// not always at /sys/fs/cgroup: the host, a container runtime and a user
// service manager each mount it differently, and readings from the root only
// describe the leaf when the process happens to live there.
func cgroupRoots() []string {
	roots := []string{"/sys/fs/cgroup"}
	if raw, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 5 || fields[len(fields)-3] != "cgroup2" && fields[len(fields)-3] != "cgroup" {
				continue
			}
			if root := fields[4]; root != "" && root != "/sys/fs/cgroup" {
				roots = append(roots, root)
			}
		}
	}
	return roots
}

// cgroupDirs returns the process's own cgroup directory and its ancestors,
// deepest first, for every mounted hierarchy. Limits are inherited, so the
// tightest value in the chain is the one that actually applies.
func cgroupDirs() []string {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return cgroupRoots()
	}
	paths := []string{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 || fields[2] == "" {
			continue
		}
		paths = append(paths, fields[2])
	}
	dirs := []string{}
	for _, root := range cgroupRoots() {
		for _, path := range paths {
			dir := filepath.Join(root, filepath.Clean("/"+path))
			for {
				dirs = append(dirs, dir)
				parent := filepath.Dir(dir)
				if parent == root || len(parent) <= len(root) || dir == root {
					break
				}
				dir = parent
			}
		}
	}
	return dirs
}

// readMemoryLimit walks the cgroup chain and returns the smallest limit found.
func readMemoryLimit() (uint64, string) {
	best := uint64(0)
	source := ""
	for _, dir := range cgroupDirs() {
		for _, candidate := range []struct{ name, label string }{
			{"memory.max", "cgroup v2"},
			{"memory/memory.limit_in_bytes", "cgroup v1"},
		} {
			raw, err := os.ReadFile(filepath.Join(dir, candidate.name))
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
			if best == 0 || limit < best {
				best, source = limit, candidate.label
			}
		}
	}
	return best, source
}

// readCPULimit walks the cgroup chain and returns the smallest quota in whole
// cores. A missing quota means "unlimited" and inherits from the parent.
func readCPULimit() (int, string) {
	best := 0
	source := ""
	for _, dir := range cgroupDirs() {
		quota, label := cpuQuotaIn(dir)
		if quota == 0 {
			continue
		}
		if best == 0 || quota < best {
			best, source = quota, label
		}
	}
	return best, source
}

func cpuQuotaIn(dir string) (int, string) {
	if raw, err := os.ReadFile(filepath.Join(dir, "cpu.max")); err == nil {
		fields := strings.Fields(string(raw))
		if len(fields) == 2 && fields[0] != "max" {
			quota, qerr := strconv.ParseInt(fields[0], 10, 64)
			period, perr := strconv.ParseInt(fields[1], 10, 64)
			if qerr == nil && perr == nil && quota > 0 && period > 0 {
				return int((quota + period - 1) / period), "cgroup v2"
			}
		}
	}
	quotaRaw, quotaErr := os.ReadFile(filepath.Join(dir, "cpu/cpu.cfs_quota_us"))
	periodRaw, periodErr := os.ReadFile(filepath.Join(dir, "cpu/cpu.cfs_period_us"))
	if quotaErr == nil && periodErr == nil {
		quota, qerr := strconv.ParseInt(strings.TrimSpace(string(quotaRaw)), 10, 64)
		period, perr := strconv.ParseInt(strings.TrimSpace(string(periodRaw)), 10, 64)
		if qerr == nil && perr == nil && quota > 0 && period > 0 {
			return int((quota + period - 1) / period), "cgroup v1"
		}
	}
	return 0, ""
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

// recommendConcurrent sizes concurrency from how a run actually behaves.
//
// A run is mostly waiting: one model request in flight, occasionally a tool
// call. CPU therefore is not the binding constraint for the common case, and
// "one task per core" wastes a machine that is only waiting on the network.
// Memory is the harder constraint, because every concurrent run keeps its own
// prompt buffer, stream decoder, tool output and pending completion.
//
// The budget is what remains after the Runtime process and the operating
// system, divided by a documented per-run figure:
//
//	available = memory - max(25% of memory, 512 MiB) - 256 MiB runtime base
//	byMemory  = available / 512 MiB per run
//	byCPU     = cores * 2   (a run that blocks on a tool still needs CPU)
//	result    = clamp(min(byMemory, byCPU), 2, 8)
//
// The resulting curve: 2 cores/2 GiB gives 2, 4/4 GiB gives 5, 4/8 GiB or
// 8/16 GiB gives 8.
//
// The upper bound of eight is a practical ceiling for one Node process: beyond
// it the event loop, the SQLite journal per conversation and the streaming
// decoders start competing more than the model API does. Administrators can
// configure more; those tasks queue rather than fail.
func recommendConcurrent(cores int, memory uint64) int {
	if cores < 1 {
		cores = 1
	}
	byCPU := cores * 2
	if byCPU < minConcurrent {
		byCPU = minConcurrent
	}
	if memory == 0 {
		// Without a memory reading, stay deliberately low: guessing memory would
		// be worse than admitting the gap, and the UI states the gap.
		return max(minConcurrent, min(byCPU, 4))
	}
	reserve := memory / 4
	if reserve < osReserve {
		reserve = osReserve
	}
	if memory <= reserve+runtimeBaseBudget {
		return minConcurrent
	}
	byMemory := int((memory - reserve - runtimeBaseBudget) / runBaseBudget)
	result := min(byMemory, byCPU, maxConcurrent)
	if result < minConcurrent {
		result = minConcurrent
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
	notes := []string{}
	if memory == 0 {
		notes = append(notes, "未能读取内存容量，建议值仅按 CPU 估算。")
	}
	if cpuLimit != "" || memoryLimit != "" {
		notes = append(notes, "本进程受容器或服务单元限制，建议值按受限后的 CPU 与内存计算；宿主实际规格更大。")
	}
	if concurrent >= maxConcurrent {
		notes = append(notes, "建议并发已到单进程的实用上限；继续调高时多余任务会排队，而不是被拒绝。")
	}
	if len(notes) > 0 {
		report.Note = strings.Join(notes, " ")
	}
	return report
}
