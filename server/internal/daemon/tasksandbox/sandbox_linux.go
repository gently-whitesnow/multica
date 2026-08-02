//go:build linux

package tasksandbox

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const taskUserPrefix = "mct-"

const managedRootMarker = ".multica_sidecar_manifest.json"

type linuxSession struct {
	self, taskID, root, workDir, home string
	logger                            *slog.Logger
}

func prepare(ctx context.Context, self, taskID, root, workDir string, logger *slog.Logger) (Session, error) {
	if err := validatePaths(taskID, root, workDir); err != nil {
		return nil, err
	}
	if self == "" || !filepath.IsAbs(self) {
		return nil, errors.New("task sandbox: multica executable must be absolute")
	}
	home := filepath.Join(root, "home")
	args := []string{"-n", self, RootHelperArg, "prepare", taskID, root, workDir, home, strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())}
	cmd := exec.CommandContext(ctx, "sudo", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("task sandbox: prepare failed closed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return &linuxSession{self: self, taskID: taskID, root: root, workDir: workDir, home: home, logger: logger}, nil
}

func (s *linuxSession) Environment(realExecutable string) map[string]string {
	return map[string]string{
		EnvEnabled: "1", EnvExecutable: realExecutable, EnvTaskID: s.taskID,
		EnvRoot: s.root, EnvWorkDir: s.workDir, EnvSelf: s.self,
		EnvDaemonPID: strconv.Itoa(os.Getpid()),
		"HOME":       s.home, "XDG_CONFIG_HOME": filepath.Join(s.home, ".config"),
		"XDG_CACHE_HOME": filepath.Join(s.home, ".cache"),
		"XDG_DATA_HOME":  filepath.Join(s.home, ".local", "share"),
		"XDG_STATE_HOME": filepath.Join(s.home, ".local", "state"),
	}
}

func (s *linuxSession) Close(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "sudo", "-n", s.self, RootHelperArg, "cleanup", s.taskID, s.root, strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid()))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("task sandbox: cleanup failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runWrapper(ctx context.Context, args []string, stdio Stdio) error {
	self, executable, taskID := os.Getenv(EnvSelf), os.Getenv(EnvExecutable), os.Getenv(EnvTaskID)
	root, workDir, daemonPID := os.Getenv(EnvRoot), os.Getenv(EnvWorkDir), os.Getenv(EnvDaemonPID)
	if self == "" || executable == "" {
		return errors.New("task sandbox wrapper: incomplete managed environment")
	}
	if _, err := strconv.Atoi(daemonPID); err != nil {
		return errors.New("task sandbox wrapper: invalid daemon pid")
	}
	helperArgs := []string{"-n", "--preserve-env", self, RootHelperArg, "run", taskID, root, workDir, daemonPID, strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid()), executable, "--"}
	helperArgs = append(helperArgs, args...)
	cmd := exec.CommandContext(ctx, "sudo", helperArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdio.In, stdio.Out, stdio.Err
	cmd.Env = os.Environ()
	return cmd.Run()
}

func runRootHelper(args []string, stdio Stdio) error {
	if os.Geteuid() != 0 {
		return errors.New("must run as root through the restricted sudo rule")
	}
	if len(args) == 0 {
		return errors.New("missing action")
	}
	switch args[0] {
	case "prepare":
		if len(args) != 7 {
			return errors.New("invalid prepare arguments")
		}
		duid, err := strconv.Atoi(args[5])
		if err != nil || duid <= 0 {
			return errors.New("invalid daemon uid")
		}
		dgid, err := strconv.Atoi(args[6])
		if err != nil || dgid <= 0 {
			return errors.New("invalid daemon gid")
		}
		return rootPrepare(args[1], args[2], args[3], args[4], duid, dgid)
	case "run":
		if len(args) < 9 || args[8] != "--" {
			return errors.New("invalid run arguments")
		}
		pid, err := strconv.Atoi(args[4])
		if err != nil || pid <= 1 {
			return errors.New("invalid daemon pid")
		}
		duid, err := strconv.Atoi(args[5])
		if err != nil || duid <= 0 {
			return errors.New("invalid daemon uid")
		}
		dgid, err := strconv.Atoi(args[6])
		if err != nil || dgid <= 0 {
			return errors.New("invalid daemon gid")
		}
		return rootRun(args[1], args[2], args[3], pid, duid, dgid, args[7], args[9:], stdio)
	case "cleanup":
		if len(args) != 5 {
			return errors.New("invalid cleanup arguments")
		}
		duid, err := strconv.Atoi(args[3])
		if err != nil || duid <= 0 {
			return errors.New("invalid daemon uid")
		}
		dgid, err := strconv.Atoi(args[4])
		if err != nil || dgid <= 0 {
			return errors.New("invalid daemon gid")
		}
		return rootCleanup(args[1], args[2], duid, dgid)
	default:
		return errors.New("unknown action")
	}
}

