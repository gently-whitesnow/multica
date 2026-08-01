//go:build linux

package tasksandbox

import (
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const privilegedTestEnv = "MULTICA_TASK_SANDBOX_TEST_ROOT"

func TestTaskUserNameAndPathValidation(t *testing.T) {
	taskID := "11111111-2222-3333-4444-555555555555"
	name, err := taskUserName(taskID)
	if err != nil || name != "mct-111111112222" {
		t.Fatalf("taskUserName = %q, %v", name, err)
	}
	if err := validatePaths(taskID, "/srv/tasks/11111111", "/srv/tasks/11111111/workdir"); err != nil {
		t.Fatalf("valid paths: %v", err)
	}
	if err := validatePaths(taskID, "/srv/tasks/11111111", "/srv/tasks/22222222"); err == nil {
		t.Fatal("cross-task workdir must be rejected")
	}
	if _, err := taskUserName("../../root"); err == nil {
		t.Fatal("invalid task id must be rejected")
	}
}

func TestStripSandboxEnv(t *testing.T) {
	got := strings.Join(stripSandboxEnv([]string{"SAFE=1", EnvEnabled + "=1", EnvDaemonPID + "=42"}), ",")
	if got != "SAFE=1" {
		t.Fatalf("stripped env = %q", got)
	}
}

// TestLinuxTaskSandboxRuntime is an opt-in runtime proof with a real Linux
// user. It covers the positive path, daemon credential/config/log/profile and
// cross-task denies, background-process cleanup, and identity deletion.
func TestLinuxTaskSandboxRuntime(t *testing.T) {
	if os.Getenv("MULTICA_RUN_LINUX_TASK_SANDBOX_TESTS") != "1" {
		t.Skip("set MULTICA_RUN_LINUX_TASK_SANDBOX_TESTS=1 for privileged runtime test")
	}
	if os.Getenv(privilegedTestEnv) != "1" {
		cmd := exec.Command("sudo", "-n", "--preserve-env=MULTICA_RUN_LINUX_TASK_SANDBOX_TESTS", os.Args[0], "-test.run=^TestLinuxTaskSandboxRuntime$", "-test.v")
		cmd.Env = append(os.Environ(), privilegedTestEnv+"=1", "MULTICA_TASK_SANDBOX_ORIGINAL_UID="+strconv.Itoa(os.Getuid()), "MULTICA_TASK_SANDBOX_ORIGINAL_GID="+strconv.Itoa(os.Getgid()))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("privileged sandbox test: %v\n%s", err, out)
		}
		return
	}
	if os.Geteuid() != 0 {
		t.Fatal("privileged sandbox test did not run as root")
	}
	runPrivilegedRuntimeTest(t)
}

