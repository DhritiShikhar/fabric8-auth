package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/user"
	"runtime"
	"time"

	"github.com/fabric8-services/fabric8-auth/app"
	factorymanager "github.com/fabric8-services/fabric8-auth/application/factory/manager"
	"github.com/fabric8-services/fabric8-auth/application/transaction"
	accountservice "github.com/fabric8-services/fabric8-auth/authentication/account/service"
	"github.com/fabric8-services/fabric8-auth/authorization/token/manager"
	"github.com/fabric8-services/fabric8-auth/configuration"
	"github.com/fabric8-services/fabric8-auth/controller"
	"github.com/fabric8-services/fabric8-auth/goamiddleware"
	"github.com/fabric8-services/fabric8-auth/gormapplication"
	"github.com/fabric8-services/fabric8-auth/jsonapi"
	"github.com/fabric8-services/fabric8-auth/log"
	"github.com/fabric8-services/fabric8-auth/migration"
	"github.com/fabric8-services/fabric8-auth/sentry"

	"github.com/goadesign/goa"
	"github.com/goadesign/goa/logging/logrus"
	"github.com/goadesign/goa/middleware"
	"github.com/goadesign/goa/middleware/gzip"
	"github.com/goadesign/goa/middleware/security/jwt"
	"github.com/jinzhu/gorm"
	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	// --------------------------------------------------------------------
	// Parse flags
	// --------------------------------------------------------------------
	var configFile string
	var serviceAccountConfigFile string
	var printConfig bool
	var migrateDB bool
	flag.StringVar(&configFile, "config", "", "Path to the config file to read")
	flag.StringVar(&serviceAccountConfigFile, "serviceAccountConfig", "", "Path to the service account configuration file")
	flag.BoolVar(&printConfig, "printConfig", false, "Prints the config (including merged environment variables) and exits")
	flag.BoolVar(&migrateDB, "migrateDatabase", false, "Migrates the database to the newest version and exits.")
	flag.Parse()

	// Override default -config switch with environment variable only if -config switch was
	// not explicitly given via the command line.
	configFile = configFileFromFlags("config", "AUTH_CONFIG_FILE_PATH")
	serviceAccountConfigFile = configFileFromFlags("serviceAccountConfig", "AUTH_SERVICE_ACCOUNT_CONFIG_FILE")

	config, err := configuration.NewConfigurationData(configFile, serviceAccountConfigFile)
	if err != nil {
		log.Panic(nil, map[string]interface{}{
			"config_file":                 configFile,
			"service_account_config_file": serviceAccountConfigFile,
			"err":                         err,
		}, "failed to setup the configuration")
	}

	if printConfig {
		os.Exit(0)
	}

	// Initialized developer mode flag and log level for the logger
	log.InitializeLogger(config.IsLogJSON(), config.GetLogLevel())

	printUserInfo()

	var db *gorm.DB
	for {
		db, err = gorm.Open("postgres", config.GetPostgresConfigString())
		if err != nil {
			db.Close()
			log.Logger().Errorf("ERROR: Unable to open connection to database %v", err)
			log.Logger().Infof("Retrying to connect in %v...", config.GetPostgresConnectionRetrySleep())
			time.Sleep(config.GetPostgresConnectionRetrySleep())
		} else {
			defer db.Close()
			break
		}
	}

	// Initialize sentry client
	haltSentry, err := sentry.InitializeSentryClient(
		config.GetSentryDSN(),
		sentry.WithRelease(controller.Commit),
		sentry.WithEnvironment(config.GetEnvironment()),
	)
	if err != nil {
		log.Panic(nil, map[string]interface{}{
			"err": err,
		}, "failed to setup the sentry client")
	}
	defer haltSentry()

	if config.IsPostgresDeveloperModeEnabled() && log.IsDebug() {
		db = db.Debug()
	}

	if config.GetPostgresConnectionMaxIdle() > 0 {
		log.Logger().Infof("Configured connection pool max idle %v", config.GetPostgresConnectionMaxIdle())
		db.DB().SetMaxIdleConns(config.GetPostgresConnectionMaxIdle())
	}
	if config.GetPostgresConnectionMaxOpen() > 0 {
		log.Logger().Infof("Configured connection pool max open %v", config.GetPostgresConnectionMaxOpen())
		db.DB().SetMaxOpenConns(config.GetPostgresConnectionMaxOpen())
	}

	// Set the database transaction timeout
	transaction.SetDatabaseTransactionTimeout(config.GetPostgresTransactionTimeout())

	// Migrate the schema
	err = migration.Migrate(db.DB(), config.GetPostgresDatabase(), config)
	if err != nil {
		log.Panic(nil, map[string]interface{}{
			"err": err,
		}, "failed migration")
	}

	// Nothing to here except exit, since the migration is already performed.
	if migrateDB {
		os.Exit(0)
	}

	// Create service
	service := goa.New("auth")

	// Mount middleware
	service.Use(middleware.RequestID())
	// Use our own log request to inject identity id and modify other properties
	service.Use(log.LogRequest(config.IsPostgresDeveloperModeEnabled()))
	service.Use(gzip.Middleware(9))
	service.Use(jsonapi.ErrorHandler(service, true))
	service.Use(middleware.Recover())

	service.WithLogger(goalogrus.New(log.Logger()))

	// Setup Account/Login/Security
	appDB := gormapplication.NewGormDB(db, config, factorymanager.NewDisabledFactoryWrappers())

	tokenManager, err := manager.DefaultManager(config)
	if err != nil {
		log.Panic(nil, map[string]interface{}{
			"err": err,
		}, "failed to create token manager")
	}
	// Middleware that extracts and stores the token in the context
	jwtMiddlewareTokenContext := goamiddleware.TokenContext(tokenManager, app.NewJWTSecurity())
	service.Use(jwtMiddlewareTokenContext)

	service.Use(manager.InjectTokenManager(tokenManager))
	service.Use(log.LogRequest(config.IsPostgresDeveloperModeEnabled()))
	app.UseJWTMiddleware(service, jwt.New(tokenManager.PublicKeys(), nil, app.NewJWTSecurity()))

	var tenantService accountservice.TenantService
	if config.GetTenantServiceURL() != "" {
		log.Logger().Infof("Enabling Tenant service %v", config.GetTenantServiceURL())
		tenantService = accountservice.NewTenantService(config)
	} else {
		log.Logger().Warn("Tenant service is not enabled")
	}

	// Try to fetch the initial list of clusters and start Cluster Service cache refresher
	_, err = appDB.ClusterService().Status(context.Background(), func(c *http.Client) {
		c.Timeout = 3 * time.Second
	})
	if err != nil {
		// It's not a critical error. Cluster management service can be offline during Auth service startup.
		// Cluster service during startup requires Auth service to be ready to fetch public keys.
		// So, we can't introduce cycle dependency in Auth service startup on Cluster service.
		// If fetching clusters upfront failed in main function then let's just log this error and continue to start the service.
		// We will try to fetch clusters later when we need them during user registration or OSO-OSIO account linking.
		log.Warn(nil, map[string]interface{}{
			"err": err,
		}, "failed to fetch clusters")
	}

	// Mount "login" controller
	loginCtrl := controller.NewLoginController(service, appDB)
	app.MountLoginController(service, loginCtrl)

	// Mount "resource-roles" controller
	resourceRoleCtrl := controller.NewResourceRolesController(service, appDB)
	app.MountResourceRolesController(service, resourceRoleCtrl)

	// Mount "roles" controller
	rolesCtrl := controller.NewRolesController(service, appDB)
	app.MountRolesController(service, rolesCtrl)

	// Mount "authorize" controller
	authorizeCtrl := controller.NewAuthorizeController(service, appDB, config)
	app.MountAuthorizeController(service, authorizeCtrl)

	// Mount "logout" controller
	logoutCtrl := controller.NewLogoutController(service, appDB)
	app.MountLogoutController(service, logoutCtrl)

	// Mount "token" controller
	tokenCtrl := controller.NewTokenController(service, appDB, tokenManager, config)
	app.MountTokenController(service, tokenCtrl)

	// Mount "status" controller
	statusCtrl := controller.NewStatusController(service, controller.NewGormDBChecker(db), config)
	app.MountStatusController(service, statusCtrl)

	// Mount "space" controller
	spaceCtrl := controller.NewSpaceController(service, appDB)
	app.MountSpaceController(service, spaceCtrl)

	// Mount "open-configuration" controller
	openidConfigurationCtrl := controller.NewOpenidConfigurationController(service)
	app.MountOpenidConfigurationController(service, openidConfigurationCtrl)

	// Mount "user" controller
	userCtrl := controller.NewUserController(service, appDB, config, tokenManager, tenantService)
	app.MountUserController(service, userCtrl)

	// Mount "search" controller
	searchCtrl := controller.NewSearchController(service, appDB, config)
	app.MountSearchController(service, searchCtrl)

	// Mount "users" controller
	emailVerificationService := accountservice.NewEmailVerificationClient(appDB)
	usersCtrl := controller.NewUsersController(service, appDB, config)
	usersCtrl.EmailVerificationService = emailVerificationService
	app.MountUsersController(service, usersCtrl)

	// Mount "namedusers" controlller
	namedusersCtrl := controller.NewNamedusersController(service, appDB, config, tenantService)
	app.MountNamedusersController(service, namedusersCtrl)

	//Mount "userinfo" controller
	userInfoCtrl := controller.NewUserinfoController(service, appDB, tokenManager)
	app.MountUserinfoController(service, userInfoCtrl)

	// Mount "collaborators" controller
	collaboratorsCtrl := controller.NewCollaboratorsController(service, appDB, config)
	app.MountCollaboratorsController(service, collaboratorsCtrl)

	// Mount "clusters" controller
	clustersCtrl := controller.NewClustersController(service, config)
	app.MountClustersController(service, clustersCtrl)

	// Mount "resources" controller
	resourcesCtrl := controller.NewResourceController(service, appDB)
	app.MountResourceController(service, resourcesCtrl)

	// Mount "organizations" controller
	organizationCtrl := controller.NewOrganizationController(service, appDB)
	app.MountOrganizationController(service, organizationCtrl)

	// Mount "teams" controller
	teamCtrl := controller.NewTeamController(service, appDB)
	app.MountTeamController(service, teamCtrl)

	// Mount "invitations" controller
	invitationCtrl := controller.NewInvitationController(service, appDB, config)
	app.MountInvitationController(service, invitationCtrl)

	log.Logger().Infoln("Git Commit SHA: ", controller.Commit)
	log.Logger().Infoln("UTC Build Time: ", controller.BuildTime)
	log.Logger().Infoln("UTC Start Time: ", controller.StartTime)
	log.Logger().Infoln("Dev mode:       ", config.IsPostgresDeveloperModeEnabled())
	log.Logger().Infoln("GOMAXPROCS:     ", runtime.GOMAXPROCS(-1))
	log.Logger().Infoln("NumCPU:         ", runtime.NumCPU())

	http.Handle("/api/", service.Mux)
	http.Handle("/favicon.ico", http.NotFoundHandler())

	// Start/mount metrics http
	if config.GetHTTPAddress() == config.GetMetricsHTTPAddress() {
		http.Handle("/metrics", prometheus.Handler())
	} else {
		go func(metricAddress string) {
			mx := http.NewServeMux()
			mx.Handle("/metrics", prometheus.Handler())
			if err := http.ListenAndServe(metricAddress, mx); err != nil {
				log.Error(nil, map[string]interface{}{
					"addr": metricAddress,
					"err":  err,
				}, "unable to connect to metrics server")
				service.LogError("startup", "err", err)
			}
		}(config.GetMetricsHTTPAddress())
	}

	// Start http
	if err := http.ListenAndServe(config.GetHTTPAddress(), nil); err != nil {
		log.Error(nil, map[string]interface{}{
			"addr": config.GetHTTPAddress(),
			"err":  err,
		}, "unable to connect to server")
		service.LogError("startup", "err", err)
	}
}

func configFileFromFlags(flagName string, envVarName string) string {
	configSwitchIsSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == flagName {
			configSwitchIsSet = true
		}
	})
	if !configSwitchIsSet {
		if envConfigPath, ok := os.LookupEnv(envVarName); ok {
			return envConfigPath
		}
	}
	return ""
}

func printUserInfo() {
	u, err := user.Current()
	if err != nil {
		log.Warn(nil, map[string]interface{}{
			"err": err,
		}, "failed to get current user")
	} else {
		log.Info(nil, map[string]interface{}{
			"username": u.Username,
			"uuid":     u.Uid,
		}, "Running as user name '%s' with UID %s.", u.Username, u.Uid)
		g, err := user.LookupGroupId(u.Gid)
		if err != nil {
			log.Warn(nil, map[string]interface{}{
				"err": err,
			}, "failed to lookup group")
		} else {
			log.Info(nil, map[string]interface{}{
				"groupname": g.Name,
				"gid":       g.Gid,
			}, "Running as as group '%s' with GID %s.", g.Name, g.Gid)
		}
	}
}
