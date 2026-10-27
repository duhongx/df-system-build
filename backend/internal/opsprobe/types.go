package opsprobe

import "time"

const Version = "1.0.0"

type Disk struct {
	Device           string  `json:"device"`
	Mountpoint       string  `json:"mountpoint"`
	FSType           string  `json:"fsType"`
	TotalBytes       uint64  `json:"totalBytes"`
	AvailableBytes   uint64  `json:"availableBytes"`
	UsedPercent      float64 `json:"usedPercent"`
	Inodes           uint64  `json:"inodes"`
	InodeUsedPercent float64 `json:"inodeUsedPercent"`
}
type Network struct {
	Name              string  `json:"name"`
	ReceiveBytes      uint64  `json:"receiveBytes"`
	TransmitBytes     uint64  `json:"transmitBytes"`
	ReceivePerSecond  float64 `json:"receivePerSecond"`
	TransmitPerSecond float64 `json:"transmitPerSecond"`
	Errors            uint64  `json:"errors"`
	Drops             uint64  `json:"drops"`
}
type DiskIO struct {
	Device         string   `json:"device"`
	Kind           string   `json:"kind"`
	MappedName     string   `json:"mappedName"`
	ParentDevice   string   `json:"parentDevice"`
	BackingDevices []string `json:"backingDevices"`
	Mountpoints    []string `json:"mountpoints"`
	SizeBytes      uint64   `json:"sizeBytes"`
	ReadPerSecond  float64  `json:"readPerSecond"`
	WritePerSecond float64  `json:"writePerSecond"`
	ReadIOPS       float64  `json:"readIOPS"`
	WriteIOPS      float64  `json:"writeIOPS"`
	Utilization    float64  `json:"utilization"`
	AwaitMs        float64  `json:"awaitMs"`
	ReadAwaitMs    *float64 `json:"readAwaitMs"`
	WriteAwaitMs   *float64 `json:"writeAwaitMs"`
	AverageQueue   float64  `json:"averageQueue"`
	InFlight       uint64   `json:"inFlight"`
}
type CPUPressure struct {
	Avg10  float64 `json:"avg10"`
	Avg60  float64 `json:"avg60"`
	Avg300 float64 `json:"avg300"`
}
type HostMetrics struct {
	Source                     string       `json:"source"`
	SourceURL                  string       `json:"sourceURL"`
	SampleSeconds              float64      `json:"sampleSeconds"`
	ExporterVersion            string       `json:"exporterVersion"`
	CollectorVersion           string       `json:"collectorVersion"`
	CollectedAt                time.Time    `json:"collectedAt"`
	Hostname                   string       `json:"hostname"`
	OS                         string       `json:"os"`
	Kernel                     string       `json:"kernel"`
	CPUUsagePercent            float64      `json:"cpuUsagePercent"`
	CPUCores                   int          `json:"cpuCores"`
	CPUWaitPercent             float64      `json:"cpuWaitPercent"`
	CPUStealPercent            float64      `json:"cpuStealPercent"`
	CPUUserPercent             float64      `json:"cpuUserPercent"`
	CPUSystemPercent           float64      `json:"cpuSystemPercent"`
	RunnableTasks              *uint64      `json:"runnableTasks"`
	BlockedTasks               *uint64      `json:"blockedTasks"`
	CPUSchedulingWaitMs        *float64     `json:"cpuSchedulingWaitMs"`
	CPUIntervalPressurePercent *float64     `json:"cpuIntervalPressurePercent"`
	CPUPressure                *CPUPressure `json:"cpuPressure"`
	CPUPressureError           string       `json:"cpuPressureError"`
	CPUCoreIDs                 []string     `json:"cpuCoreIDs"`
	CPUPerCore                 []float64    `json:"cpuPerCore"`
	Load1                      float64      `json:"load1"`
	Load5                      float64      `json:"load5"`
	Load15                     float64      `json:"load15"`
	MemTotalBytes              uint64       `json:"memTotalBytes"`
	MemAvailableBytes          uint64       `json:"memAvailableBytes"`
	MemUsedBytes               uint64       `json:"memUsedBytes"`
	MemUsagePercent            float64      `json:"memUsagePercent"`
	MemCachedBytes             uint64       `json:"memCachedBytes"`
	SwapTotalBytes             uint64       `json:"swapTotalBytes"`
	SwapUsedBytes              uint64       `json:"swapUsedBytes"`
	UptimeSeconds              float64      `json:"uptimeSeconds"`
	Disks                      []Disk       `json:"disks"`
	Networks                   []Network    `json:"networks"`
	DiskIO                     []DiskIO     `json:"diskIO"`
	Warnings                   []string     `json:"warnings"`
}
type DatabaseConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`
	SSLMode  string `json:"sslMode"`
}
type Activity struct {
	PID                int        `json:"pid"`
	DBName             string     `json:"dbName"`
	Username           string     `json:"username"`
	Application        string     `json:"application"`
	Client             string     `json:"client"`
	State              string     `json:"state"`
	Query              string     `json:"query"`
	QueryStart         *time.Time `json:"queryStart"`
	TransactionStart   *time.Time `json:"transactionStart"`
	ElapsedSeconds     float64    `json:"elapsedSeconds"`
	TransactionSeconds float64    `json:"transactionSeconds"`
	WaitEventType      string     `json:"waitEventType"`
	WaitEvent          string     `json:"waitEvent"`
	BlockingPIDs       []int      `json:"blockingPids"`
}
type Lock struct {
	PID         int        `json:"pid"`
	LockType    string     `json:"lockType"`
	Mode        string     `json:"mode"`
	Granted     bool       `json:"granted"`
	Object      string     `json:"object"`
	WaitStart   *time.Time `json:"waitStart"`
	WaitSeconds float64    `json:"waitSeconds"`
}
type Diagnostic struct {
	CheckedAt      time.Time  `json:"checkedAt"`
	Version        string     `json:"version"`
	Database       string     `json:"database"`
	Username       string     `json:"username"`
	Role           string     `json:"role"`
	Connections    int        `json:"connections"`
	MaxConnections int        `json:"maxConnections"`
	Deadlocks      int64      `json:"deadlocks"`
	StatsReset     *time.Time `json:"statsReset"`
	Activities     []Activity `json:"activities"`
	Locks          []Lock     `json:"locks"`
	Truncated      bool       `json:"truncated"`
	Limit          int        `json:"limit"`
	Warnings       []string   `json:"warnings"`
}
type Check struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Value    string `json:"value"`
	Evidence string `json:"evidence"`
	Advice   string `json:"advice"`
	Error    string `json:"error"`
}
type JobConfig struct {
	Type           string          `json:"type"`
	Name           string          `json:"name"`
	Database       *DatabaseConfig `json:"database,omitempty"`
	Services       []string        `json:"services"`
	TimeoutSeconds int             `json:"timeoutSeconds"`
}
type JobResult struct {
	Version    string       `json:"version"`
	Name       string       `json:"name"`
	Type       string       `json:"type"`
	Hostname   string       `json:"hostname"`
	StartedAt  time.Time    `json:"startedAt"`
	FinishedAt time.Time    `json:"finishedAt"`
	Checks     []Check      `json:"checks"`
	Metrics    *HostMetrics `json:"metrics,omitempty"`
	Database   *Diagnostic  `json:"database,omitempty"`
	Error      string       `json:"error"`
}