func rootPrepare(taskID, root, workDir, home string, daemonUID, daemonGID int) error {
	if err := validatePaths(taskID, root, workDir); err != nil {
		return err
	}
	if err := validateManagedRoot(root); err != nil {
		return err
	}
	if home != filepath.Join(root, "home") {
		return errors.New("home escapes sandbox root")
	}
	name, err := taskUserName(taskID)
	if err != nil {
		return err
	}
	if _, err := user.Lookup(name); err == nil {
		if _, err := lookupTaskUser(name, taskID); err != nil {
			return err
		}
		if err := rootCleanup(taskID, root, daemonUID, daemonGID); err != nil {
			return fmt.Errorf("recover stale task identity: %w", err)
		}
	}
	useradd, err := exec.LookPath("useradd")
	if err != nil {
		return err
	}
	if out, err := exec.Command(useradd, "--system", "--no-create-home", "--home-dir", home, "--shell", "/usr/sbin/nologin", "--comment", "multica-task-"+taskID, name).CombinedOutput(); err != nil {
		return fmt.Errorf("create task identity: %w: %s", err, strings.TrimSpace(string(out)))
	}
	prepared := false
	defer func() {
		if !prepared {
			_ = deleteTaskUser(name)
		}
	}()
	u, err := lookupTaskUser(name, taskID)
	if err != nil {
		return err
	}
	uid, _ := strconv.Atoi(u.Uid)
	if err := os.MkdirAll(home, 0o770); err != nil {
		return err
	}
	if err := handoffTree(root, uid, daemonGID); err != nil {
		return err
	}
	prepared = true
	_ = daemonUID // recorded by ownership; retained for protocol symmetry/audit.
	return nil
}

func rootRun(taskID, root, workDir string, daemonPID, daemonUID, daemonGID int, executable string, args []string, stdio Stdio) error {
	if err := validatePaths(taskID, root, workDir); err != nil {
		return err
	}
	if err := validateManagedRoot(root); err != nil {
		return err
	}
	if !filepath.IsAbs(executable) {
		return errors.New("task executable must be absolute")
	}
	name, err := taskUserName(taskID)
	if err != nil {
		return err
	}
	u, err := lookupTaskUser(name, taskID)
	if err != nil {
		return err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("enable task process reaping: %w", err)
	}
	childCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(childCtx, executable, args...)
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = workDir, stdio.In, stdio.Out, stdio.Err
	cmd.Env = stripSandboxEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), NoSetGroups: true}, Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start isolated task: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sig)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			killUID(uid)
			return err
		case <-sig:
			killUID(uid)
		case <-tick.C:
			if err := syscall.Kill(daemonPID, 0); errors.Is(err, syscall.ESRCH) {
				killUID(uid)
				if err := rootCleanup(taskID, root, daemonUID, daemonGID); err != nil {
					return fmt.Errorf("daemon exited; isolated task cleanup failed: %w", err)
				}
				return errors.New("daemon exited; isolated task terminated and identity removed")
			}
		}
	}
}

