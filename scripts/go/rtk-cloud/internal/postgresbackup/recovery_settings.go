package postgresbackup

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// NativeRecoverySettings carries only fixed numeric recovery prerequisites from
// authenticated control data. Source runtime config and shell hooks are excluded.
func NativeRecoverySettings(control string) (string, error) {
	if strings.Contains(control, "WARNING:") {
		return "", errors.New("PostgreSQL control data reported a warning")
	}
	keys := []struct{ field, parameter string }{{"max_connections", "max_connections"}, {"max_worker_processes", "max_worker_processes"}, {"max_wal_senders", "max_wal_senders"}, {"max_prepared_xacts", "max_prepared_transactions"}, {"max_locks_per_xact", "max_locks_per_transaction"}}
	values := map[string]string{}
	for _, line := range strings.Split(control, "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	var settings strings.Builder
	for _, key := range keys {
		value, err := strconv.Atoi(values[key.field+" setting"])
		if err != nil || value < 0 || value > 262143 {
			return "", errors.New("invalid or missing PostgreSQL recovery parameter")
		}
		fmt.Fprintf(&settings, "%s = %d\n", key.parameter, value)
	}
	return settings.String(), nil
}
