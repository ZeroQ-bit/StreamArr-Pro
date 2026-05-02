package services

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Zerr0-C00L/StreamArr/internal/settings"
)

const (
	rdMountRoot       = "/mount/rd"
	rdMountStateDir   = "/app/rd-mount"
	rdMountConfigPath = rdMountStateDir + "/zurg.yml"
	rdMountRclonePath = rdMountStateDir + "/rclone.conf"
	rdMountCacheDir   = "/app/cache/rclone"
	rdMountListenURL  = "http://127.0.0.1:9999/dav/"
)

type RDMountManager struct {
	settingsManager *settings.Manager

	mu           sync.Mutex
	currentToken string
	zurgCmd      *exec.Cmd
	rcloneCmd    *exec.Cmd
	stopChan     chan struct{}
}

func NewRDMountManager(settingsManager *settings.Manager) *RDMountManager {
	return &RDMountManager{
		settingsManager: settingsManager,
		stopChan:        make(chan struct{}),
	}
}

func (m *RDMountManager) Start() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		m.reconcile()

		for {
			select {
			case <-m.stopChan:
				m.stopStack("shutdown")
				return
			case <-ticker.C:
				m.reconcile()
			}
		}
	}()
}

func (m *RDMountManager) Stop() {
	select {
	case <-m.stopChan:
	default:
		close(m.stopChan)
	}
}

func (m *RDMountManager) reconcile() {
	if m.settingsManager == nil {
		return
	}

	cfg := m.settingsManager.Get()
	token := strings.TrimSpace(cfg.RealDebridAPIKey)
	if !cfg.PlexExportEnabled || token == "" {
		m.stopStack("disabled or missing Real-Debrid API key")
		return
	}

	m.mu.Lock()
	tokenChanged := token != m.currentToken
	running := m.stackRunningLocked()
	m.mu.Unlock()

	if !running || tokenChanged {
		if err := m.startStack(token); err != nil {
			log.Printf("[RD-MOUNT] Mount stack unavailable: %v", err)
		}
	}
}

func (m *RDMountManager) stackRunningLocked() bool {
	return processAlive(m.zurgCmd) && processAlive(m.rcloneCmd)
}

func (m *RDMountManager) startStack(token string) error {
	m.stopStack("reconfigure")

	if _, err := os.Stat("/dev/fuse"); err != nil {
		return fmt.Errorf("/dev/fuse unavailable inside container: %w", err)
	}
	if _, err := exec.LookPath("zurg"); err != nil {
		return fmt.Errorf("zurg binary not found: %w", err)
	}
	if _, err := exec.LookPath("rclone"); err != nil {
		return fmt.Errorf("rclone binary not found: %w", err)
	}

	// Clear any stale mount endpoint before we touch the shared mount root.
	m.unmountIfNeeded()

	if err := os.MkdirAll(rdMountStateDir, 0o755); err != nil {
		return fmt.Errorf("create rd mount state dir: %w", err)
	}
	if err := os.MkdirAll(rdMountCacheDir, 0o755); err != nil {
		return fmt.Errorf("create rd mount cache dir: %w", err)
	}
	if err := ensureDirectory(rdMountRoot, 0o755); err != nil {
		return fmt.Errorf("create rd mount root: %w", err)
	}

	if err := os.WriteFile(rdMountConfigPath, []byte(renderZurgConfig(token)), 0o600); err != nil {
		return fmt.Errorf("write zurg config: %w", err)
	}
	if err := os.WriteFile(rdMountRclonePath, []byte(renderRcloneConfig()), 0o600); err != nil {
		return fmt.Errorf("write rclone config: %w", err)
	}

	zurgCmd := exec.Command("zurg", "-c", rdMountConfigPath)
	zurgCmd.Stdout = prefixWriter("[RD-MOUNT:zurg] ")
	zurgCmd.Stderr = prefixWriter("[RD-MOUNT:zurg] ")
	if err := zurgCmd.Start(); err != nil {
		return fmt.Errorf("start zurg: %w", err)
	}

	if err := waitForHTTP(rdMountListenURL, 20*time.Second); err != nil {
		_ = killCommand(zurgCmd)
		return fmt.Errorf("wait for zurg: %w", err)
	}

	rcloneCmd := exec.Command(
		"rclone",
		"mount",
		"zurg:",
		rdMountRoot,
		"--config", rdMountRclonePath,
		"--allow-other",
		"--allow-non-empty",
		"--cache-dir", rdMountCacheDir,
		"--buffer-size", "32M",
		"--dir-cache-time", "10s",
		"--vfs-cache-mode", "off",
	)
	rcloneCmd.Stdout = prefixWriter("[RD-MOUNT:rclone] ")
	rcloneCmd.Stderr = prefixWriter("[RD-MOUNT:rclone] ")
	if err := rcloneCmd.Start(); err != nil {
		_ = killCommand(zurgCmd)
		return fmt.Errorf("start rclone mount: %w", err)
	}

	if err := waitForMountDirectories(rdMountRoot, 20*time.Second); err != nil {
		_ = killCommand(rcloneCmd)
		_ = killCommand(zurgCmd)
		m.unmountIfNeeded()
		return fmt.Errorf("wait for mounted library: %w", err)
	}

	m.mu.Lock()
	m.currentToken = token
	m.zurgCmd = zurgCmd
	m.rcloneCmd = rcloneCmd
	m.mu.Unlock()

	log.Printf("[RD-MOUNT] Real-Debrid library mounted at %s", rdMountRoot)
	return nil
}

