package cmd

import (
	"errors"
	"os"
	"reflect"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

type Config struct {
	Log    LogConfig    `mapstructure:"log"`
	AWS    AWSConfig    `mapstructure:"aws"`
	Milvus MilvusConfig `mapstructure:"milvus"`
}

type AWSConfig struct {
	Region   string `mapstructure:"region"`
	Endpoint string `mapstructure:"endpoint"`
}

type MilvusConfig struct {
	Local          bool   `mapstructure:"local"`
	OperatorName   string `mapstructure:"operator_name"`
	Namespace      string `mapstructure:"namespace"`
	Username       string `mapstructure:"username"`
	Password       string `mapstructure:"password"`
	RootBucket     string `mapstructure:"root_bucket"`
	RootPath       string `mapstructure:"root_path"`
	BackupBucket   string `mapstructure:"backup_bucket"`
	BackupEtcdPath string `mapstructure:"backup_etcd_path"`
	BackupS3Path   string `mapstructure:"backup_s3_path"`
}

// GRPCAddr returns the Milvus gRPC address.
// When Local is true, returns localhost:19530.
// Otherwise derives it from OperatorName: {operator_name}-milvus:19530.
func (m MilvusConfig) GRPCAddr() string {
	if m.Local {
		return "localhost:19530"
	}
	return m.OperatorName + "-milvus:19530"
}

// EtcdEndpoints returns the etcd endpoint list.
// When Local is true, returns [localhost:2379].
// Otherwise derives it from OperatorName: {operator_name}-etcd:2379.
func (m MilvusConfig) EtcdEndpoints() []string {
	if m.Local {
		return []string{"localhost:2379"}
	}
	return []string{m.OperatorName + "-etcd:2379"}
}

type LogConfig struct {
	Level  string `mapstructure:"level" default:"info" validate:"oneof=debug info warn error fatal panic"`
	Format string `mapstructure:"format" default:"text" validate:"oneof=json text"`
}

// rootCmd represents the base command when called without any subcommands.
var rootCmd = &cobra.Command{
	Use:   "milvus-utils",
	Short: "A set of common utilities to manage milvus.",
	Long:  `A set of common utilities to manage milvus. Has capabilities to create and restore snapshots.`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		logrus.WithError(err).Fatal("Failed to execute root command")
	}
}

func init() { //nolint:gochecknoinits
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file (default is config.yaml)")
}

// InitConfig initialises viper with config files and env vars.
// Exported so standalone scripts can call it before LoadConfig.
func InitConfig() {
	initConfig()
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	setDefaults(Config{})

	viper.AddConfigPath(".")
	viper.AddConfigPath("/config")

	// First we load the default configs
	viper.SetConfigName("config")
	if err := viper.ReadInConfig(); err == nil {
		logrus.Info("Using config file: ", viper.ConfigFileUsed())
	}

	// Then we load the secrets configs (if any) and merge it with the default configs
	viper.SetConfigName("secrets")
	err := viper.MergeInConfig()
	if err == nil {
		logrus.Info("Merging config file: ", viper.ConfigFileUsed())
	} else if !errors.As(err, &viper.ConfigFileNotFoundError{}) {
		logrus.WithError(err).Error("Failed to merge secrets config file")
	}

	// Then we load the config file specified by the user (if any) --config flag and merge it with the default configs
	if cfgFile != "" {
		// Ensure the config file exists
		// This is a workaround for viper not checking if the file exists
		_, err := os.Stat(cfgFile)
		if err != nil {
			logrus.WithError(err).Fatal("Failed to load config file")
		}

		viper.SetConfigFile(cfgFile)
		if err := viper.MergeInConfig(); err == nil {
			logrus.Info("Merging config file: ", viper.ConfigFileUsed())
		}
	}

	viper.AutomaticEnv() // read in environment variables that match
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
}

func LoadConfig() (*Config, error) {
	var config Config
	bindEnvs(config)

	err := viper.Unmarshal(&config)
	if err != nil {
		return nil, err
	}

	return &config, nil
}

func GetLogger(config LogConfig) *logrus.Logger {
	logger := logrus.New()
	lvl, err := logrus.ParseLevel(config.Level)
	if err != nil {
		lvl = logrus.InfoLevel
		logger.WithError(err).Warnf("Failed to parse log level, setting log level to '%s'", lvl)
	}
	logger.SetLevel(lvl)
	switch config.Format {
	case "json":
		logger.SetFormatter(&logrus.JSONFormatter{})
	case "text":
		logger.SetFormatter(&logrus.TextFormatter{})
	}

	return logger
}

// Adapted from https://github.com/spf13/viper/issues/188#issuecomment-401431526
func bindEnvs(iface interface{}, parts ...string) {
	ifv := reflect.ValueOf(iface)
	ift := reflect.TypeOf(iface)
	for i := 0; i < ift.NumField(); i++ {
		fieldv := ifv.Field(i)
		t := ift.Field(i)
		name := strings.ToLower(t.Name)
		tag, ok := t.Tag.Lookup("mapstructure")
		if ok {
			name = tag
		}
		fieldParts := append(parts, name) //nolint:gocritic
		switch fieldv.Kind() {            //nolint:exhaustive
		case reflect.Struct:
			bindEnvs(fieldv.Interface(), fieldParts...)
		default:
			viper.BindEnv(strings.Join(fieldParts, ".")) //nolint:errcheck
		}
	}
}

// setDefaults walks the config struct and registers each field's `default` tag with viper.
func setDefaults(iface interface{}, parts ...string) {
	ifv := reflect.ValueOf(iface)
	ift := reflect.TypeOf(iface)
	for i := 0; i < ift.NumField(); i++ {
		fieldv := ifv.Field(i)
		t := ift.Field(i)
		name := strings.ToLower(t.Name)
		if tag, ok := t.Tag.Lookup("mapstructure"); ok {
			name = tag
		}
		fieldParts := append(parts, name) //nolint:gocritic
		switch fieldv.Kind() {            //nolint:exhaustive
		case reflect.Struct:
			setDefaults(fieldv.Interface(), fieldParts...)
		default:
			if defaultVal, ok := t.Tag.Lookup("default"); ok {
				viper.SetDefault(strings.Join(fieldParts, "."), defaultVal)
			}
		}
	}
}
