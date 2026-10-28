package opsprobe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"df-build-server/internal/inspection"
	"github.com/jackc/pgx/v5"
)

func ExecuteManualCommand(parent context.Context, command string) inspection.Execution {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	e := inspection.Execution{Kind: "command", Command: command, StartedAt: time.Now(), ExitCode: -1}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd := exec.CommandContext(ctx, "bash", "-o", "pipefail", "-c", command)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "COLUMNS=180")
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	e.FinishedAt = time.Now()
	e.Stdout = inspection.Redact(stdout.String())
	e.Stderr = inspection.Redact(stderr.String())
	if cmd.ProcessState != nil {
		e.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		e.Error = err.Error()
	}
	if ctx.Err() != nil {
		e.Error = ctx.Err().Error()
	}
	return e
}

type manualSQLExecutor struct {
	conn   *pgx.Conn
	config DatabaseConfig
	err    error
}

func newManualSQLExecutor(ctx context.Context, c DatabaseConfig) *manualSQLExecutor {
	conn, err := OpenDatabase(ctx, c)
	return &manualSQLExecutor{conn: conn, config: c, err: err}
}
func (s *manualSQLExecutor) Close() {
	if s.conn != nil {
		_ = s.conn.Close(context.Background())
	}
}

func (s *manualSQLExecutor) Execute(ctx context.Context, code string) (inspection.Execution, inspection.Table) {
	e := inspection.Execution{Kind: "sql", StartedAt: time.Now(), ExitCode: -1}
	t := inspection.Table{Columns: []string{}, Rows: [][]string{}}
	sql, err := manualSQL.ReadFile("manual_sql/" + code + ".sql")
	if err == nil {
		e.Command = string(sql)
	}
	if err == nil {
		err = s.err
	}
	if err != nil {
		e.Error = redactSecret(err.Error(), s.config.Password)
		e.Stderr = e.Error
		e.FinishedAt = time.Now()
		return e, t
	}
	rows, err := s.conn.Query(ctx, e.Command)
	if err != nil {
		e.Error = redactSecret(err.Error(), s.config.Password)
		e.Stderr = e.Error
		e.FinishedAt = time.Now()
		return e, t
	}
	defer rows.Close()
	for _, f := range rows.FieldDescriptions() {
		t.Columns = append(t.Columns, string(f.Name))
	}
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			e.Error = redactSecret(err.Error(), s.config.Password)
			break
		}
		r := make([]string, len(values))
		fields := rows.FieldDescriptions()
		sensitive := false
		for i, v := range values {
			if v != nil {
				if encoded, e := s.conn.TypeMap().Encode(fields[i].DataTypeOID, pgx.TextFormatCode, v, nil); e == nil {
					v = string(encoded)
				}
			}
			switch v := v.(type) {
			case nil:
				r[i] = "NULL"
			case time.Time:
				r[i] = v.Format(time.RFC3339)
			case []byte:
				r[i] = string(v)
			default:
				r[i] = fmt.Sprint(v)
			}
			if sensitiveSetting(r[i]) {
				sensitive = true
			}
			r[i] = inspection.Redact(r[i])
		}
		if sensitive {
			for i, col := range t.Columns {
				if strings.HasPrefix(strings.ToLower(col), "value") || col == "setting" {
					r[i] = "[REDACTED]"
				}
			}
		}
		t.Rows = append(t.Rows, r)
	}
	if err := rows.Err(); err != nil {
		e.Error = redactSecret(err.Error(), s.config.Password)
	}
	var output strings.Builder
	output.WriteString(strings.Join(t.Columns, "\t") + "\n")
	for _, r := range t.Rows {
		output.WriteString(strings.Join(r, "\t") + "\n")
	}
	output.WriteString(fmt.Sprintf("(%d rows)\n", len(t.Rows)))
	e.Stdout = output.String()
	e.FinishedAt = time.Now()
	if e.Error == "" {
		e.ExitCode = 0
	} else {
		e.Stderr = e.Error
	}
	return e, t
}
func sensitiveSetting(s string) bool {
	s = strings.ToLower(s)
	for _, k := range []string{"password", "passphrase", "secret", "token", "primary_conninfo"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}
