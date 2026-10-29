package service

import (
	"context"
	"df-build-server/internal/model"
	"df-build-server/internal/opsremote"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ManagedServiceInput struct {
	ID          uint     `json:"id"`
	Name        string   `json:"name"`
	Mode        string   `json:"mode"`
	Unit        string   `json:"unit"`
	RunUser     string   `json:"runUser"`
	Directory   string   `json:"directory"`
	StartScript string   `json:"startScript"`
	Identity    string   `json:"identity"`
	LogPath     string   `json:"logPath"`
	Operators   []string `json:"operators"`
}

func ServiceCanConfigure(row model.ManagedService, actor string) bool {
	return actor != "" && (actor == "admin" || actor == row.CreatedBy)
}
func ServiceCanOperate(row model.ManagedService, actor string) bool {
	if ServiceCanConfigure(row, actor) {
		return true
	}
	var names []string
	if json.Unmarshal([]byte(row.OperatorsJSON), &names) != nil {
		return false
	}
	for _, name := range names {
		if name == actor && actor != "" {
			return true
		}
	}
	return false
}

func validateManagedService(req ManagedServiceInput) error {
	if strings.TrimSpace(req.Name) == "" || len([]rune(req.Name)) > 128 {
		return fmt.Errorf("请填写服务名称，最多 128 字")
	}
	for _, v := range []string{req.Directory, req.StartScript, req.LogPath} {
		if v != "" && (!path.IsAbs(v) || strings.ContainsAny(v, "\x00\r\n") || len(v) > 512) {
			return fmt.Errorf("服务目录、脚本和日志需填写绝对路径")
		}
	}
	switch req.Mode {
	case "systemd":
		if !regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@-]{0,120}\.service$`).MatchString(req.Unit) {
			return fmt.Errorf("请填写准确的 systemd .service 单元名")
		}
	case "script":
		if req.Directory == "" || req.StartScript == "" {
			return fmt.Errorf("脚本服务需填写工作目录与启动脚本")
		}
		if !regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,63}$`).MatchString(req.RunUser) {
			return fmt.Errorf("请填写实际运行账号")
		}
		if req.Identity == "" || len(req.Identity) > 512 || strings.ContainsAny(req.Identity, "\x00\r\n") {
			return fmt.Errorf("请填写进程命令行中的一个完整参数，例如 OAP 主类或绝对 jar 路径")
		}
		if !strings.HasPrefix(path.Clean(req.StartScript), path.Clean(req.Directory)+"/") {
			return fmt.Errorf("启动脚本必须位于工作目录内")
		}
	default:
		return fmt.Errorf("仅支持 systemd 与已有脚本")
	}
	if len(req.Operators) > 100 {
		return fmt.Errorf("维护人员过多")
	}
	return nil
}

