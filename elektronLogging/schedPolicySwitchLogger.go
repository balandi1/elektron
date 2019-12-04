package elektronLogging

import (
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

type SchedPolicySwitchLogger struct {
	LoggerImpl
}

func NewSchedPolicySwitchLogger(b *baseLogData, logType int, prefix string) *SchedPolicySwitchLogger {
	sLog := &SchedPolicySwitchLogger{}
	sLog.Type = logType
	sLog.CreateLogFile(prefix)
	sLog.next = nil
	sLog.baseLogData = b
	return sLog
}

func (sLog SchedPolicySwitchLogger) Log(logType int, level log.Level, message string) {
	if sLog.Type == logType {
		if config.SPSConfig.Enabled {
			if sLog.AllowOnConsole {
				logger.SetOutput(os.Stdout)
				logger.WithFields(sLog.data).Log(level, message)
			}

			logger.SetOutput(sLog.LogFile)
			logger.WithFields(sLog.data).Log(level, message)
		}
	}
	if sLog.next != nil {
		sLog.next.Log(logType, level, message)
	} else {
		// Clearing the fields.
		sLog.resetFields()
	}
}

func (sLog SchedPolicySwitchLogger) Logf(logType int, level log.Level, msgFmtString string, args ...interface{}) {
	if sLog.Type == logType {
		if config.SPSConfig.Enabled {
			if sLog.AllowOnConsole {
				logger.SetOutput(os.Stdout)
				logger.WithFields(sLog.data).Logf(level, msgFmtString, args...)
			}

			logger.SetOutput(sLog.LogFile)
			logger.WithFields(sLog.data).Logf(level, msgFmtString, args...)
		}
	}
	// Forwarding to next logger
	if sLog.next != nil {
		sLog.next.Logf(logType, level, msgFmtString, args...)
	} else {
		// Clearing the fields.
		sLog.resetFields()
	}
}

func (sLog *SchedPolicySwitchLogger) CreateLogFile(prefix string) {
	if config.SPSConfig.Enabled {
		filename := strings.Join([]string{prefix, config.SPSConfig.FilenameExtension}, "")
		dirName := logDir.getDirName()
		if dirName != "" {
			if logFile, err := os.Create(filepath.Join(dirName, filename)); err != nil {
				log.Fatal("Unable to create logFile: ", err)
			} else {
				sLog.LogFile = logFile
				sLog.AllowOnConsole = config.SPSConfig.AllowOnConsole
			}
		}
	}
}
