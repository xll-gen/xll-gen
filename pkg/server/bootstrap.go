package server

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xll-gen/shm/go"
	"github.com/xll-gen/xll-gen/pkg/log"
)

// envPlaceholderRe matches ${VAR} placeholders in logging.dir, mirroring the
// C++ side's ExpandEnvVarsW so e.g. ${TEMP} resolves identically in both logs.
var envPlaceholderRe = regexp.MustCompile(`\$\{(\w+)\}`)

func InitLog(logDir string, level string, projectName string) (string, error) {
	exePath, _ := os.Executable()
	binDir := filepath.Dir(exePath)

	logDir = strings.ReplaceAll(logDir, "${XLL_DIR}", os.Getenv("XLL_DIR"))
	logDir = strings.ReplaceAll(logDir, "${BIN_DIR}", binDir)
	logDir = envPlaceholderRe.ReplaceAllStringFunc(logDir, func(m string) string {
		return os.Getenv(m[2 : len(m)-1])
	})

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

// ResolveSHMName returns the shared-memory name to connect to: projectName by
// default, overridden by a `-xll-shm=<name>` process argument (the form the C++
// launcher, regtest, and the regression harness all emit).
//
// It scans os.Args manually on purpose — NOT via the flag package. The
// generated Serve() runs inside the user's own main(), so a global flag.Parse()
// would choke on (and os.Exit(2) over) any flag the user's program defines that
// xll-gen does not know about. A manual prefix scan reads only the arg it owns
// and ignores everything else, so it composes with arbitrary user flags.
func ResolveSHMName(projectName string) string {
	name := projectName
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "-xll-shm=") {
			name = strings.TrimPrefix(arg, "-xll-shm=")
		}
	}
	return name
}

func ConnectSHM(projectName string) (*shm.Client, error) {
	name := ResolveSHMName(projectName)

	client, err := shm.Connect(shm.ClientConfig{ShmName: name})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to SHM: %w", err)
	}
	return client, nil
}
