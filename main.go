package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"amp-sentinel/amp"
	"amp-sentinel/api"
	"amp-sentinel/diagnosis"
	"amp-sentinel/intake"
	"amp-sentinel/logger"
	"amp-sentinel/notify"
	"amp-sentinel/project"
	"amp-sentinel/scheduler"
	"amp-sentinel/skill"
	"amp-sentinel/store"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	// Load config
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Initialize logger
	level := logger.ParseLevel(cfg.Logger.Level)
	var loggers []logger.Logger
	loggers = append(loggers, logger.NewConsole(level, cfg.Logger.Console.Color))

	if cfg.Logger.File.Enabled {
		fileLog, err := logger.NewFile(logger.FileConfig{
			Dir:        cfg.Logger.File.Dir,
			Level:      level,
			MaxSizeMB:  cfg.Logger.File.MaxSizeMB,
			MaxAgeDays: cfg.Logger.File.MaxAgeDays,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to init file logger: %v\n", err)
			os.Exit(1)
		}
		loggers = append(loggers, fileLog)
	}

	if cfg.Logger.Structured.Enabled {
		structLog, err := logger.NewStructured(cfg.Logger.Structured.Path, level)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to init structured logger: %v\n", err)
			os.Exit(1)
		}
		loggers = append(loggers, structLog)
	}

	var log logger.Logger
	if len(loggers) == 1 {
		log = loggers[0]
	} else {
		log = logger.Multi(loggers...)
	}
	defer log.Close()

	log.Info("sentinel.starting", logger.String("config", *configPath))

	// Resolve API key
	apiKey := cfg.Amp.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("AMP_API_KEY")
	}
	if apiKey == "" {
		log.Error("AMP_API_KEY is required (set in config or environment)")
		os.Exit(1)
	}

	// Initialize store
	var dataStore store.Store
	switch cfg.Store.Type {
	case "mysql":
		dataStore, err = store.NewMySQLStore(store.MySQLConfig{
			DSN:             cfg.Store.MySQL.DSN,
			MaxOpenConns:    cfg.Store.MySQL.MaxOpenConns,
			MaxIdleConns:    cfg.Store.MySQL.MaxIdleConns,
			ConnMaxLifetime: ParseDuration(cfg.Store.MySQL.ConnMaxLifetime, 5*time.Minute),
		}, log)
	case "json":
		storePath := cfg.Store.JSON.Path
		if storePath == "" {
			storePath = "./data/sentinel.json"
		}
		if dir := filepath.Dir(storePath); dir != "." {
			os.MkdirAll(dir, 0755)
		}
		dataStore, err = store.NewJSONStore(storePath, ParseDuration(cfg.Store.JSON.FlushInterval, 30*time.Second), log)
	default:
		dbPath := cfg.Store.SQLite.Path
		if dir := filepath.Dir(dbPath); dir != "." {
			os.MkdirAll(dir, 0755)
		}
		dataStore, err = store.NewSQLiteStore(dbPath, log)
	}
	if err != nil {
		log.Error("store.init_failed", logger.Err(err))
		os.Exit(1)
	}
	defer dataStore.Close()

	// Initialize components
	ampClient := amp.NewClient(cfg.Amp.Binary, apiKey, log)
	registry := project.NewRegistry(cfg.Projects)
	sources := project.NewSourceManager(cfg.Source.BaseDir, cfg.Source.GitSSHKey, log)

	feishuNotifier := notify.NewFeishuNotifier(notify.FeishuConfig{
		DefaultWebhook: cfg.Feishu.DefaultWebhook,
		SignKey:        cfg.Feishu.SignKey,
		DashboardURL:   cfg.Feishu.DashboardURL,
		Timeout:        ParseDuration(cfg.Feishu.Timeout, 10*time.Second),
		RetryCount:     cfg.Feishu.RetryCount,
	}, log)

	// Initialize skill manager
	skillMgr := skill.NewManager(cfg.Skill.Dir, cfg.Skill.Env, log)
	if cfg.Skill.Dir != "" {
		if err := os.MkdirAll(cfg.Skill.Dir, 0o755); err != nil {
			log.Warn("skill.mkdir_failed", logger.Err(err))
		} else if err := skillMgr.LoadAll(); err != nil {
			log.Warn("skill.load_failed", logger.Err(err))
		} else {
			log.Info("skill.ready", logger.Int("count", skillMgr.Len()))
		}
	}

	engine := diagnosis.NewEngine(ampClient, sources, registry, skillMgr, log, diagnosis.EngineConfig{
		Mode:       cfg.Amp.DefaultMode,
		SkillDir:   cfg.Skill.Dir,
		SessionDir: cfg.Logger.Session.Dir,
	})

	// storeCtx creates an independent context for store writes that must
	// succeed even after the diagnosis context is cancelled (timeout/shutdown).
	storeCtx := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 10*time.Second)
	}

	// Define the diagnosis function used by the scheduler
	diagnoseFn := func(ctx context.Context, taskID string, inc *intake.Incident) error {
		// Persist task record (use independent context in case of retry —
		// CreateTask may fail with duplicate key, fall back to UpdateTask)
		now := time.Now()
		storeTask := &store.DiagnosisTask{
			ID:         taskID,
			IncidentID: inc.ID,
			ProjectKey: inc.ProjectKey,
			Status:     store.StatusRunning,
			Priority:   intake.SeverityPriority(inc.Severity),
			CreatedAt:  now,
			StartedAt:  &now,
		}
		sCtx, sCancel := storeCtx()
		if createErr := dataStore.CreateTask(sCtx, storeTask); createErr != nil {
			// Retry: task already exists from a previous attempt, update instead
			if updateErr := dataStore.UpdateTask(sCtx, storeTask); updateErr != nil {
				log.Error("store.create_task_failed", logger.Err(updateErr))
			}
		}
		sCancel()

		report, err := engine.Diagnose(ctx, inc)
		if err != nil {
			// Update task as failed — use independent context because
			// the diagnosis context may be cancelled (timeout).
			finishedAt := time.Now()
			storeTask.Status = store.StatusFailed
			storeTask.Error = err.Error()
			storeTask.FinishedAt = &finishedAt
			sCtx, sCancel := storeCtx()
			if updateErr := dataStore.UpdateTask(sCtx, storeTask); updateErr != nil {
				log.Error("store.update_task_failed", logger.Err(updateErr))
			}
			sCancel()
			return err
		}

		// Persist report
		storeReport := &store.DiagnosisReport{
			ID:          "rpt-" + taskID,
			TaskID:      taskID,
			IncidentID:  report.IncidentID,
			ProjectKey:  report.ProjectKey,
			ProjectName: report.ProjectName,
			Summary:     report.Summary,
			RawResult:   report.RawResult,
			Confidence:  report.Confidence,
			HasIssue:    report.HasIssue,
			Tainted:     report.Tainted,
			ToolsUsed:   report.ToolsUsed,
			SkillsUsed:  report.SkillsUsed,
			DiagnosedAt: report.DiagnosedAt,
		}

		// Send Feishu notification with a separate context so it
		// isn't cancelled by scheduler shutdown after diagnosis completes.
		proj, _ := registry.Lookup(inc.ProjectKey)
		if proj != nil {
			notifyCtx, notifyCancel := context.WithTimeout(context.Background(), 30*time.Second)
			if notifyErr := feishuNotifier.Notify(notifyCtx, proj, inc, report); notifyErr != nil {
				log.Error("feishu.failed",
					logger.String("incident_id", inc.ID),
					logger.Err(notifyErr),
				)
			} else {
				report.Notified = true
				storeReport.Notified = true
			}
			notifyCancel()
		}

		// Save report and update task — use independent context because
		// the diagnosis context may be cancelled by this point.
		sCtx, sCancel = storeCtx()
		if saveErr := dataStore.SaveReport(sCtx, storeReport); saveErr != nil {
			log.Error("store.save_report_failed", logger.Err(saveErr))
		}

		finishedAt := time.Now()
		storeTask.Status = store.StatusCompleted
		storeTask.SessionID = report.SessionID
		storeTask.DurationMs = report.DurationMs
		storeTask.NumTurns = report.NumTurns
		storeTask.FinishedAt = &finishedAt
		if report.Usage != nil {
			storeTask.InputTokens = report.Usage.InputTokens
			storeTask.OutputTokens = report.Usage.OutputTokens
		}
		if updateErr := dataStore.UpdateTask(sCtx, storeTask); updateErr != nil {
			log.Error("store.update_task_failed", logger.Err(updateErr))
		}
		sCancel()

		log.Info("diagnosis.report",
			logger.String("incident_id", inc.ID),
			logger.String("task_id", taskID),
			logger.String("project", report.ProjectKey),
			logger.Bool("has_issue", report.HasIssue),
			logger.String("confidence", report.Confidence),
			logger.Bool("notified", report.Notified),
		)

		return nil
	}

	// Initialize scheduler
	sched := scheduler.New(scheduler.Config{
		MaxConcurrency: cfg.Scheduler.MaxConcurrency,
		QueueSize:      cfg.Scheduler.QueueSize,
		DefaultTimeout: ParseDuration(cfg.Scheduler.DefaultTimeout, 15*time.Minute),
		RetryCount:     cfg.Scheduler.RetryCount,
		RetryDelay:     ParseDuration(cfg.Scheduler.RetryDelay, 10*time.Second),
	}, diagnoseFn, log)
	sched.Start()

	// Resolve intake auth token
	intakeToken := cfg.Intake.AuthToken
	if intakeToken == "" {
		intakeToken = os.Getenv("INTAKE_AUTH_TOKEN")
	}

	// Initialize intake handler
	handler := intake.NewHandler(intake.HandlerConfig{
		AuthToken:   intakeToken,
		DedupWindow: ParseDuration(cfg.Intake.DedupWindow, 10*time.Minute),
		RateLimit:   cfg.Intake.RateLimit,
		MinSeverity: cfg.Intake.MinSeverity,
	}, log, registry.Exists, func(inc *intake.Incident) (string, error) {
		// Persist incident
		storeInc := &store.Incident{
			ID:          inc.ID,
			ProjectKey:  inc.ProjectKey,
			Title:       inc.Title,
			ErrorType:   inc.ErrorType,
			ErrorMsg:    inc.ErrorMsg,
			Stacktrace:  inc.Stacktrace,
			Environment: inc.Environment,
			Severity:    inc.Severity,
			URL:         inc.URL,
			Source:      inc.Source,
			Status:      "pending",
			Metadata:    inc.Metadata,
			OccurredAt:  inc.OccurredAt,
			ReportedAt:  inc.ReportedAt,
		}
		incCtx, incCancel := storeCtx()
		if createErr := dataStore.CreateIncident(incCtx, storeInc); createErr != nil {
			log.Error("store.create_incident_failed", logger.Err(createErr))
		}
		incCancel()

		taskID, err := sched.Submit(inc)
		if err != nil {
			return "", err
		}
		return taskID, nil
	})

	// HTTP server
	mux := http.NewServeMux()
	mux.Handle("/api/v1/incidents", handler)
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","projects":%d}`, registry.Len())
	})
	mux.HandleFunc("/api/v1/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sched.Stats())
	})

	server := &http.Server{
		Addr:              cfg.Intake.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Start HTTP server
	fatalCh := make(chan error, 1)
	go func() {
		log.Info("intake.listening", logger.String("addr", cfg.Intake.Listen))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("intake.listen_failed", logger.Err(err))
			fatalCh <- err
		}
	}()

	// Start Admin API server (if enabled)
	var adminServer *http.Server
	if cfg.AdminAPI.Enabled {
		adminToken := cfg.AdminAPI.AuthToken
		if adminToken == "" {
			adminToken = os.Getenv("ADMIN_API_TOKEN")
		}
		adminAPI := api.NewServer(dataStore, registry, sched, log, func(inc *intake.Incident) (string, error) {
			return sched.Submit(inc)
		}, adminToken)
		adminServer = &http.Server{
			Addr:              cfg.AdminAPI.Listen,
			Handler:           adminAPI.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		go func() {
			log.Info("admin.listening", logger.String("addr", cfg.AdminAPI.Listen))
			if err := adminServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Error("admin.listen_failed", logger.Err(err))
			}
		}()
	}

	log.Info("sentinel.ready",
		logger.Int("projects", registry.Len()),
		logger.Int("concurrency", cfg.Scheduler.MaxConcurrency),
		logger.String("listen", cfg.Intake.Listen),
	)
	if cfg.AdminAPI.Enabled {
		log.Info("admin.dashboard", logger.String("url", "http://localhost"+cfg.AdminAPI.Listen+"/admin/dashboard/"))
	}

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Info("sentinel.shutdown", logger.String("signal", sig.String()))
	case err := <-fatalCh:
		log.Error("sentinel.fatal", logger.Err(err))
	}

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if adminServer != nil {
		adminServer.Shutdown(ctx)
	}
	server.Shutdown(ctx)
	handler.StopCleanup()
	sched.Stop()

	log.Info("sentinel.stopped")
}
