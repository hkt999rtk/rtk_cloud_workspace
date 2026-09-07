package recovery

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// RenderWALArchiveConfig returns a reviewable PostgreSQL fragment. It does not
// alter a running database, install a schedule or demonstrate an RPO.
func RenderWALArchiveConfig(c WALConfig, configFile, executable string, timeoutSeconds int) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	for _, path := range []string{configFile, executable} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
			return "", errors.New("absolute clean WAL config/tool paths required")
		}
	}
	if timeoutSeconds < 30 || timeoutSeconds > 300 {
		return "", errors.New("archive timeout must be between 30 and 300 seconds")
	}
	command := walArchiveCommand(c, configFile, executable)
	return fmt.Sprintf("# Reviewed online WAL archiving; source wal_level must be replica or logical.\narchive_mode = on\narchive_library = ''\narchive_command = %s\narchive_timeout = '%ds'\n", pgConfigString(command), timeoutSeconds), nil
}
func walArchiveCommand(c WALConfig, configFile, executable string) string {
	argv := []string{executable, "wal-archive", "--config", configFile, "--confirm-environment", c.Environment, "--confirm-stack", c.Stack}
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = restoreShellArgument(arg)
	}
	return "exec " + strings.Join(quoted, " ") + " --name '%f' --source '%p'"
}
