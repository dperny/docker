package daemon

import (
	"errors"
	"strconv"
	"time"

	"golang.org/x/net/context"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/api/types/backend"
	containertypes "github.com/docker/docker/api/types/container"
	timetypes "github.com/docker/docker/api/types/time"
	"github.com/docker/docker/container"
	"github.com/docker/docker/daemon/logger"
)

// ContainerLogs copies the container's log channel to the channel provided in
// the config. If ContainerLogs returns an error, no messages have been copied.
// and the channel will be closed without data.
//
// if it returns nil, the config channel will be active and return log
// messages until it runs out or the context is canceled.
func (daemon *Daemon) ContainerLogs(ctx context.Context, containerName string, config *backend.ContainerLogsConfig) (rerr error) {

	// O K so every path out of this function is an error before we start the
	// streaming goroutine at the bottom, and nil after. if we return an error,
	// that goroutine doesn't start (and doesn't close this channel, which, as
	// the writer, it's our responsibility to close).
	defer func() {
		if rerr != nil {
			close(config.Messages)
		}
	}()

	// i know most of docker doesn't use logrus fields like this but swarmkit
	// does and i think it's better so i might as well be the change i want to
	// see in the world
	lg := logrus.WithFields(logrus.Fields{
		"module":    "daemon",
		"method":    "(*Daemon).ContainerLogs",
		"container": containerName,
	})

	// it's either error here or we will either block on trying to send, or
	// panic when we try to close. the former case is gonna leave someone
	// scratching their head for a few minutes before they realize their dumb
	// mistake
	if config.Messages == nil {
		// i'm REALLY tempted to do panic() here because i NEVER get to use panic()
		return errors.New("can't send messages on a nil channel, fix your code")
	}

	if !(config.ShowStdout || config.ShowStderr) {
		return errors.New("You must choose at least one stream")
	}
	container, err := daemon.GetContainer(containerName)
	if err != nil {
		return err
	}

	if container.RemovalInProgress || container.Dead {
		return errors.New("can not get logs from container which is dead or marked for removal")
	}

	if container.HostConfig.LogConfig.Type == "none" {
		return logger.ErrReadLogsNotSupported
	}

	cLog, err := daemon.getLogger(container)
	if err != nil {
		return err
	}

	logReader, ok := cLog.(logger.LogReader)
	if !ok {
		return logger.ErrReadLogsNotSupported
	}

	follow := config.Follow && container.IsRunning()
	tailLines, err := strconv.Atoi(config.Tail)
	if err != nil {
		tailLines = -1
	}

	var since time.Time
	if config.Since != "" {
		s, n, err := timetypes.ParseTimestamps(config.Since, 0)
		if err != nil {
			return err
		}
		since = time.Unix(s, n)
	}

	readConfig := logger.ReadConfig{
		Since:  since,
		Tail:   tailLines,
		Follow: follow,
	}

	logs := logReader.ReadLogs(readConfig)

	// past this point, we can't possibly return any errors, so we can just
	// start a goroutine and return to tell the caller not to expect errors
	// (if the caller wants to give up on logs, they have to cancel the context)
	// this goroutine functions as a shim between the logger and the caller.
	go func() {
		// set up some defers
		defer func() {
			// ok so this function, originally, was placed right after that
			// logger.ReadLogs call above. I THINK that means it sets off the
			// chain of events that results in the logger needing to be closed.
			// i do not know if an error in time parsing above causing an early
			// return will result in leaking the logger. if that is the case,
			// it would also have been a bug in the original code
			logs.Close()
			if cLog != container.LogDriver {
				// Since the logger isn't cached in the container, which
				// occurs if it is running, it must get explicitly closed
				// here to avoid leaking it and any file handles it has.
				if err := cLog.Close(); err != nil {
					logrus.Errorf("Error closing logger: %v", err)
				}
			}
		}()
		// close the messages channel. closing is the only way to signal above
		// that we're doing with logs (other than context cancel i guess).
		defer close(config.Messages)

		lg.Debug("begin logs")
		for {
			select {
			// i do not believe as the system is currently designed any error
			// is possible, but we should be prepared to handle it anyway. if
			// we do get an error, copy only the error field to a new object so
			// we don't end up with partial data in the other fields
			case err := <-logs.Err:
				lg.Errorf("Error streaming logs: %v", err)
				config.Messages <- &backend.LogMessage{Err: err}
				return
			case <-ctx.Done():
				lg.Debug("logs: end stream, ctx is done: %v", ctx.Err())
				return
			case msg, ok := <-logs.Msg:
				// there might be something weird with returning log messages
				// straight off that channel. there's some kind of ring buffer
				// log thing available to the logger so i don't actually know
				// how long the pointers i'm returning are valid probably not
				// an issue but check that if weird errors occur
				if !ok {
					lg.Debug("end logs")
					return
				}
				m := msg.AsLogMessage() // just a pointer conversion, does not copy data

				// there could be a case where the reader stops accepting
				// messages and the context is canceled. we need to check that
				// here, or otherwise we risk blocking forever on the message
				// send.
				select {
				case <-ctx.Done():
					return
				case config.Messages <- m:
				}
			}
		}
	}()
	return nil
}

func (daemon *Daemon) getLogger(container *container.Container) (logger.Logger, error) {
	if container.LogDriver != nil && container.IsRunning() {
		return container.LogDriver, nil
	}
	return container.StartLogger()
}

// mergeLogConfig merges the daemon log config to the container's log config if the container's log driver is not specified.
func (daemon *Daemon) mergeAndVerifyLogConfig(cfg *containertypes.LogConfig) error {
	if cfg.Type == "" {
		cfg.Type = daemon.defaultLogConfig.Type
	}

	if cfg.Config == nil {
		cfg.Config = make(map[string]string)
	}

	if cfg.Type == daemon.defaultLogConfig.Type {
		for k, v := range daemon.defaultLogConfig.Config {
			if _, ok := cfg.Config[k]; !ok {
				cfg.Config[k] = v
			}
		}
	}

	return logger.ValidateLogOpts(cfg.Type, cfg.Config)
}
