package elektronLogging

import (
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

type ConsoleLogger struct {
	LoggerImpl
}

func NewConsoleLogger(logType int, prefix string) *ConsoleLogger {
	cLog := &ConsoleLogger{}
	cLog.Type = logType
	cLog.CreateLogFile(prefix)
	return cLog
}
func (cLog ConsoleLogger) Log(logType int, level log.Level, logData log.Fields, message string) {
	if config.ConsoleConfig.Enabled {
		if logType <= cLog.Type {

			logger.SetLevel(level)

			logger.SetOutput(os.Stdout)
			logger.WithFields(logData).Println(message)

			logger.SetOutput(cLog.LogFile)
			logger.WithFields(logData).Println(message)
		}
		if cLog.next != nil {
			cLog.next.Log(logType, level, logData, message)
		}
	}
}

func (cLog *ConsoleLogger) CreateLogFile(prefix string) {

	if config.ConsoleConfig.Enabled {
		filename := strings.Join([]string{prefix, config.ConsoleConfig.FilenameExtension}, "")
		dirName := logDir.getDirName()
		if dirName != "" {
			if logFile, err := os.Create(filepath.Join(dirName, filename)); err != nil {
				log.Fatal("Unable to create logFile: ", err)
			} else {
				cLog.LogFile = logFile
				cLog.AllowOnConsole = config.ConsoleConfig.AllowOnConsole
			}
		}
	}
}
