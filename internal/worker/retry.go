package worker

import (
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"strings"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/queue"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
)

func isTransient(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	s := telemetry.ExtractSQLState(err)
	return errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, driver.ErrBadConn) || strings.HasPrefix(s, "08") || s == "57P01" || s == "40001" || s == "40P01" || strings.Contains(err.Error(), "connect to database") || strings.Contains(err.Error(), "koneksi")
}

var errNextAttempt = errors.New("transient execution failure; retry with a new audit attempt")

// Deterministic across a crash between publishing the retry and acknowledging
// its predecessor. Duplicate retry messages therefore refer to the same attempt.
func nextAttempt(j queue.JobMessage) queue.JobMessage {
	j.ExecutionID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(j.ExecutionID+"/retry")).String()
	j.AttemptNumber++
	j.LockWaitStartedAt = nil
	return j
}
