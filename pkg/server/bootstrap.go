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

// StdoutLogEnvVar is the environment variable the C++ launcher sets on the
// server process it spawns (internal/assets/files/src/xll_launch.cpp:
// env[L"XLL_LOG_TO_STDOUT"] = L"1"). Named rather than inlined so the two sides
// of that contract are greppable as one symbol.
const StdoutLogEnvVar = "XLL_LOG_TO_STDOUT"

// InitServerLogging is the generated server's entire logger bootstrap. It used
// to be an if/else inline in internal/templates/server.go.tmpl; the logic
// carried no template variables, only the three xll.yaml values it now takes as
// arguments, so every generated project got a re-emitted copy that nothing but
// a golden-string grep covered.
//
// WHICH SINK, AND WHY IT IS NOT A CONFIG OPTION:
//
//   - StdoutLogEnvVar == "1" means we were launched BY THE XLL, and the
//     launcher already redirected this process's stdout/stderr into
//     <logDir>\<proj>_go.log (xll_launch.cpp, LaunchProcess). So we log to
//     STDOUT and must NOT open that file ourselves: two writers on one path
//     interleave partial lines, and the second handle would keep the file
//     locked independently of the inherited one — which is the orphaned-server
//     "log file cannot be deleted" symptom (S2, AGENTS.md §23.6) with an extra
//     handle bolted on.
//   - Anything else means no launcher (a dev `go run`, regtest, a user's own
//     main), so we resolve logging.dir ourselves and open the file. See InitLog
//     and AGENTS.md §18.12.
//
// A FAILED INIT IS REPORTED AND SURVIVED, never fatal. An add-in that works but
// cannot write a log is strictly better than one that refuses to start because
// its log directory is read-only. The report goes through fmt.Printf rather
// than log, because log is precisely what just failed to initialize.
func InitServerLogging(logDir string, level string, projectName string) {
	if os.Getenv(StdoutLogEnvVar) == "1" {
		if err := log.Init("", level); err != nil {
			fmt.Printf("Failed to initialize stdout logger: %v\n", err)
		}
		return
	}
	if _, err := InitLog(logDir, level, projectName); err != nil {
		fmt.Printf("Failed to initialize logger: %v\n", err)
	}
}

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