func (m *RDMountManager) stopStack(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.zurgCmd == nil && m.rcloneCmd == nil && m.currentToken == "" {
		return
	}

	log.Printf("[RD-MOUNT] Stopping mount stack: %s", reason)
	_ = killCommand(m.rcloneCmd)
	_ = killCommand(m.zurgCmd)
	m.unmountIfNeeded()

	m.currentToken = ""
	m.zurgCmd = nil
	m.rcloneCmd = nil
}

func (m *RDMountManager) unmountIfNeeded() {
	for _, name := range []string{"fusermount3", "fusermount"} {
		if _, err := exec.LookPath(name); err == nil {
			_ = exec.Command(name, "-u", "-z", rdMountRoot).Run()
			break
		}
	}
	_ = exec.Command("umount", rdMountRoot).Run()
	_ = exec.Command("umount", "-l", rdMountRoot).Run()
}

func ensureDirectory(path string, mode os.FileMode) error {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s exists but is not a directory", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(path, mode)
}

func processAlive(cmd *exec.Cmd) bool {
	return cmd != nil && cmd.Process != nil && cmd.ProcessState == nil
}

func killCommand(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	return nil
}

func waitForHTTP(target string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}

	for time.Now().Before(deadline) {
		resp, err := client.Get(target)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("timed out waiting for %s", target)
}

func waitForMountDirectories(root string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(root, "movies")); err == nil {
			return nil
		}
		if _, err := os.Stat(filepath.Join(root, "shows")); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s/{movies,shows}", root)
}

func renderZurgConfig(token string) string {
	return fmt.Sprintf(`zurg: v1
token: %s
check_for_changes_every_secs: 10
enable_repair: true
auto_delete_rar_torrents: true
directories:
  shows:
    group_order: 20
    group: media
    filters:
      - has_episodes: true
  movies:
    group_order: 30
    group: media
    only_show_the_biggest_file: true
    filters:
      - regex: /.*/
`, token)
}

func renderRcloneConfig() string {
	return `[zurg]
type = webdav
url = http://127.0.0.1:9999/dav
vendor = other
pacer_min_sleep = 0
`
}

type logPrefixWriter struct {
	prefix string
}

func prefixWriter(prefix string) io.Writer {
	return &logPrefixWriter{prefix: prefix}
}

func (w *logPrefixWriter) Write(p []byte) (int, error) {
	trimmed := strings.TrimSpace(string(p))
	if trimmed == "" {
		return len(p), nil
	}
	for _, line := range strings.Split(trimmed, "\n") {
		log.Printf("%s%s", w.prefix, strings.TrimSpace(line))
	}
	return len(p), nil
}
