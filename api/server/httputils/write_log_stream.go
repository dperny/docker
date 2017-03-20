package httputils

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"golang.org/x/net/context"

	"github.com/docker/docker/api/types/backend"
	"github.com/docker/docker/pkg/ioutils"
	"github.com/docker/docker/pkg/jsonlog"
	"github.com/docker/docker/pkg/stdcopy"
)

// WriteLogStream writes an encoded byte stream of log messages from the
// messages channel, multiplexing them with a stdcopy.Writer if mux is true
func WriteLogStream(ctx context.Context, w io.Writer, config *backend.ContainerLogsConfig, mux bool) {
	wf := ioutils.NewWriteFlusher(w)
	defer wf.Close()

	wf.Flush()

	var outStream io.Writer
	outStream = wf
	errStream := outStream
	sysErrStream := errStream
	if mux {
		sysErrStream = stdcopy.NewStdWriter(outStream, stdcopy.Systemerr)
		errStream = stdcopy.NewStdWriter(outStream, stdcopy.Stderr)
		outStream = stdcopy.NewStdWriter(outStream, stdcopy.Stdout)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-config.Messages:
			if !ok {
				return
			}
			// check if the message contains an error. if so, write that error
			// and exit
			if msg.Err != nil {
				fmt.Fprintf(sysErrStream, "Error grabbing logs: %v\n", msg.Err)
				return
			}
			logLine := msg.Line
			if config.Details {
				logLine = append([]byte(stringAttrs(msg.Attrs)+" "), logLine...)
			}
			if config.Timestamps {
				// TODO(dperny) the format is defined in
				// daemon/logger/logger.go as logger.TimeFormat. importing
				// logger is verboten (not part of backend) so idk if just
				// importing the same thing from jsonlog is good enough
				logLine = append([]byte(msg.Timestamp.Format(jsonlog.RFC3339NanoFixed)+" "), logLine...)
			}
			if msg.Source == "stdout" && config.ShowStdout {
				outStream.Write(logLine)
			}
			if msg.Source == "stderr" && config.ShowStderr {
				errStream.Write(logLine)
			}
		}
	}
}

type byKey []string

func (s byKey) Len() int { return len(s) }
func (s byKey) Less(i, j int) bool {
	keyI := strings.Split(s[i], "=")
	keyJ := strings.Split(s[j], "=")
	return keyI[0] < keyJ[0]
}
func (s byKey) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func stringAttrs(a backend.LogAttributes) string {
	var ss byKey
	for k, v := range a {
		ss = append(ss, k+"="+v)
	}
	sort.Sort(ss)
	return strings.Join(ss, ",")
}
