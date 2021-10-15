package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/c13n-io/c13n-backend/app"
	"github.com/c13n-io/c13n-backend/lnchat"
	"github.com/c13n-io/c13n-backend/rpc"
	"github.com/c13n-io/c13n-backend/slog"
	"github.com/c13n-io/c13n-backend/store"
)

var (
	logger *slog.Logger
	server *rpc.Server
)

func initLogger() {
	// Create temporary cmd logger
	logger = slog.NewLogger("cmd")
}

// Run initializes the configuration and starts the application.
func Run(_ *cobra.Command, _ []string) error {
	// Set the default log level
	logLevel := viper.GetString("log_level")
	if err := slog.SetLogLevel(logLevel); err != nil {
		logger.WithError(err).
			Errorf("Could not set log level to %q", logLevel)
		return err
	}
	// Recreate cmd logger after the log level initialization
	logger = slog.NewLogger("cmd")

	// Initialize database
	db, err := store.New(viper.GetString("database.db_path"))
	if err != nil {
		logger.WithError(err).Error("Could not create database")
		return err
	}

	// Initialize chat service
	var lnchatMgr lnchat.LightManager
	if viper.GetString("lndconnect") != "" {
		lnchatMgr, err = lnchat.NewFromURL(viper.GetString("lndconnect"))
	} else {
		lnchatMgr, err = lnchat.New(viper.GetString("lnd.address"),
			lnchat.WithTLSPath(viper.GetString("lnd.tls_path")),
			lnchat.WithMacaroonPath(viper.GetString("lnd.macaroon_path")))
	}
	if err != nil {
		logger.WithError(err).Error("Could not initialize lnchat service")
		return err
	}

	ctxb := context.Background()
	globalCtx, globalCancel := context.WithCancel(ctxb)
	defer globalCancel()

	// Initialize application
	var appOpts []func(*app.App) error

	defaultFeeLimitMsat := viper.GetInt64("app.default_fee_limit_msat")
	if defaultFeeLimitMsat != 0 {
		appOpts = append(appOpts, app.WithDefaultFeeLimitMsat(defaultFeeLimitMsat))
	}
	application, err := app.New(lnchatMgr, db, appOpts...)
	if err != nil {
		logger.WithError(err).Error("Could not create application")
		return err
	}

	if err := application.Init(globalCtx, 15); err != nil {
		logger.WithError(err).Error("Could not initialize application")
		return err
	}

	// Initialize server
	var srvOpts []func(*rpc.Server) error
	if viper.IsSet("server.tls.cert_path") && viper.IsSet("server.tls.key_path") {
		srvOpts = append(srvOpts, rpc.WithTLS(
			viper.GetString("server.tls.cert_path"),
			viper.GetString("server.tls.key_path"),
		))
	}
	if viper.IsSet("server.user") && viper.IsSet("server.pass") {
		srvOpts = append(srvOpts, rpc.WithBasicAuth(
			viper.GetString("server.user"),
			viper.GetString("server.pass"),
		))
	}
	srvAddress := viper.GetString("server.address")
	server, err = rpc.New(srvAddress, application, srvOpts...)
	if err != nil {
		logger.WithError(err).Error("Could not initialize server")
		return err
	}

	// Shutdown on interrupt
	terminationCh := make(chan interface{})
	go waitForTermination(terminationCh,
		time.Duration(viper.GetInt("server.graceful_shutdown_timeout"))*time.Second)

	logger.Infof("Starting server on %s", srvAddress)

	// Run server
	if err := server.Serve(server.Listener); err != nil {
		logger.WithError(err).Error("Fatal server error during Serve")
		return err
	}

	<-terminationCh

	logger.Info("THE END")
	return nil
}

func waitForTermination(terminationCh chan<- interface{}, gracePeriodTimeout time.Duration) {
	interruptCh := make(chan os.Signal, 1)
	signal.Notify(interruptCh, os.Interrupt, syscall.SIGTERM)

	<-interruptCh
	logger.Info("Received shutdown signal.")

	// Try to terminate gracefully
	graceWaitCh := make(chan struct{})
	go func() {
		//graceWaitCh <- true
		server.GracefulStop()
		close(graceWaitCh)
	}()

	// Stop the server
	logger.Infof("Waiting %v for graceful termination.", gracePeriodTimeout)
	select {
	case <-graceWaitCh:
	case <-time.After(gracePeriodTimeout):
		server.Stop()
	}

	if err := server.Cleanup(); err != nil {
		logger.WithError(err).Error("Error generated during cleanup")
	}

	close(terminationCh)
}