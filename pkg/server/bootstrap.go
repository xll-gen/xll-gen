package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xll-gen/shm/go"
	"github.com/xll-gen/xll-gen/pkg/log"
)

func InitLog(logDir string, level string, projectName string) (string, error) {
	exePath, _ := os.Executable()
	binDir := filepath.Dir(exePath)

	logDir = strings.ReplaceAll(logDir, "${XLL_DIR}", os.Getenv("XLL_DIR"))
	logDir = strings.ReplaceAll(logDir, "${BIN_DIR}", binDir)

	if logDir == "" {
		logDir = "."
	}
	logPath := filepath.Join(logDir, projectName+"_go.log")

	if err := log.Init(logPath, level); err != nil {
		return "", fmt.Errorf("failed to initialize logger: %w", err)
	}
	shm.SetLogger(log.Default())
	return logPath, nil
}

func ConnectSHM(projectName string) (*shm.Client, error) {
	name := projectName
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "-xll-shm=") {
			name = strings.TrimPrefix(arg, "-xll-shm=")
		}
	}

	client, err := shm.Connect(shm.ClientConfig{ShmName: name})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to SHM: %w", err)
	}
	return client, nil
}
