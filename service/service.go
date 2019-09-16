package service

import (
	"fmt"
	"net/http"
	"time"

	"github.com/drakkan/sftpgo/api"
	"github.com/drakkan/sftpgo/config"
	"github.com/drakkan/sftpgo/dataprovider"
	"github.com/drakkan/sftpgo/logger"
	"github.com/drakkan/sftpgo/sftpd"
	"github.com/rs/zerolog"
)

const (
	logSender = "service"
)

// Service defines the SFTPGo service
type Service struct {
	ConfigDir     string
	ConfigFile    string
	LogFilePath   string
	LogMaxSize    int
	LogMaxBackups int
	LogMaxAge     int
	LogCompress   bool
	LogVerbose    bool
	Shutdown      chan bool
}

// Start initializes the service
func (s *Service) Start() error {
	logLevel := zerolog.DebugLevel
	if !s.LogVerbose {
		logLevel = zerolog.InfoLevel
	}
	logger.InitLogger(s.LogFilePath, s.LogMaxSize, s.LogMaxBackups, s.LogMaxAge, s.LogCompress, logLevel)
	logger.Info(logSender, "", "starting SFTPGo, config dir: %v, config file: %v, log max size: %v log max backups: %v "+
		"log max age: %v log verbose: %v, log compress: %v", s.ConfigDir, s.ConfigFile, s.LogMaxSize, s.LogMaxBackups, s.LogMaxAge,
		s.LogVerbose, s.LogCompress)
	config.LoadConfig(s.ConfigDir, s.ConfigFile)
	providerConf := config.GetProviderConf()

	err := dataprovider.Initialize(providerConf, s.ConfigDir)
	if err != nil {
		logger.Error(logSender, "", "error initializing data provider: %v", err)
		logger.ErrorToConsole("error initializing data provider: %v", err)
		return err
	}

	dataProvider := dataprovider.GetProvider()
	sftpdConf := config.GetSFTPDConfig()
	httpdConf := config.GetHTTPDConfig()

	sftpd.SetDataProvider(dataProvider)

	go func() {
		logger.Debug(logSender, "", "initializing SFTP server with config %+v", sftpdConf)
		if err := sftpdConf.Initialize(s.ConfigDir); err != nil {
			logger.Error(logSender, "", "could not start SFTP server: %v", err)
			logger.ErrorToConsole("could not start SFTP server: %v", err)
		}
		s.Shutdown <- true
	}()

	if httpdConf.BindPort > 0 {
		router := api.GetHTTPRouter()
		api.SetDataProvider(dataProvider)

		go func() {
			logger.Debug(logSender, "", "initializing HTTP server with config %+v", httpdConf)
			httpServer := &http.Server{
				Addr:           fmt.Sprintf("%s:%d", httpdConf.BindAddress, httpdConf.BindPort),
				Handler:        router,
				ReadTimeout:    300 * time.Second,
				WriteTimeout:   300 * time.Second,
				MaxHeaderBytes: 1 << 20, // 1MB
			}
			if err := httpServer.ListenAndServe(); err != nil {
				logger.Error(logSender, "", "could not start HTTP server: %v", err)
				logger.ErrorToConsole("could not start HTTP server: %v", err)
			}
			s.Shutdown <- true
		}()
	} else {
		logger.Debug(logSender, "", "HTTP server not started, disabled in config file")
		logger.DebugToConsole("HTTP server not started, disabled in config file")
	}
	return nil
}

// Wait blocks until the service exits
func (s *Service) Wait() {
	<-s.Shutdown
}

// Stop terminates the service and unblocks the Wait method
func (s *Service) Stop() {
	close(s.Shutdown)
	logger.Debug(logSender, "", "Service stopped")
}
