// inspection-runner is a bounded, read-only executable sent to managed Linux hosts.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"df-build-server/internal/opsprobe"
)

type progress struct {
	Status  string    `json:"status"`
	Stage   string    `json:"stage"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
	PID     int       `json:"pid"`
	Version string    `json:"version"`
}

func writeJSON(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	pending := path + ".tmp"
	if err := os.WriteFile(pending, b, 0600); err != nil {
		return err
	}
	return os.Rename(pending, path)
}
func main() {
	mode := flag.String("mode", "sample", "sample, job, cancel")
	dir := flag.String("dir", "", "private job directory")
	flag.Parse()
	if err := run(*mode, *dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(mode, dir string) error {
	if mode == "sample" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		m, err := opsprobe.CollectHost(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(m)
	}
	if dir == "" || !filepath.IsAbs(dir) {
		return fmt.Errorf("job directory must be absolute")
	}
	if mode == "cancel" {
		return os.WriteFile(filepath.Join(dir, "cancel"), []byte("cancel"), 0600)
	}
	if mode != "job" {
		return fmt.Errorf("unknown mode")
	}
	resultPath := filepath.Join(dir, "result.json")
	if _, err := os.Stat(resultPath); err == nil {
		return nil
	}
	lock, err := os.OpenFile(filepath.Join(dir, "job.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("job already claimed: %w", err)
	}
	fmt.Fprint(lock, os.Getpid())
	lock.Close()
	cfgFile := filepath.Join(dir, "config.json")
	f, err := os.Open(cfgFile)
	if err != nil {
		return err
	}
	var cfg opsprobe.JobConfig
	err = json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&cfg)
	f.Close()
	_ = os.Remove(cfgFile)
	if err != nil {
		return fmt.Errorf("invalid job configuration")
	}
	if cfg.Type != "host" && cfg.Type != "database" {
		return fmt.Errorf("invalid inspection type")
	}
	if cfg.TimeoutSeconds < 10 || cfg.TimeoutSeconds > 300 {
		cfg.TimeoutSeconds = 120
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	stage := "准备检查"
	message := ""
	state := "RUNNING"
	emit := func() {
		_ = writeJSON(filepath.Join(dir, "status.json"), progress{state, stage, message, time.Now(), os.Getpid(), opsprobe.Version})
	}
	// Only this goroutine writes progress, keeping snapshots and heartbeats coherent.
	updates := make(chan [2]string, 10)
	stop := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		emit()
		for {
			select {
			case change := <-updates:
				stage, message = change[0], change[1]
				emit()
			case <-ticker.C:
				if _, err := os.Stat(filepath.Join(dir, "cancel")); err == nil {
					cancel()
				}
				emit()
			case <-stop:
				return
			}
		}
	}()
	result := opsprobe.JobResult{Version: opsprobe.Version, Name: cfg.Name, Type: cfg.Type, StartedAt: time.Now(), Checks: []opsprobe.Check{}}
	result.Hostname, _ = os.Hostname()
	if cfg.Manual != nil {
		if cfg.Type == "host" {
			updates <- [2]string{"采集 Linux 基础资源", "CPU、内存、实际文件系统与网络"}
			metrics, err := opsprobe.CollectHost(ctx)
			if err != nil {
				result.Checks = append(result.Checks, opsprobe.Check{Code: "host_collection", Name: "Linux 基础资源", Status: "UNKNOWN", Error: err.Error()})
			} else {
				result.Metrics = metrics
				result.Checks = append(result.Checks, opsprobe.HostChecks(metrics)...)
			}
		}
		updates <- [2]string{"按指导书执行检查", "记录实际命令／SQL、返回输出、错误与退出码"}
		checks, e := opsprobe.CollectDeploymentAwareManual(ctx, *cfg.Manual, cfg.Database, cfg.HostDatabases, opsprobe.ExecuteManualCommand)
		result.ReportChecks = checks
		if e != nil {
			result.Error = e.Error()
		}
		if cfg.Database != nil {
			cfg.Database.Password = ""
		}
		for _, binding := range cfg.HostDatabases {
			if binding.Config != nil {
				binding.Config.Password = ""
			}
		}
	} else if cfg.Type == "host" {
		updates <- [2]string{"采集主机资源", "CPU、内存、磁盘和网络"}
		m, e := opsprobe.CollectHost(ctx)
		if e != nil {
			result.Error = e.Error()
			result.Checks = append(result.Checks, opsprobe.Check{Code: "host_collection", Name: "主机采集", Status: "UNKNOWN", Error: e.Error()})
		} else {
			result.Metrics = m
			result.Checks = append(result.Checks, opsprobe.HostChecks(m)...)
		}
		for _, name := range cfg.Services {
			if len(cfg.Services) > 20 || !regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,128}$`).MatchString(name) {
				result.Checks = append(result.Checks, opsprobe.Check{Code: "service_config", Name: "服务配置", Status: "UNKNOWN", Error: "服务名称不合法或超过 20 项"})
				break
			}
			updates <- [2]string{"检查服务", name}
			checkCtx, done := context.WithTimeout(ctx, 5*time.Second)
			result.Checks = append(result.Checks, opsprobe.ServiceCheck(checkCtx, name))
			done()
		}
	} else {
		updates <- [2]string{"检查数据库", "身份、锁等待、长事务和当前慢 SQL"}
		if cfg.Database == nil {
			result.Error = "未提供数据库连接"
		} else {
			d, e := opsprobe.Diagnose(ctx, *cfg.Database)
			if e != nil {
				result.Error = e.Error()
				result.Checks = append(result.Checks, opsprobe.Check{Code: "database_collection", Name: "数据库采集", Status: "UNKNOWN", Error: e.Error()})
			} else {
				result.Database = d
				result.Checks = append(result.Checks, opsprobe.DatabaseChecks(d)...)
			}
			updates <- [2]string{"采集报告证据", "只读 SQL、配置、复制、归档与 Top5"}
			result.ReportChecks = opsprobe.InspectionDatabase(ctx, *cfg.Database)
			cfg.Database.Password = ""
		}
	}
	if ctx.Err() != nil {
		result.Error = "检查已取消或达到超时限制: " + ctx.Err().Error()
	}
	result.FinishedAt = time.Now()
	close(stop)
	<-finished
	if err := writeJSON(resultPath, result); err != nil {
		return err
	}
	state = "COMPLETED"
	if result.Error != "" {
		state = "FAILED"
	}
	if ctx.Err() == context.Canceled {
		state = "CANCELED"
	}
	stage = "检查结束"
	message = result.Error
	emit()
	return nil
}
