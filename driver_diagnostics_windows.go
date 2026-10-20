//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type DriverDiagnostic struct {
	Service        string `json:"service"`
	RegisteredPath string `json:"registeredPath"`
	ExpectedPath   string `json:"expectedPath"`
	Running        bool   `json:"running"`
	PathMismatch   bool   `json:"pathMismatch"`
	Missing        bool   `json:"missing"`
	Message        string `json:"message"`
}

// Read-only inspection, including kernel drivers. Another path may belong to
// another application; never stop or delete shared driver services here.
func diagnoseDriverServices(exeDir string) ([]DriverDiagnostic, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services`, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil, err
	}
	defer key.Close()
	names, err := key.ReadSubKeyNames(-1)
	if err != nil {
		return nil, err
	}
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, err
	}
	defer windows.CloseServiceHandle(scm)
	result := make([]DriverDiagnostic, 0)
	var errs []error
	for _, name := range names {
		if !strings.HasPrefix(strings.ToLower(name), "windivert") {
			continue
		}
		serviceName, err := windows.UTF16PtrFromString(name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		handle, err := windows.OpenService(scm, serviceName, windows.SERVICE_QUERY_CONFIG|windows.SERVICE_QUERY_STATUS)
		if err != nil {
			errs = append(errs, fmt.Errorf("inspect %s: %w", name, err))
			continue
		}
		s := &mgr.Service{Name: name, Handle: handle}
		cfg, configErr := s.Config()
		status, statusErr := s.Query()
		s.Close()
		if err := errors.Join(configErr, statusErr); err != nil {
			errs = append(errs, fmt.Errorf("inspect %s: %w", name, err))
			continue
		}
		result = append(result, assessDriverService(name, cfg.BinaryPathName, exeDir, status.State == svc.Running))
	}
	return result, errors.Join(errs...)
}
func normalizeDriverPath(path string) string {
	path = strings.Trim(strings.TrimSpace(path), `"`)
	// Windows environment variables use %NAME%, not Go's $NAME syntax.
	for _, name := range []string{"SystemRoot", "windir"} {
		for {
			start := strings.Index(strings.ToLower(path), "%"+strings.ToLower(name)+"%")
			if start < 0 {
				break
			}
			path = path[:start] + os.Getenv(name) + path[start+len(name)+2:]
		}
	}
	path = strings.TrimPrefix(path, `\??\`)
	path = strings.TrimPrefix(path, `\\?\`)
	if strings.HasPrefix(strings.ToLower(path), `\systemroot\`) {
		path = filepath.Join(os.Getenv("SystemRoot"), path[len(`\SystemRoot\`):])
	}
	return filepath.Clean(path)
}
func assessDriverService(name, path, exeDir string, running bool) DriverDiagnostic {
	path = normalizeDriverPath(path)
	expected := filepath.Join(exeDir, "WinDivert64.sys")
	_, err := os.Stat(path)
	d := DriverDiagnostic{Service: name, RegisteredPath: path, ExpectedPath: expected, Running: running,
		PathMismatch: !strings.EqualFold(path, expected), Missing: errors.Is(err, os.ErrNotExist)}
	switch {
	case d.Missing:
		d.Message = "注册的驱动文件不存在，可能已移动程序目录。确认其他 WinDivert 程序已退出后，按 README 的人工修复步骤处理。"
	case d.PathMismatch:
		d.Message = "服务路径与当前目录不同，也可能由其他程序注册；不会自动停止或删除共享驱动。"
	default:
		d.Message = "服务路径与当前目录一致。"
	}
	if err != nil && !d.Missing {
		d.Message += " 文件检查失败: " + err.Error()
	}
	return d
}