func (s *OperationsService) ManagedServices(ctx context.Context, serverID uint) ([]model.ManagedService, error) {
	var rows []model.ManagedService
	err := s.db.WithContext(ctx).Where("server_id=?", serverID).Order("id").Find(&rows).Error
	return rows, err
}
func (s *OperationsService) SaveManagedService(ctx context.Context, serverID uint, actor string, req ManagedServiceInput) (*model.ManagedService, error) {
	if err := validateManagedService(req); err != nil {
		return nil, err
	}
	var saved model.ManagedService
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var host model.Server
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&host, serverID).Error; err != nil {
			return err
		}
		row := model.ManagedService{ServerID: serverID, CreatedBy: actor}
		if req.ID > 0 {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("server_id=?", serverID).First(&row, req.ID).Error; err != nil {
				return err
			}
			if !ServiceCanConfigure(row, actor) {
				return fmt.Errorf("只有服务配置人或管理员可修改服务")
			}
		} else if actor != "admin" && actor != host.CreatedBy {
			return fmt.Errorf("只有主机创建人或管理员可纳管服务")
		}
		var running int64
		if err := tx.Model(&model.ServiceOperation{}).Where("service_id=? AND status IN ?", row.ID, []string{"QUEUED", "RUNNING"}).Count(&running).Error; err != nil {
			return err
		}
		if running > 0 {
			return fmt.Errorf("服务正在操作，不能修改配置")
		}
		for _, name := range req.Operators {
			var count int64
			if err := tx.Model(&model.User{}).Where("username=?", name).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return fmt.Errorf("维护账号 %s 不存在", name)
			}
		}
		var duplicates int64
		scope := tx.Model(&model.ManagedService{}).Where("server_id=? AND id<>?", serverID, row.ID)
		if req.Mode == "systemd" {
			scope = scope.Where("mode='systemd' AND unit=?", req.Unit)
		} else {
			scope = scope.Where("mode='script' AND run_user=? AND directory=? AND identity=?", req.RunUser, path.Clean(req.Directory), req.Identity)
		}
		if err := scope.Count(&duplicates).Error; err != nil {
			return err
		}
		if duplicates > 0 {
			return fmt.Errorf("此主机上的相同服务已纳管，请维护已有配置")
		}
		row.Name, row.Mode, row.Unit, row.RunUser, row.Directory, row.StartScript, row.Identity, row.LogPath, row.OperatorsJSON = req.Name, req.Mode, req.Unit, req.RunUser, path.Clean(req.Directory), path.Clean(req.StartScript), req.Identity, req.LogPath, mustJSON(req.Operators)
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		saved = row
		return nil
	})
	return &saved, err
}
func (s *OperationsService) DeleteManagedService(ctx context.Context, id uint, actor string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row model.ManagedService
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, id).Error; err != nil {
			return err
		}
		if !ServiceCanConfigure(row, actor) {
			return fmt.Errorf("无权删除服务配置")
		}
		var count int64
		if err := tx.Model(&model.ServiceOperation{}).Where("service_id=? AND status IN ?", id, []string{"QUEUED", "RUNNING"}).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("服务正在操作")
		}
		return tx.Delete(&row).Error
	})
}

// Script services are identified by an exact argv token AND the configured
// effective user and working directory. The agent never uses broad pkill/pgrep.
func serviceProcessScript(row model.ManagedService, action string) string {
	q := opsremote.Quote
	identity := q(row.Identity)
	dir := q(path.Clean(row.Directory))
	user := q(row.RunUser)
	script := `set -eu
 uid=$(id -u -- ` + user + `)
[ "$(id -u)" = 0 ] || [ "$(id -u)" = "$uid" ] || { echo "无法以当前 SSH 账号核对该运行账号的进程" >&2; exit 24; }
workdir=$(readlink -f -- ` + dir + `)
[ -d "$workdir" ] || { echo "工作目录不存在" >&2; exit 24; }
 matches() {
  for d in /proc/[0-9]*; do
   [ -r "$d/cmdline" ] || continue
   [ "$(stat -c %u "$d" 2>/dev/null)" = "$uid" ] || continue
   [ "$(readlink -f "$d/cwd" 2>/dev/null)" = "$workdir" ] || continue
   tr '\000' '\n' < "$d/cmdline" 2>/dev/null | grep -Fqx -- ` + identity + ` || continue
   echo "${d##*/}"
  done
 }
 pids=$(matches)
`
	if action == "status" {
		return script + `if [ -n "$pids" ]; then printf 'RUNNING %s\n' "$pids"; else printf 'STOPPED\n'; fi`
	}
	if action == "restart" {
		script += `if [ -n "$pids" ]; then
 for pid in $pids; do
  # Recheck identity immediately before signalling; signal only the process.
  current=$(matches)
  printf '%s\n' "$current" | grep -Fxq -- "$pid" || exit 21
  kill -TERM -- "$pid"
 done
 n=0
 while [ -n "$(matches)" ]; do n=$((n+1)); [ "$n" -lt 25 ] || { echo '旧进程未退出，未启动新进程'; exit 22; }; sleep 1; done
fi
`
	} else {
		script += `if [ -n "$pids" ]; then echo 'ALREADY_RUNNING'; exit 0; fi
`
	}
	inner := "nohup /bin/sh -c " + q("cd -- "+dir+" && exec /bin/sh "+q(row.StartScript)) + " </dev/null >" + q(path.Join(row.Directory, ".dfops-start.log")) + " 2>&1 &"
	script += `test -f ` + q(row.StartScript) + `
if [ "$(id -un)" = ` + user + ` ]; then /bin/sh -c ` + q(inner) + `; elif [ "$(id -u)" = 0 ]; then runuser -u ` + user + ` -- /bin/sh -c ` + q(inner) + `; else sudo -n -u ` + user + ` /bin/sh -c ` + q(inner) + `; fi
n=0
while [ "$n" -lt 30 ]; do
 pids=$(matches)
 if [ -n "$pids" ]; then sleep 2; pids=$(matches); if [ -n "$pids" ]; then printf 'RUNNING %s\n' "$pids"; exit 0; fi; fi
 n=$((n+1)); sleep 1
done
echo '启动后未发现匹配进程，请查看配置的日志及 .dfops-start.log'
exit 23
`
	return script
}