func runPrivilegedRuntimeTest(t *testing.T) {
	originalUID, _ := strconv.Atoi(os.Getenv("MULTICA_TASK_SANDBOX_ORIGINAL_UID"))
	originalGID, _ := strconv.Atoi(os.Getenv("MULTICA_TASK_SANDBOX_ORIGINAL_GID"))
	base, err := os.MkdirTemp("/tmp", "multica-task-sandbox-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	if err := os.Chmod(base, 0o711); err != nil {
		t.Fatal(err)
	}
	taskID := "a1111111-2222-3333-4444-555555555555"
	root := filepath.Join(base, "a1111111")
	work, home := filepath.Join(root, "workdir"), filepath.Join(root, "home")
	daemonState, otherWork := filepath.Join(base, "daemon"), filepath.Join(base, "other-task")
	for _, dir := range []string{work, daemonState, otherWork} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, managedRootMarker), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"credential", "config", "daemon.log", "audit_ssh_key", "profile"} {
		if err := os.WriteFile(filepath.Join(daemonState, name), []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(otherWork, "source"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(daemonState, originalUID, originalGID); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(otherWork, originalUID, originalGID); err != nil {
		t.Fatal(err)
	}
	if err := rootPrepare(taskID, root, work, home, originalUID, originalGID); err != nil {
		t.Fatal(err)
	}

	denies := []string{}
	for _, name := range []string{"credential", "config", "daemon.log", "audit_ssh_key", "profile"} {
		denies = append(denies, "test ! -r '"+filepath.Join(daemonState, name)+"'")
	}
	denies = append(denies, "test ! -r '"+filepath.Join(otherWork, "source")+"'")
	probe := "set -eu; test -w .; " + strings.Join(denies, "; ") + "; id -u > uid; sleep 300 </dev/null >/dev/null 2>&1 & echo $! > child_pid"
	if err := rootRun(taskID, root, work, os.Getpid(), originalUID, originalGID, "/bin/sh", []string{"-c", probe}, Stdio{}); err != nil {
		t.Fatal(err)
	}
	pidRaw, err := os.ReadFile(filepath.Join(work, "child_pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("background task process survived: %v", err)
	}
	name, _ := taskUserName(taskID)
	if _, err := user.Lookup(name); err != nil {
		t.Fatalf("identity disappeared before cleanup: %v", err)
	}
	if err := rootCleanup(taskID, root, originalUID, originalGID); err != nil {
		t.Fatal(err)
	}
	if _, err := user.Lookup(name); err == nil {
		t.Fatal("task identity survived cleanup")
	}

	t.Run("failure", func(t *testing.T) {
		id, caseRoot, caseWork := prepareRuntimeCase(t, base, "b1111111-2222-3333-4444-555555555555", originalUID, originalGID)
		if err := rootRun(id, caseRoot, caseWork, os.Getpid(), originalUID, originalGID, "/bin/sh", []string{"-c", "exit 7"}, Stdio{}); err == nil {
			t.Fatal("failed task returned success")
		}
		assertRuntimeCleanup(t, id, caseRoot, originalUID, originalGID)
	})

	t.Run("daemon-crash", func(t *testing.T) {
		id, caseRoot, caseWork := prepareRuntimeCase(t, base, "c1111111-2222-3333-4444-555555555555", originalUID, originalGID)
		if err := rootRun(id, caseRoot, caseWork, 2_000_000_000, originalUID, originalGID, "/bin/sh", []string{"-c", "echo $$ > pid; exec sleep 300"}, Stdio{}); err == nil || !strings.Contains(err.Error(), "daemon exited") {
			t.Fatalf("crash result = %v", err)
		}
		name, _ := taskUserName(id)
		if _, err := user.Lookup(name); err == nil {
			t.Fatal("task identity survived daemon crash")
		}
	})

	for i, terminal := range []string{"cancel", "timeout"} {
		t.Run(terminal, func(t *testing.T) {
			id := string(rune('d'+i)) + "1111111-2222-3333-4444-555555555555"
			id, caseRoot, caseWork := prepareRuntimeCase(t, base, id, originalUID, originalGID)
			done := make(chan error, 1)
			go func() {
				done <- rootRun(id, caseRoot, caseWork, os.Getpid(), originalUID, originalGID, "/bin/sh", []string{"-c", "echo $$ > pid; exec sleep 300"}, Stdio{})
			}()
			pidPath := filepath.Join(caseWork, "pid")
			deadline := time.Now().Add(2 * time.Second)
			for {
				if _, err := os.Stat(pidPath); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("task process did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			assertRuntimeCleanup(t, id, caseRoot, originalUID, originalGID)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("isolated runner did not stop")
			}
		})
	}
}

func prepareRuntimeCase(t *testing.T, base, taskID string, daemonUID, daemonGID int) (string, string, string) {
	t.Helper()
	root := filepath.Join(base, shortTaskID(taskID))
	work, home := filepath.Join(root, "workdir"), filepath.Join(root, "home")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, managedRootMarker), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rootPrepare(taskID, root, work, home, daemonUID, daemonGID); err != nil {
		t.Fatal(err)
	}
	return taskID, root, work
}

func assertRuntimeCleanup(t *testing.T, taskID, root string, daemonUID, daemonGID int) {
	t.Helper()
	if err := rootCleanup(taskID, root, daemonUID, daemonGID); err != nil {
		t.Fatal(err)
	}
	name, _ := taskUserName(taskID)
	if _, err := user.Lookup(name); err == nil {
		t.Fatalf("task identity %s survived cleanup", name)
	}
}