func rootCleanup(taskID, root string, daemonUID, daemonGID int) error {
	name, err := taskUserName(taskID)
	if err != nil {
		return err
	}
	if filepath.Base(root) != shortTaskID(taskID) {
		return errors.New("sandbox root does not match task id")
	}
	if _, statErr := os.Lstat(root); statErr == nil {
		if err := validateManagedRoot(root); err != nil {
			return err
		}
	}
	u, err := lookupTaskUser(name, taskID)
	if err == nil {
		uid, _ := strconv.Atoi(u.Uid)
		killUID(uid)
	}
	if err := handoffTree(root, daemonUID, daemonGID); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err == nil {
		return deleteTaskUser(name)
	}
	return nil
}

func validatePaths(taskID, root, workDir string) error {
	if _, err := taskUserName(taskID); err != nil {
		return err
	}
	if !filepath.IsAbs(root) || !filepath.IsAbs(workDir) {
		return errors.New("sandbox paths must be absolute")
	}
	if filepath.Base(root) != shortTaskID(taskID) {
		return errors.New("sandbox root does not match task id")
	}
	rel, err := filepath.Rel(root, workDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return errors.New("workdir escapes sandbox root")
	}
	return nil
}

func shortTaskID(taskID string) string {
	compact := strings.ReplaceAll(taskID, "-", "")
	if len(compact) > 8 {
		return strings.ToLower(compact[:8])
	}
	return strings.ToLower(compact)
}

func validateManagedRoot(root string) error {
	clean := filepath.Clean(root)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return fmt.Errorf("resolve sandbox root: %w", err)
	}
	if resolved != clean {
		return errors.New("sandbox root must not traverse symlinks")
	}
	marker := filepath.Join(clean, managedRootMarker)
	info, err := os.Lstat(marker)
	if err != nil {
		return fmt.Errorf("sandbox root missing managed marker: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("sandbox root marker must be a regular file")
	}
	return nil
}

func taskUserName(taskID string) (string, error) {
	compact := strings.ReplaceAll(taskID, "-", "")
	if len(compact) < 12 {
		return "", errors.New("invalid task id")
	}
	for _, r := range compact {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return "", errors.New("invalid task id")
		}
	}
	return taskUserPrefix + strings.ToLower(compact[:12]), nil
}

func lookupTaskUser(name, taskID string) (*user.User, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return nil, fmt.Errorf("lookup task identity: %w", err)
	}
	if u.Username != name || u.Name != "multica-task-"+taskID {
		return nil, errors.New("task identity mismatch")
	}
	return u, nil
}

func handoffTree(root string, uid, gid int) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := os.Lchown(path, uid, gid); err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm() & 0o111
		if d.IsDir() {
			mode |= 0o770
		} else {
			mode |= 0o660
		}
		return os.Chmod(path, mode)
	})
}

func deleteTaskUser(name string) error {
	userdel, err := exec.LookPath("userdel")
	if err != nil {
		return err
	}
	if out, err := exec.Command(userdel, name).CombinedOutput(); err != nil {
		return fmt.Errorf("delete task identity: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func killUID(uid int) {
	if uid <= 0 {
		return
	}
	if pkill, err := exec.LookPath("pkill"); err == nil {
		_ = exec.Command(pkill, "-KILL", "-U", strconv.Itoa(uid)).Run()
	}
	if pgrep, err := exec.LookPath("pgrep"); err == nil {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			for {
				var status syscall.WaitStatus
				if pid, _ := syscall.Wait4(-1, &status, syscall.WNOHANG, nil); pid <= 0 {
					break
				}
			}
			if err := exec.Command(pgrep, "-U", strconv.Itoa(uid)).Run(); err != nil {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
}

func stripSandboxEnv(in []string) []string {
	prefixes := []string{EnvEnabled + "=", EnvExecutable + "=", EnvTaskID + "=", EnvRoot + "=", EnvWorkDir + "=", EnvSelf + "=", EnvDaemonPID + "="}
	out := in[:0]
	for _, entry := range in {
		keep := true
		for _, p := range prefixes {
			if strings.HasPrefix(entry, p) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, entry)
		}
	}
	return out
}