type ManagedServiceStatus struct {
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	CheckedAt time.Time `json:"checkedAt"`
}

func systemdServiceState(text string) string {
	values := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			values[parts[0]] = strings.TrimSpace(parts[1])
		}
	}
	if values["LoadState"] != "loaded" {
		return "UNKNOWN"
	}
	if values["ActiveState"] == "inactive" || values["ActiveState"] == "failed" {
		return "STOPPED"
	}
	pid, _ := strconv.Atoi(values["MainPID"])
	if values["ActiveState"] == "active" && pid > 0 && values["SubState"] == "running" {
		return "RUNNING"
	}
	return "UNKNOWN"
}

func (s *OperationsService) ManagedServiceStatus(ctx context.Context, id uint) (ManagedServiceStatus, error) {
	var row model.ManagedService
	if err := s.db.First(&row, id).Error; err != nil {
		return ManagedServiceStatus{}, err
	}
	qctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	conn, err := s.connect(qctx, row.ServerID)
	if err != nil {
		return ManagedServiceStatus{Status: "UNKNOWN", Message: err.Error(), CheckedAt: time.Now()}, nil
	}
	defer conn.Close()
	cmd := "/bin/sh -c " + opsremote.Quote(serviceProcessScript(row, "status"))
	if row.Mode == "systemd" {
		cmd = "systemctl show " + opsremote.Quote(row.Unit) + " --property=LoadState,ActiveState,SubState,MainPID --no-pager"
	}
	data, err := conn.Run(qctx, cmd, 8192)
	out := ManagedServiceStatus{Status: "UNKNOWN", CheckedAt: time.Now(), Message: strings.TrimSpace(string(data))}
	if err != nil {
		out.Message = err.Error()
		return out, nil
	}
	if row.Mode == "systemd" {
		out.Status = systemdServiceState(out.Message)
	} else if strings.HasPrefix(out.Message, "RUNNING ") {
		out.Status = "RUNNING"
	} else if out.Message == "STOPPED" {
		out.Status = "STOPPED"
	}
	return out, nil
}

func (s *OperationsService) ServiceLogs(ctx context.Context, id uint, actor string) (string, error) {
	var row model.ManagedService
	if err := s.db.First(&row, id).Error; err != nil {
		return "", err
	}
	if !ServiceCanOperate(row, actor) {
		return "", fmt.Errorf("无权查看此服务日志")
	}
	qctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	conn, err := s.connect(qctx, row.ServerID)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	cmd := ""
	if row.Mode == "systemd" {
		cmd = "journalctl -u " + opsremote.Quote(row.Unit) + " -n 100 --no-pager -o short-iso"
	} else {
		if row.LogPath == "" {
			return "", fmt.Errorf("尚未配置服务日志路径")
		}
		cmd = "tail -n 100 -- " + opsremote.Quote(row.LogPath)
	}
	data, err := conn.Run(qctx, cmd, 128<<10)
	return string(data), err
}

