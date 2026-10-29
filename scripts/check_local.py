#!/usr/bin/env python3
"""Run local checks with a disposable PostgreSQL; never uses a configured server."""

import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import tempfile


def backend_results(path):
    passed, skipped, failures, packages = [], [], [], []
    for line in path.read_text().splitlines():
        try:
            item = json.loads(line)
        except ValueError:
            continue
        action, name = item.get("Action"), item.get("Test")
        if action == "pass" and name:
            passed.append(name)
        elif action == "skip" and name:
            skipped.append({"package": item["Package"], "test": name})
        elif action == "fail":
            failures.append({"package": item["Package"], "test": name})
        elif action == "pass" and not name:
            packages.append(item["Package"])
    return {"passedCasesIncludingSubtests": len(passed),
            "passedTopLevelTests": sum("/" not in name for name in passed),
            "passedPackages": packages, "skipped": skipped, "failures": failures}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--font", default=os.environ.get("DFOPS_REPORT_FONT"))
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[1]
    font = Path(args.font).resolve() if args.font else repo / "output/fonts/DroidSansFallback.ttf"
    if not font.is_file():
        parser.error("请用 --font 或 DFOPS_REPORT_FONT 指定覆盖中文的 TrueType 字体")
    required = ["initdb", "pg_ctl", "createdb", "openssl", "go", "npm", "bash", "git"]
    missing = [name for name in required if not shutil.which(name)]
    if missing:
        parser.error("缺少本地开发工具：" + ", ".join(missing))
    if os.geteuid() == 0:
        parser.error("本地 PostgreSQL 需要普通开发账号，请不要用 root 执行")

    os.umask(0o077)
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    output = repo / "output/selfcheck" / f"{stamp}-{os.getpid()}"
    output.mkdir(parents=True)
    env = os.environ.copy()
    for key in ["DF_DEPLOYMENT_CONFIG_CLUSTER_TEST", "DF_DEPLOYMENT_CONFIG_CLUSTER_SETTINGS",
                "DF_FRONTEND_CLUSTER_TEST", "DF_FRONTEND_CLUSTER_SETTINGS", "DFOPS_TEST_EXPORTER",
                "DATABASE_DSN", "DB_HOST", "DB_PORT", "DB_NAME", "DB_USER", "DB_PASSWORD",
                "CONFIG_PATH", "DOCKER_HOST", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"]:
        env.pop(key, None)

    def run(name, command, cwd=repo, check=False):
        with (output / f"{name}.log").open("w") as log:
            result = subprocess.run(command, cwd=cwd, env=env, stdout=log, stderr=subprocess.STDOUT)
        if check and result.returncode:
            raise RuntimeError(f"{name} 失败，详情见 {output / (name + '.log')}")
        return result.returncode

    summary = {"scope": "disposable local PostgreSQL and local fixtures; no deployment",
               "gitBase": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=repo, text=True).strip(),
               "dirtyPaths": subprocess.check_output(["git", "status", "--short"], cwd=repo, text=True).splitlines(),
               "startedAt": datetime.datetime.now(datetime.timezone.utc).isoformat()}
    with tempfile.TemporaryDirectory(prefix="dfops-local-check-") as temporary:
        work = Path(temporary)
        data = work / "postgres"
        password = secrets.token_urlsafe(24)
        password_file = work / "password"
        password_file.write_text(password)
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        env.update(PGPASSWORD=password, TEST_DB_HOST="127.0.0.1", TEST_DB_PORT=str(port),
                   TEST_DB_NAME="dfops_local_check", TEST_DB_USER="postgres", TEST_DB_PASSWORD=password,
                   TEST_DATABASE_DSN=f"host=127.0.0.1 port={port} user=postgres password={password} dbname=dfops_local_check sslmode=disable",
                   DF_ENCRYPT_KEY=secrets.token_hex(32), DFOPS_REPORT_FONT=str(font),
                   DFOPS_MONITORING_WORKFLOW_ARTIFACTS=str(output / "workflow"),
                   DFOPS_CONFIGURED_REPORT_FIXTURE=str(output / "pdf"))
        started = False
        try:
            run("initdb", ["initdb", "-D", str(data), "-U", "postgres", "--auth-host=scram-sha-256",
                           "--auth-local=trust", "--pwfile=" + str(password_file)], check=True)
            cert, key = work / "server.crt", work / "server.key"
            run("certificate", ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
                                "-keyout", str(key), "-out", str(cert), "-days", "1", "-subj", "/CN=localhost"], check=True)
            key.chmod(0o600)
            with (data / "postgresql.conf").open("a") as config:
                config.write(f"\nlisten_addresses='127.0.0.1'\nport={port}\nssl=on\nssl_cert_file='{cert}'\nssl_key_file='{key}'\n")
            run("postgres-start", ["pg_ctl", "-D", str(data), "-l", str(output / "postgres.log"), "start"], check=True)
            started = True
            run("database", ["createdb", "-h", "127.0.0.1", "-p", str(port), "-U", "postgres", "dfops_local_check"], check=True)
            print("本地临时 PostgreSQL 已启动，开始前端测试、构建及后端完整测试。", flush=True)
            summary["frontendExit"] = run("frontend", ["npm", "--prefix", "frontend", "run", "test:workspace"])
            summary["buildExit"] = run("build", ["bash", "scripts/build_dfops.sh"])
            summary["backendExit"] = run("backend", ["go", "test", "-json", "-p", "1", "./...", "-count=1"], repo / "backend")
            summary["backend"] = backend_results(output / "backend.log")
            if summary["buildExit"] == 0:
                with (repo / "output/dfops").open("rb") as binary:
                    summary["binarySha256"] = hashlib.file_digest(binary, "sha256").hexdigest()
            summary["passed"] = all(summary[name] == 0 for name in ["frontendExit", "buildExit", "backendExit"])
        finally:
            if started:
                run("postgres-stop", ["pg_ctl", "-D", str(data), "-m", "fast", "stop"])
            summary["finishedAt"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
            (output / "summary.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"passed": summary["passed"], "backend": summary["backend"], "evidence": str(output)}, ensure_ascii=False, indent=2))
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