func (s *OperationsService) StartServiceOperation(ctx context.Context, id uint, action, actor string) (*model.ServiceOperation, error) {
	if action != "start" && action != "restart" {
		return nil, fmt.Errorf("仅支持启动与重启")
	}
	var operation model.ServiceOperation
	var row model.ManagedService
	var server model.Server
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, id).Error; err != nil {
			return err
		}
		if !ServiceCanOperate(row, actor) {
			return fmt.Errorf("未授权操作此服务")
		}
		var running int64
		if err := tx.Model(&model.ServiceOperation{}).Where("service_id=? AND status IN ?", id, []string{"QUEUED", "RUNNING"}).Count(&running).Error; err != nil {
			return err
		}
		if running > 0 {
			return fmt.Errorf("该服务已有操作进行中，请等待完成")
		}
		var uncertain int64
		if err := tx.Model(&model.ServiceOperation{}).Where("service_id=? AND status='UNKNOWN' AND started_at>?", id, time.Now().Add(-2*time.Minute)).Count(&uncertain).Error; err != nil {
			return err
		}
		if uncertain > 0 {
			return fmt.Errorf("上次操作结果未确认，先核实服务状态；两分钟内不重复下发")
		}
		if err := tx.First(&server, row.ServerID).Error; err != nil {
			return fmt.Errorf("关联主机不存在")
		}
		operation = model.ServiceOperation{ServiceID: id, ServerID: row.ServerID, TargetName: row.Name, TargetEndpoint: serverEndpoint(server), ConfigurationJSON: mustJSON(map[string]string{"mode": row.Mode, "unit": row.Unit, "runUser": row.RunUser, "directory": row.Directory, "startScript": row.StartScript, "identity": row.Identity}), Action: action, Status: "QUEUED", Operator: actor, StartedAt: time.Now(), Message: "操作已受理"}
		return tx.Create(&operation).Error
	})
	if err != nil {
		return nil, err
	}
	go s.runServiceOperation(row, operation, server)
	return &operation, nil
}
func (s *OperationsService) runServiceOperation(row model.ManagedService, op model.ServiceOperation, server model.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	if result := s.db.Model(&op).Where("status='QUEUED'").Updates(map[string]any{"status": "RUNNING", "message": "连接主机并核对服务"}); result.Error != nil || result.RowsAffected != 1 {
		return
	}
	finish := func(status, message string) {
		now := time.Now()
		s.db.Model(&op).Updates(map[string]any{"status": status, "message": message, "finished_at": now})
	}
	var current model.Server
	if err := s.db.First(&current, server.ID).Error; err != nil || serverEndpoint(current) != op.TargetEndpoint {
		finish("FAILED", "主机连接目标已变化，未执行操作；请重新核对配置")
		return
	}
	conn, err := s.connectServer(ctx, server)
	if err != nil {
		finish("FAILED", "未执行操作："+err.Error())
		return
	}
	defer conn.Close()
	command := "/bin/sh -c " + opsremote.Quote(serviceProcessScript(row, op.Action))
	if row.Mode == "systemd" {
		unit := opsremote.Quote(row.Unit)
		command = "systemctl " + op.Action + " " + unit + " && sleep 2 && systemctl show " + unit + " --property=LoadState,ActiveState,SubState,MainPID --no-pager"
	}
	data, err := conn.Run(ctx, command, 8192)
	if err != nil {
		finish("UNKNOWN", "命令未确认成功，请刷新实际服务状态后决定是否重试："+err.Error())
		return
	}
	message := strings.TrimSpace(string(data))
	if row.Mode == "systemd" {
		if systemdServiceState(message) != "RUNNING" {
			finish("UNKNOWN", "操作已返回，但服务状态未确认："+message)
			return
		}
	} else if !strings.HasPrefix(message, "RUNNING ") && message != "ALREADY_RUNNING" {
		finish("UNKNOWN", "未确认目标进程："+message)
		return
	}
	finish("SUCCEEDED", "服务已运行；"+message)
}

func (s *OperationsService) ServiceOperations(ctx context.Context, id uint) ([]model.ServiceOperation, error) {
	var rows []model.ServiceOperation
	err := s.db.WithContext(ctx).Where("service_id=?", id).Order("id DESC").Limit(20).Find(&rows).Error
	return rows, err
}
